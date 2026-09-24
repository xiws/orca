// Package domain 定义核心领域模型，包括 Run、Invocation、Session、Task 等核心类型及其状态转换规则。
package domain

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/xiws/orca/internal/model"
)

// RunID 运行实例的唯一标识符。
type RunID int64

// InvocationID 调用实例的唯一标识符。
type InvocationID int64

// InputRequestID 输入请求的唯一标识符。
type InputRequestID int64

// NewID 使用加密随机数生成一个正的奇数 int64 ID。
func NewID() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	// 取最高位为 0 保证正数，最低位为 1 保证奇数。
	return int64(binary.BigEndian.Uint64(b[:])&((1<<63)-1)) | 1
}

// RunState 运行状态类型。
type RunState string

const (
	// Queued 排队中，尚未开始执行。
	Queued RunState = "queued"
	// Running 正在执行中。
	Running RunState = "running"
	// Waiting 等待用户输入或审批。
	Waiting RunState = "waiting"
	// Interrupted 被中断，需要恢复。
	Interrupted RunState = "interrupted"
	// Reconciling 对账中，外部操作结果不确定。
	Reconciling RunState = "reconciling"
	// Cancelling 正在取消。
	Cancelling RunState = "cancelling"
	// Succeeded 执行成功（终态）。
	Succeeded RunState = "succeeded"
	// Failed 执行失败（终态）。
	Failed RunState = "failed"
	// Cancelled 已取消（终态）。
	Cancelled RunState = "cancelled"
)

// Terminal 判断当前状态是否为终态（succeeded/failed/cancelled）。
func (s RunState) Terminal() bool { return s == Succeeded || s == Failed || s == Cancelled }

// CanTransition 检查从 from 到 to 的状态转换是否合法。
func CanTransition(from, to RunState) bool {
	if from == to {
		return true
	}
	// 终态不允许任何转换。
	if from.Terminal() {
		return false
	}
	// 任何非终态都可以转为 cancelling。
	if to == Cancelling {
		return true
	}
	switch from {
	case Queued:
		return to == Running || to == Interrupted || to == Failed
	case Running:
		return to == Waiting || to == Interrupted || to == Reconciling || to == Succeeded || to == Failed
	case Waiting:
		return to == Running || to == Interrupted || to == Reconciling || to == Failed
	case Interrupted:
		return to == Running || to == Reconciling || to == Failed
	case Reconciling:
		return to == Interrupted || to == Failed
	case Cancelling:
		return to == Cancelled || to == Reconciling
	}
	return false
}

// Policy 定义工具授权策略，包含允许使用的工具和自动审批的工具。
type Policy struct {
	Tools       []string `json:"tools"`                  // 允许使用的工具列表
	AutoApprove []string `json:"auto_approve,omitempty"` // 自动审批的工具列表
}

// Allows 检查策略是否允许使用指定工具。
func (p Policy) Allows(name string) bool {
	for _, t := range p.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// Approved 检查指定工具是否在自动审批列表中。
func (p Policy) Approved(name string) bool {
	for _, t := range p.AutoApprove {
		if t == name {
			return true
		}
	}
	return false
}

// Intersect 计算两个策略的交集：只有在两个策略中都被允许的工具才保留，
// 自动审批需要双方都授权。
func (p Policy) Intersect(other Policy) Policy {
	r := Policy{Tools: []string{}}
	for _, t := range p.Tools {
		if other.Allows(t) && !r.Allows(t) {
			r.Tools = append(r.Tools, t)
			if p.Approved(t) && other.Approved(t) {
				r.AutoApprove = append(r.AutoApprove, t)
			}
		}
	}
	return r
}

// Limits 定义执行的资源限制。
type Limits struct {
	MaxTurns    int   `json:"max_turns"`    // 最大轮次
	MaxTokens   int64 `json:"max_tokens"`   // 最大 token 数
	MaxChildren int   `json:"max_children"` // 最大子任务数
	MaxDepth    int   `json:"max_depth"`    // 最大嵌套深度
	MaxRepairs  int   `json:"max_repairs"`  // 最大修复次数
	Deadline    int64 `json:"deadline"`     // 截止时间
}

// DefaultLimits 返回默认的 exec 资源限制配置。
func DefaultLimits() Limits {
	return Limits{MaxTurns: 64, MaxTokens: 200000, MaxChildren: 16, MaxDepth: 4, MaxRepairs: 2}
}

// Budget 记录当前已消耗的资源预算。
type Budget struct {
	Turns    int   `json:"turns"`    // 已使用轮次
	Tokens   int64 `json:"tokens"`   // 已使用 token 数
	Reserved int64 `json:"reserved"` // 已预留 token 数
	Children int   `json:"children"` // 已创建子任务数
}

// Node 表示工作流模板中的一个执行节点。
type Node struct {
	ID           string       `json:"id"`                      // 节点 ID
	Role         string       `json:"role"`                    // 角色名称（executor/validator/verifier 等）
	State        string       `json:"state"`                   // 节点状态（pending/running/completed/failed）
	Attempt      int          `json:"attempt"`                 // 当前尝试次数
	InvocationID InvocationID `json:"invocation_id,omitempty"` // 关联的调用 ID
	Result       string       `json:"result,omitempty"`        // 节点执行结果
}

// Run 表示一次完整的执行运行，包含任务、模型、策略、预算、节点列表等信息。
type Run struct {
	ID              RunID           `json:"id"`                      // 运行 ID
	TaskID          TaskID          `json:"task_id"`                 // 任务 ID
	TaskVersion     int             `json:"task_version"`            // 任务版本
	SessionID       SessionID       `json:"session_id,omitempty"`    // 会话 ID
	RootRunID       RunID           `json:"root_run_id"`             // 根运行 ID
	ParentRunID     RunID           `json:"parent_run_id,omitempty"` // 父运行 ID（用于子任务）
	Depth           int             `json:"depth"`                   // 嵌套深度
	Workspace       string          `json:"workspace"`               // 工作空间路径
	Mode            string          `json:"mode"`                    // 运行模式（code/agent/test 等）
	TemplateVersion int             `json:"template_version"`        // 模板版本
	Model           model.Ref       `json:"model"`                   // 使用的模型
	Policy          Policy          `json:"policy"`                  // 工具授权策略
	Limits          Limits          `json:"limits"`                  // 资源限制
	Budget          Budget          `json:"budget"`                  // 已消耗预算
	State           RunState        `json:"state"`                   // 当前运行状态
	Nodes           []Node          `json:"nodes"`                   // 工作流节点列表
	Input           []model.Message `json:"input,omitempty"`         // 输入消息
	InputFrozen     bool            `json:"input_frozen"`            // 输入是否已冻结
	Prompt          string          `json:"prompt"`                  // 用户提示词
	SystemPrompt    string          `json:"system_prompt,omitempty"` // 系统提示词
	WaitingID       InputRequestID  `json:"waiting_id,omitempty"`    // 等待的输入请求 ID
	Repairs         int             `json:"repairs"`                 // 已修复次数
	Result          string          `json:"result,omitempty"`        // 运行结果
	Error           string          `json:"error,omitempty"`         // 错误信息
	Version         int64           `json:"version"`                 // CAS 版本号
	CreatedAt       int64           `json:"created_at"`              // 创建时间
}

// Transition 尝试将运行状态转换到目标状态，不合法时返回错误。
func (r *Run) Transition(to RunState) error {
	if !CanTransition(r.State, to) {
		return fmt.Errorf("illegal run transition %s -> %s", r.State, to)
	}
	r.State = to
	return nil
}

// Thread 表示一次调用中的对话线程上下文。
type Thread struct {
	ContextVersion int             `json:"context_version"`  // 上下文版本
	SequenceOffset int             `json:"sequence_offset"`  // 消息序列偏移
	Messages       []model.Message `json:"messages"`         // 对话消息列表
	Cursor         *model.Cursor   `json:"cursor,omitempty"` // 续接游标
}

// Invocation 表示一次模型调用，关联到运行中的某个节点。
type Invocation struct {
	ID                InvocationID `json:"id"`                 // 调用 ID
	RunID             RunID        `json:"run_id"`             // 所属运行 ID
	NodeID            string       `json:"node_id"`            // 节点 ID
	Attempt           int          `json:"attempt"`            // 尝试次数
	Role              string       `json:"role"`               // 角色名称
	RoleVersion       int          `json:"role_version"`       // 角色版本
	Model             model.Ref    `json:"model"`              // 使用的模型
	Policy            Policy       `json:"policy"`             // 角色策略（与运行策略的交集）
	Thread            Thread       `json:"thread"`             // 对话线程
	Phase             string       `json:"phase"`              // 当前阶段
	Pending           []model.Call `json:"pending,omitempty"`  // 待执行的工具调用
	NextCall          int          `json:"next_call"`          // 下一个待执行调用索引
	AssistantSequence int          `json:"assistant_sequence"` // 助手消息序列号
	Reservation       int64        `json:"reservation"`        // 预留 token 数
	Usage             model.Usage  `json:"usage"`              // token 使用统计
	Result            string       `json:"result,omitempty"`   // 调用结果
	Error             string       `json:"error,omitempty"`    // 错误信息
	Version           int64        `json:"version"`            // CAS 版本号
}

// InputRequest 表示一个等待用户响应或审批的输入请求。
type InputRequest struct {
	ID           InputRequestID `json:"id"`                 // 请求 ID
	RunID        RunID          `json:"run_id"`             // 所属运行 ID
	InvocationID InvocationID   `json:"invocation_id"`      // 所属调用 ID
	Kind         string         `json:"kind"`               // 类型（input/approval）
	CallKey      string         `json:"call_key,omitempty"` // 工具调用键
	Prompt       string         `json:"prompt"`             // 提示内容
	State        string         `json:"state"`              // 状态（pending/answered/approved/rejected）
	Response     string         `json:"response,omitempty"` // 用户响应内容
	Approved     bool           `json:"approved"`           // 是否已批准
	ExpiresAt    int64          `json:"expires_at"`         // 过期时间（审批类型）
	Version      int64          `json:"version"`            // CAS 版本号
}

// ToolExecution 记录一次工具执行的完整状态。
type ToolExecution struct {
	Key          string       `json:"key"`              // 唯一键（由运行、调用和参数生成）
	RunID        RunID        `json:"run_id"`           // 所属运行 ID
	InvocationID InvocationID `json:"invocation_id"`    // 所属调用 ID
	Name         string       `json:"name"`             // 工具名称
	Arguments    string       `json:"arguments"`        // 规范化后的参数 JSON
	Workspace    string       `json:"workspace"`        // 工作空间路径
	State        string       `json:"state"`            // 状态（prepared/started/completed/unknown）
	Effect       string       `json:"effect"`           // 效果类型（read/external）
	Result       string       `json:"result,omitempty"` // 执行结果
	ExitCode     int          `json:"exit_code"`        // 退出码
	Version      int64        `json:"version"`          // CAS 版本号
}

// Delegation 记录一次任务委派，将子目标分配给子运行。
type Delegation struct {
	Key          string       `json:"key"`           // 唯一键
	ParentRunID  RunID        `json:"parent_run_id"` // 父运行 ID
	InvocationID InvocationID `json:"invocation_id"` // 触发委派的调用 ID
	Children     []RunID      `json:"children"`      // 子运行 ID 列表
}

// Artifact 记录运行过程中产生的产物（如节点执行结果）。
type Artifact struct {
	ID           int64        `json:"id"`            // 产物 ID
	RunID        RunID        `json:"run_id"`        // 所属运行 ID
	InvocationID InvocationID `json:"invocation_id"` // 所属调用 ID
	Kind         string       `json:"kind"`          // 产物类型（通常为角色名称）
	Content      string       `json:"content"`       // 产物内容
}

// Event 表示运行过程中的事件流条目，用于实时状态更新。
type Event struct {
	Sequence          int64        `json:"sequence"`                     // 事件序列号（自增）
	RunID             RunID        `json:"run_id"`                       // 所属运行 ID
	InvocationID      InvocationID `json:"invocation_id,omitempty"`      // 关联的调用 ID
	AssistantSequence int          `json:"assistant_sequence,omitempty"` // 助手消息序列号
	Kind              string       `json:"kind"`                         // 事件类型（state/waiting/completed/failed）
	Content           string       `json:"content"`                      // 事件内容
}

// ErrConflict 状态版本冲突错误。
var ErrConflict = errors.New("state version conflict")

// ErrNotFound 记录未找到错误。
var ErrNotFound = errors.New("record not found")

// ErrBudget 执行预算耗尽错误。
var ErrBudget = errors.New("execution budget exhausted")

// ErrUnknown 外部操作结果未知，需要对账。
var ErrUnknown = errors.New("external operation outcome unknown; reconciliation required")

// ErrCheckpoint 检查点不可用，执行已停止。
var ErrCheckpoint = errors.New("checkpoint unavailable; execution stopped")

// Mutation 表示一组原子提交的状态变更。
// Version 是期望的当前版本号，0 表示插入新记录。
type Mutation struct {
	Sessions    []*Session
	Tasks       []*Task
	Runs        []*Run
	Invocations []*Invocation
	Inputs      []*InputRequest
	Tools       []*ToolExecution
	Delegations []*Delegation
	Artifacts   []*Artifact
	Events      []Event
}
