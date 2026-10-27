// Package tools 提供内置工具的统一策略、审批和执行边界。
// Gateway 在其打开的工作区内序列化工具调用。
// Bash 是已批准的外部操作，而非文件系统或安全沙箱。
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

// Ledger 定义工具执行记录的持久化接口
type Ledger interface {
	Apply(context.Context, domain.Mutation) error
	Tool(context.Context, string) (*domain.ToolExecution, error)
	InputByCall(context.Context, string) (*domain.InputRequest, error)
}

// Goal 表示委派任务的目标
type Goal struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Result 表示工具执行结果
type Result struct {
	Content string               // 工具返回的 JSON 内容
	Waiting *domain.InputRequest // 非 nil 表示正在等待用户输入
	Goals   []Goal               // 委派任务的目标列表
}

// Gateway 是工具执行的网关，通过互斥锁序列化工作区内的工具调用
type Gateway struct {
	mu        sync.Mutex
	root      *os.Root
	workspace string
	ledger    Ledger
	closed    bool
}

// New 创建新的工具网关，绑定到指定工作区和执行记录存储
func New(workspace string, ledger Ledger) (*Gateway, error) {
	if ledger == nil {
		return nil, fmt.Errorf("tool ledger is required")
	}
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("workspace is required")
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &Gateway{root: root, workspace: abs, ledger: ledger}, nil
}

// Close 关闭网关和工作区根目录
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	return g.root.Close()
}

// toolResult 构建工具成功执行的返回结果
func toolResult(call model.Call, data map[string]any) Result {
	data["call_id"] = call.ID
	data["tool"] = call.Name
	if _, exists := data["ok"]; !exists {
		data["ok"] = true
	}
	b, _ := json.Marshal(data)
	return Result{Content: string(b)}
}

// toolError 构建工具执行错误的返回结果
func toolError(call model.Call, code string, err error) Result {
	return toolResult(call, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": err.Error()}})
}

// Execute 执行工具调用。每次调用时读取最新的 Run 和 Invocation 策略快照。
// 等待请求不在此处写入：由 Runner 在提交 Invocation 阶段和 Run 状态时原子写入。
func (g *Gateway) Execute(ctx context.Context, run *domain.Run, inv *domain.Invocation, call model.Call, key string) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if g.closed {
		return Result{}, os.ErrClosed
	}
	e := lookup(call.Name)
	if e == nil {
		return toolError(call, "unknown_tool", fmt.Errorf("unknown tool %q", call.Name)), nil
	}
	if run == nil || inv == nil || inv.RunID != run.ID || key == "" {
		return toolError(call, "invalid_context", fmt.Errorf("run, matching invocation, and call key are required")), nil
	}
	workspace, err := filepath.Abs(run.Workspace)
	if err != nil || run.Workspace == "" || workspace != g.workspace {
		return toolError(call, "workspace_mismatch", fmt.Errorf("run workspace does not match gateway workspace %q", g.workspace)), nil
	}
	if !run.Policy.Intersect(inv.Policy).Allows(call.Name) {
		return toolError(call, "not_allowed", fmt.Errorf("tool %q is not allowed by both current run and role policies", call.Name)), nil
	}
	args, canonical, err := e.parse(call.Arguments)
	if err != nil {
		return toolError(call, "invalid_arguments", err), nil
	}
	// 在创建审批或执行记录之前，先拒绝词法逃逸路径
	if name := stringArg(args, "filename"); name != "" {
		if _, err := g.relative(name); err != nil {
			return toolError(call, "invalid_path", err), nil
		}
	}
	if call.Name == "bash" {
		if _, err := g.relative(stringArg(args, "workdir")); err != nil {
			return toolError(call, "invalid_path", err), nil
		}
	}
	if e.control {
		if call.Name == "create_task" {
			goals := make([]Goal, 0, len(args["task_target"].([]any)))
			for _, v := range args["task_target"].([]any) {
				goal := v.(map[string]any)
				goals = append(goals, Goal{Title: stringArg(goal, "title"), Description: stringArg(goal, "description")})
			}
			r := toolResult(call, map[string]any{"goals": goals})
			r.Goals = goals
			return r, nil
		}
		return g.input(ctx, run, inv, call, key, canonical, "input", false)
	}

	record, err := g.ledger.Tool(ctx, key)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return Result{}, err
	}
	if record != nil {
		if record.RunID != run.ID || record.InvocationID != inv.ID || record.Name != call.Name || record.Arguments != canonical || record.Workspace != g.workspace {
			return toolError(call, "call_key_conflict", fmt.Errorf("call key is bound to different arguments, workspace, tool, or invocation")), nil
		}
		switch record.State {
		case "completed":
			return Result{Content: record.Result}, nil
		case "prepared":
			// prepared 状态证明尚未开始外部操作，可以继续
		default:
			return Result{}, domain.ErrUnknown
		}
	}
	if e.sideEffect {
		r, err := g.input(ctx, run, inv, call, key, canonical, "approval", run.Policy.Intersect(inv.Policy).Approved(call.Name))
		if err != nil || r.Waiting != nil || r.Content != "" {
			return r, err
		}
	}
	// 执行操作前重新校验工具授权，审批不能替代当前授权检查
	if !run.Policy.Intersect(inv.Policy).Allows(call.Name) {
		return toolError(call, "not_allowed", fmt.Errorf("tool authorization was revoked")), nil
	}
	if record == nil {
		effect := "read"
		if e.sideEffect {
			effect = "external"
		}
		record = &domain.ToolExecution{Key: key, RunID: run.ID, InvocationID: inv.ID, Name: call.Name, Arguments: canonical, Workspace: g.workspace, State: "prepared", Effect: effect}
		if err := g.ledger.Apply(ctx, domain.Mutation{Tools: []*domain.ToolExecution{record}}); err != nil {
			return Result{}, err
		}
		// 读取已提交的版本，不依赖 Apply 是否会修改输入指针
		record, err = g.ledger.Tool(ctx, key)
		if err != nil {
			return Result{}, err
		}
		if record == nil || record.State != "prepared" {
			return Result{}, domain.ErrUnknown
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	started := *record
	started.State = "started"
	if err := g.ledger.Apply(ctx, domain.Mutation{Tools: []*domain.ToolExecution{&started}}); err != nil {
		return Result{}, err
	}
	record, err = g.ledger.Tool(ctx, key)
	if err != nil {
		return Result{}, errors.Join(domain.ErrUnknown, err)
	}
	if record == nil || record.State != "started" {
		return Result{}, domain.ErrUnknown
	}
	if err := ctx.Err(); err != nil {
		return g.unknown(ctx, record, Result{}, err)
	}

	r, exitCode, actionErr := g.perform(ctx, call, args)
	if ctx.Err() != nil || errors.Is(actionErr, context.Canceled) || errors.Is(actionErr, context.DeadlineExceeded) {
		return g.unknown(ctx, record, r, errors.Join(actionErr, ctx.Err()))
	}
	if actionErr != nil {
		code := "execution_failed"
		if errors.Is(actionErr, domain.ErrConflict) {
			code = "conflict"
		}
		r = toolError(call, code, actionErr)
	}
	done := *record
	done.State, done.Result, done.ExitCode = "completed", r.Content, exitCode
	if err := g.ledger.Apply(ctx, domain.Mutation{Tools: []*domain.ToolExecution{&done}}); err != nil {
		// 持久化成功结果失败时不返回成功，started 记录会阻止自动重试
		return Result{}, errors.Join(domain.ErrUnknown, err)
	}
	return r, nil
}

// unknown 将工具执行标记为不确定状态，即使上下文已取消也会尝试记录
func (g *Gateway) unknown(ctx context.Context, record *domain.ToolExecution, r Result, cause error) (Result, error) {
	unknown := *record
	unknown.State, unknown.Result = "unknown", r.Content
	// 取消不能阻止记录不确定的副作用。使用独立上下文尽力提交
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	err := g.ledger.Apply(cleanup, domain.Mutation{Tools: []*domain.ToolExecution{&unknown}})
	return Result{}, errors.Join(domain.ErrUnknown, cause, err)
}

// input 处理用户输入/审批请求的创建和状态查询
func (g *Gateway) input(ctx context.Context, run *domain.Run, inv *domain.Invocation, call model.Call, key, canonical, kind string, auto bool) (Result, error) {
	binding := struct {
		Kind      string          `json:"kind"`
		Workspace string          `json:"workspace"`
		Tool      string          `json:"tool"`
		CallID    string          `json:"call_id"`
		Arguments json.RawMessage `json:"arguments"`
		Notice    string          `json:"notice"`
	}{kind, g.workspace, call.Name, call.ID, json.RawMessage(canonical), "Approve only these exact arguments in this workspace."}
	if kind == "input" {
		binding.Notice = "Answer the prompt in arguments to continue this tool call."
	}
	if call.Name == "bash" {
		binding.Notice += " Bash is not a sandbox and can access outside this workspace."
	}
	b, _ := json.MarshalIndent(binding, "", "  ")
	prompt := string(b)
	input, err := g.ledger.InputByCall(ctx, key)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return Result{}, err
	}
	if input != nil {
		if input.RunID != run.ID || input.InvocationID != inv.ID || input.Kind != kind || input.CallKey != key || input.Prompt != prompt {
			return toolError(call, "approval_binding_conflict", fmt.Errorf("input/approval belongs to different arguments, workspace, tool, or invocation")), nil
		}
		if input.State == "pending" {
			copy := *input
			return Result{Waiting: &copy}, nil
		}
		if kind == "input" && input.State == "answered" {
			return Result{Content: input.Response}, nil
		}
		if input.State == "rejected" || (kind == "approval" && input.State == "answered" && !input.Approved) {
			return toolError(call, "approval_rejected", fmt.Errorf("user rejected this request")), nil
		}
		if kind == "approval" && (input.State == "approved" || (input.State == "answered" && input.Approved)) {
			if input.ExpiresAt <= time.Now().Unix() {
				return toolError(call, "approval_expired", fmt.Errorf("approval has expired; a new call and approval are required")), nil
			}
			return Result{}, nil
		}
		return toolError(call, "input_unavailable", fmt.Errorf("input request is in state %q", input.State)), nil
	}
	if auto && kind == "approval" {
		return Result{}, nil
	}
	expires := int64(0)
	if kind == "approval" {
		expires = time.Now().Add(30 * time.Minute).Unix()
	}
	return Result{Waiting: &domain.InputRequest{ID: domain.InputRequestID(domain.NewID()), RunID: run.ID, InvocationID: inv.ID, Kind: kind, CallKey: key, Prompt: prompt, State: "pending", ExpiresAt: expires}}, nil
}

// relative 将路径转换为工作区内的相对路径，拒绝父目录遍历和路径逃逸
func (g *Gateway) relative(name string) (string, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("path contains NUL")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return "", fmt.Errorf("parent traversal is not allowed")
		}
	}
	if name == "" {
		return ".", nil
	}
	if filepath.IsAbs(name) {
		rel, err := filepath.Rel(g.workspace, name)
		if err != nil {
			return "", err
		}
		name = rel
	}
	name = filepath.Clean(name)
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("path is outside the workspace")
	}
	return name, nil
}

// perform 根据工具名称分发到具体的执行函数
func (g *Gateway) perform(ctx context.Context, call model.Call, args map[string]any) (Result, int, error) {
	switch call.Name {
	case "read":
		r, err := g.read(ctx, call, args)
		return r, 0, err
	case "write", "edit":
		r, err := g.write(ctx, call, args)
		return r, 0, err
	case "bash":
		return g.bash(ctx, call, args)
	default:
		return Result{}, 0, fmt.Errorf("tool has no executor")
	}
}
