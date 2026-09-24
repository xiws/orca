package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/tools"
)

// ExecutionStore 是执行状态的持久化接口，支持状态变更、运行查询和调用查询。
type ExecutionStore interface {
	// Apply 应用一个状态变更（mutation）。
	Apply(context.Context, domain.Mutation) error
	// Run 按 ID 查询运行状态。
	Run(context.Context, domain.RunID) (*domain.Run, error)
	// Invocation 按 ID 查询调用状态。
	Invocation(context.Context, domain.InvocationID) (*domain.Invocation, error)
}

// ToolGateway 是工具调用的网关，负责提供工具定义和执行工具调用。
type ToolGateway interface {
	// Definitions 返回指定权限策略下的可用工具定义。
	Definitions(domain.Policy) []model.Tool
	// Execute 执行一次工具调用。
	Execute(context.Context, *domain.Run, *domain.Invocation, model.Call, string) (tools.Result, error)
}

// Outcome 描述一次 Advance 调用的结果。
type Outcome struct {
	// Kind 结果类型：completed / failed / input / delegated。
	Kind string
	// InvocationID 相关的调用 ID。
	InvocationID domain.InvocationID
	// Result 调用结果文本（Kind=completed 时有效）。
	Result string
	// Err 错误信息（Kind=failed 时有效）。
	Err error
	// Key 委托调用的唯一键（Kind=delegated 时有效）。
	Key string
	// Goals 委托目标列表（Kind=delegated 时有效）。
	Goals []tools.Goal
	// Input 输入请求（Kind=input 时有效）。
	Input *domain.InputRequest
}

// Runner 是 Agent 的执行状态机，管理并发限制、预算控制和调用推进。
// 它是 Agent 执行引擎的核心，负责将调用从 checkpoint 状态推进到下一个状态。
type Runner struct {
	// store 执行状态的持久化存储。
	store ExecutionStore
	// client LLM 客户端，用于调用模型。
	client model.Client
	// tools 工具网关，管理工具定义和执行。
	tools ToolGateway
	// limitMu 并发控制的互斥锁。
	limitMu sync.Mutex
	// limit 全局最大并发调用数。
	limit int
	// inFlight 当前正在执行的调用数。
	inFlight int
	// providers 每个 Provider 的当前并发数。
	providers map[string]int
	// available 并发槽位可用的信号通道。
	available chan struct{}
	// budgetMu 预算控制的互斥锁。
	budgetMu sync.Mutex
	// active 当前活跃的调用 ID 集合（用于冲突检测）。
	active sync.Map
	// Sink 事件接收器，用于上报执行事件（可选）。
	Sink func(domain.Event)
}

// NewRunner 创建执行状态机。
// concurrency 控制全局最大并发调用数，最小为 1。
func NewRunner(store ExecutionStore, client model.Client, gateway ToolGateway, concurrency int) *Runner {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Runner{store: store, client: client, tools: gateway, limit: concurrency, providers: map[string]int{}, available: make(chan struct{})}
}

// Advance 将调用从当前 checkpoint 推进到下一个状态。
// 每次调用只推进一步：完成模型调用、执行工具或等待用户输入。
// resumeInput 用于恢复委托（delegated）阶段的调用。
// 同一调用不能并发执行（通过 active map 检测冲突）。
func (r *Runner) Advance(ctx context.Context, id domain.InvocationID, resumeInput string) Outcome {
	out := Outcome{InvocationID: id}
	// 检测并发冲突：同一调用不能同时执行
	if _, busy := r.active.LoadOrStore(id, struct{}{}); busy {
		out.Kind = "failed"
		out.Err = domain.ErrConflict
		return out
	}
	defer r.active.Delete(id)
	// 从存储中加载调用和运行状态
	inv, err := r.store.Invocation(ctx, id)
	if err != nil {
		out.Kind = "failed"
		out.Err = errors.Join(domain.ErrCheckpoint, err)
		return out
	}
	run, err := r.store.Run(ctx, inv.RunID)
	if err != nil {
		out.Kind = "failed"
		out.Err = errors.Join(domain.ErrCheckpoint, err)
		return out
	}
	// 校验角色版本兼容性
	profile, err := Role(inv.Role)
	if err != nil || inv.RoleVersion != profile.Version {
		if err == nil {
			err = fmt.Errorf("unsupported role version %d", inv.RoleVersion)
		}
		out.Kind = "failed"
		out.Err = err
		return out
	}
	// 计算交集权限：调用权限 ∩ 运行权限 ∩ 角色权限
	inv.Policy = inv.Policy.Intersect(run.Policy).Intersect(profile.Capabilities)
	// 设置截止时间
	if run.Limits.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Unix(run.Limits.Deadline, 0))
		defer cancel()
	}
	// 状态机主循环：每次循环推进一步
	for {
		// 检查上下文是否已取消
		if err := ctx.Err(); err != nil {
			out.Kind = "failed"
			out.Err = err
			return out
		}
		// 重新加载运行状态（可能被其他 goroutine 修改）
		run, err = r.store.Run(ctx, inv.RunID)
		if err != nil {
			out.Kind, out.Err = "failed", errors.Join(domain.ErrCheckpoint, err)
			return out
		}
		// 运行已被取消或正在协调，终止执行
		if run.State == domain.Cancelling || run.State.Terminal() || run.State == domain.Reconciling {
			out.Kind, out.Err = "failed", context.Canceled
			return out
		}
		// 每次循环重新计算权限交集
		inv.Policy = inv.Policy.Intersect(run.Policy).Intersect(profile.Capabilities)
		switch inv.Phase {
		case "completed":
			// 调用已完成，返回结果
			out.Kind = "completed"
			out.Result = inv.Result
			return out
		case "failed":
			// 调用已失败，返回错误
			out.Kind = "failed"
			out.Err = errors.New(inv.Error)
			return out
		case "model_started":
			// 处于 model_started 说明上次崩溃在模型调用期间，无法安全恢复
			out.Kind = "failed"
			out.Err = domain.ErrUnknown
			return out
		case "tools", "waiting", "delegated":
			// 工具执行阶段：所有工具已完成则回到模型阶段
			if inv.NextCall >= len(inv.Pending) {
				inv.Phase = "model"
				inv.Pending = nil
				inv.NextCall = 0
				if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				continue
			}
			call := inv.Pending[inv.NextCall]
			key := fmt.Sprintf("%d:%d:%s", id, inv.AssistantSequence, call.ID)
			// 处理委托恢复：外部提供了 resumeInput 时直接注入结果
			if inv.Phase == "delegated" && resumeInput != "" {
				inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: resumeInput})
				inv.NextCall++
				inv.Phase = "tools"
				resumeInput = ""
				if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				continue
			}
			// 执行工具调用
			result, err := r.tools.Execute(ctx, run, inv, call, key)
			if err != nil {
				out.Kind = "failed"
				out.Err = err
				// 未知错误和上下文错误不加 checkpoint 包装
				if !errors.Is(err, domain.ErrUnknown) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					out.Err = errors.Join(domain.ErrCheckpoint, err)
				}
				return out
			}
			// 工具返回等待输入
			if result.Waiting != nil {
				inv.Phase = "waiting"
				mutation := domain.Mutation{Invocations: []*domain.Invocation{inv}}
				if result.Waiting.Version == 0 {
					mutation.Inputs = []*domain.InputRequest{result.Waiting}
				}
				if err := r.commit(ctx, mutation); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				out.Kind = "input"
				out.Input = result.Waiting
				return out
			}
			// 工具返回委托目标
			if len(result.Goals) > 0 {
				inv.Phase = "delegated"
				if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				out.Kind = "delegated"
				out.Key = key
				out.Goals = result.Goals
				return out
			}
			// 工具正常返回结果，追加到消息历史
			inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: result.Content})
			inv.NextCall++
			inv.Phase = "tools"
			if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}, Events: []domain.Event{{RunID: run.ID, InvocationID: id, Kind: "tool", Content: result.Content}}}); err != nil {
				out.Kind = "failed"
				out.Err = err
				return out
			}
		case "model":
			// 模型调用阶段：先获取并发槽位
			if err := r.acquire(ctx, inv.Model.Provider); err != nil {
				out.Kind, out.Err = "failed", err
				return out
			}
			// 预留 token 预算
			err := r.reserve(ctx, run.RootRunID, inv)
			if err != nil {
				r.release(inv.Model.Provider)
				out.Kind = "failed"
				out.Err = err
				return out
			}
			// 计算 assistant 消息在序列中的位置
			assistantSequence := inv.Thread.SequenceOffset + len(inv.Thread.Messages) + 1
			// 调用 LLM 完成接口
			response, callErr := r.client.Complete(ctx, model.Request{
				Model: inv.Model, ThreadID: int64(inv.ID), ContextVersion: inv.Thread.ContextVersion,
				Messages: inv.Thread.Messages, Cursor: inv.Thread.Cursor, Tools: r.tools.Definitions(inv.Policy), MaxOutputTokens: 4096,
			}, func(delta string) {
				// 流式输出回调，上报增量事件
				if r.Sink != nil {
					r.Sink(domain.Event{RunID: run.ID, InvocationID: id, AssistantSequence: assistantSequence, Kind: "delta", Content: delta})
				}
			})
			r.release(inv.Model.Provider)
			// 处理模型调用失败
			if callErr != nil || !response.Complete {
				if callErr == nil {
					callErr = model.ErrUnknown
				}
				// 未知错误或上下文错误，直接返回失败
				if errors.Is(callErr, model.ErrUnknown) || ctx.Err() != nil {
					out.Kind = "failed"
					out.Err = errors.Join(domain.ErrUnknown, callErr)
					return out
				}
				// 已知的模型错误，标记调用为失败并结算预算
				inv.Phase = "failed"
				inv.Error = callErr.Error()
				if err := r.settle(ctx, run.RootRunID, inv, model.Usage{}, nil); err != nil {
					callErr = errors.Join(callErr, err)
				}
				out.Kind = "failed"
				out.Err = callErr
				return out
			}
			// 处理模型返回的消息
			message := response.Message
			message.Role = "assistant"
			// 为工具调用分配唯一 ID 并检测重复
			ids := map[string]bool{}
			for i := range message.ToolCalls {
				if message.ToolCalls[i].ID == "" {
					message.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", len(inv.Thread.Messages), i)
				}
				if ids[message.ToolCalls[i].ID] {
					out.Kind = "failed"
					out.Err = errors.Join(domain.ErrUnknown, fmt.Errorf("duplicate tool call identity"))
					return out
				}
				ids[message.ToolCalls[i].ID] = true
			}
			// 追加 assistant 消息到对话历史
			inv.Thread.Messages = append(inv.Thread.Messages, message)
			inv.Thread.Cursor = response.Cursor
			inv.AssistantSequence = inv.Thread.SequenceOffset + len(inv.Thread.Messages)
			inv.Pending = message.ToolCalls
			inv.NextCall = 0
			if len(message.ToolCalls) == 0 {
				// 无工具调用：模型给出了最终回答
				inv.Result = message.Content
				if err := ValidateResult(profile, message.Content); err != nil {
					inv.Phase = "failed"
					inv.Error = err.Error()
				} else {
					inv.Phase = "completed"
				}
			} else {
				// 有工具调用：进入工具执行阶段
				inv.Phase = "tools"
			}
			// 处理 token 用量统计
			usage := response.Usage
			if usage.TotalTokens <= 0 {
				// 模型未返回 token 统计时进行估算
				usage.TotalTokens, err = r.estimateTokens(inv)
				if err != nil {
					out.Kind, out.Err = "failed", errors.Join(domain.ErrCheckpoint, err)
					return out
				}
				usage.Estimated = true
			}
			inv.Usage.PromptTokens += usage.PromptTokens
			inv.Usage.CompletionTokens += usage.CompletionTokens
			inv.Usage.TotalTokens += usage.TotalTokens
			inv.Usage.Estimated = inv.Usage.Estimated || usage.Estimated
			// 结算预算并上报消息事件
			if err := r.settle(ctx, run.RootRunID, inv, usage, []domain.Event{{RunID: run.ID, InvocationID: id, AssistantSequence: inv.AssistantSequence, Kind: "message", Content: message.Content}}); err != nil {
				out.Kind = "failed"
				out.Err = err
				return out
			}
		default:
			out.Kind = "failed"
			out.Err = fmt.Errorf("invalid invocation checkpoint phase %q", inv.Phase)
			return out
		}
	}
}

// acquire 获取并发槽位。全局不超过 limit，每个 Provider 不超过 min(2, limit)。
// 如果当前无法获取则阻塞等待，直到有可用槽位或上下文取消。
func (r *Runner) acquire(ctx context.Context, provider string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.limitMu.Lock()
		// 检查全局和 Provider 级别的并发限制
		if r.inFlight < r.limit && r.providers[provider] < min(2, r.limit) {
			r.inFlight++
			r.providers[provider]++
			r.limitMu.Unlock()
			return nil
		}
		available := r.available
		r.limitMu.Unlock()
		// 阻塞等待并发槽位释放
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-available:
		}
	}
}

// release 释放一个并发槽位，并通知等待者。
func (r *Runner) release(provider string) {
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	r.inFlight--
	r.providers[provider]--
	// 通过关闭并重建通道来通知所有等待者
	close(r.available)
	r.available = make(chan struct{})
}

// estimateTokens 估算调用消耗的 token 数（当模型未返回实际用量时）。
func (r *Runner) estimateTokens(inv *domain.Invocation) (int64, error) {
	prompt, err := model.ToolPrompt(r.tools.Definitions(inv.Policy))
	if err != nil {
		return 0, err
	}
	// 估算消息和系统提示词的 token 总量
	return model.Estimate(inv.Thread.Messages) + model.Estimate([]model.Message{{Role: "system", Content: prompt}}), nil
}

// reserve 在调用模型前预留 token 预算。
// 使用乐观锁重试（最多 8 次），避免并发修改预算时产生冲突。
func (r *Runner) reserve(ctx context.Context, rootID domain.RunID, inv *domain.Invocation) error {
	r.budgetMu.Lock()
	defer r.budgetMu.Unlock()
	for tries := 0; tries < 8; tries++ {
		root, err := r.store.Run(ctx, rootID)
		if err != nil {
			return errors.Join(domain.ErrCheckpoint, err)
		}
		if root.State.Terminal() || root.State == domain.Cancelling {
			return context.Canceled
		}
		estimate, err := r.estimateTokens(inv)
		if err != nil {
			return err
		}
		// 预留估算量 + 4096 安全边际
		reserve := estimate + 4096
		// 检查是否超出预算限制
		if root.Budget.Turns >= root.Limits.MaxTurns || root.Budget.Tokens+root.Budget.Reserved+reserve > root.Limits.MaxTokens {
			return domain.ErrBudget
		}
		root.Budget.Turns++
		root.Budget.Reserved += reserve
		inv.Phase = "model_started"
		inv.Reservation = reserve
		err = r.commit(ctx, domain.Mutation{Runs: []*domain.Run{root}, Invocations: []*domain.Invocation{inv}})
		if !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return errors.Join(domain.ErrCheckpoint, domain.ErrConflict)
}

// settle 结算调用的实际 token 消耗，释放预留预算。
// 同样使用乐观锁重试（最多 8 次）。
func (r *Runner) settle(ctx context.Context, rootID domain.RunID, inv *domain.Invocation, usage model.Usage, events []domain.Event) error {
	r.budgetMu.Lock()
	defer r.budgetMu.Unlock()
	for tries := 0; tries < 8; tries++ {
		root, err := r.store.Run(ctx, rootID)
		if err != nil {
			return errors.Join(domain.ErrCheckpoint, err)
		}
		// 释放预留预算，计入实际消耗
		root.Budget.Reserved -= inv.Reservation
		root.Budget.Tokens += usage.TotalTokens
		reservation := inv.Reservation
		inv.Reservation = 0
		err = r.commit(ctx, domain.Mutation{Runs: []*domain.Run{root}, Invocations: []*domain.Invocation{inv}, Events: events})
		if !errors.Is(err, domain.ErrConflict) {
			return err
		}
		// 冲突时恢复预留量以便重试
		inv.Reservation = reservation
	}
	return errors.Join(domain.ErrCheckpoint, domain.ErrConflict)
}

// commit 将状态变更应用到存储，错误会包装为 ErrCheckpoint。
func (r *Runner) commit(ctx context.Context, mutation domain.Mutation) error {
	if err := r.store.Apply(ctx, mutation); err != nil {
		return errors.Join(domain.ErrCheckpoint, err)
	}
	return nil
}

// Rebase 替换调用的对话上下文（消息历史），用于上下文注入或重建。
// 只能在 model 阶段且没有待处理的工具调用时使用。
// 会增加 ContextVersion 使缓存失效，并重置 SequenceOffset。
func Rebase(inv *domain.Invocation, messages []model.Message) error {
	if inv.Phase != "model" || len(inv.Pending) > 0 {
		return fmt.Errorf("cannot replace context with pending tool results")
	}
	inv.Thread.ContextVersion++
	// 将旧消息的数量累加到偏移量，保持序列号连续
	inv.Thread.SequenceOffset += len(inv.Thread.Messages)
	// 深拷贝新消息列表，避免外部修改
	inv.Thread.Messages = append([]model.Message(nil), messages...)
	inv.Thread.Cursor = nil
	return nil
}
