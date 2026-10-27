package roles

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
)

// Clarifier 是需求澄清 Agent。
// 它将模糊的用户需求转换为结构化的 Specification，
// 供 Planner 和 Executor 使用。
type Clarifier struct {
	Runtime *core.Runtime
}

// ClarifyStatus 表示 Clarifier 的输出状态。
type ClarifyStatus string

const (
	// ClarifyReady 需求已足够明确，可直接执行。
	ClarifyReady ClarifyStatus = "ready"

	// ClarifyQuestion 需求缺失关键信息，需要向用户提问。
	ClarifyQuestion ClarifyStatus = "question"

	// ClarifyAssumption 需求有歧义但可合理假设，不阻塞执行。
	ClarifyAssumption ClarifyStatus = "assumption"
)

// ClarifyResult 是 Clarifier 的输出。
type ClarifyResult struct {
	// Status 澄清结果状态：ready / question / assumption。
	Status ClarifyStatus `json:"status"`

	// Specification 结构化需求描述（Status=ready 或 assumption 时非空）。
	Specification *domain.Specification `json:"specification,omitempty"`

	// Questions 需要用户回答的问题列表（Status=question 时非空）。
	Questions []string `json:"questions,omitempty"`

	// Assumptions Clarifier 做出的合理假设（Status=assumption 时非空）。
	Assumptions []string `json:"assumptions,omitempty"`
}

// Clarify 分析用户输入，输出结构化 Specification。
//
// Clarifier 只使用调用方已允许的 read 工具读取项目上下文。
// 通过子 Invocation 执行独立的 LLM 对话。
func (c *Clarifier) Clarify(parentInv *core.Invocation, input string) (*ClarifyResult, error) {
	// 在子 Invocation 中执行 LLM 对话，限制工具为 read
	output, err := runRole(c.Runtime, parentInv, clarifierSystemPrompt, input, "read")
	if err != nil {
		return nil, err
	}

	return parseClarifyResult(output)
}

// parseClarifyResult 从 LLM 输出中解析 ClarifyResult。
// 目前采用简单策略：尝试从 JSON 中提取，失败则视为 ready 状态。
func parseClarifyResult(output string) (*ClarifyResult, error) {
	// TODO: 实现 JSON 解析逻辑
	// 当前简化处理：将 LLM 输出视为 ready 状态
	return &ClarifyResult{
		Status: ClarifyReady,
		Specification: &domain.Specification{
			Goal: output,
		},
	}, nil
}
