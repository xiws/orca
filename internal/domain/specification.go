package domain

// Specification 描述一个任务的需求规格，包括目标、需求列表、约束、验收标准和假设。
type Specification struct {
	Goal               string        `json:"goal"`                // 目标描述
	Requirements       []Requirement `json:"requirements"`        // 需求列表
	Constraints        []string      `json:"constraints"`         // 约束条件
	AcceptanceCriteria []string      `json:"acceptance_criteria"` // 验收标准
	Assumptions        []string      `json:"assumptions"`         // 前提假设
}

// Requirement 表示规格中的单条需求，包含唯一标识、描述和优先级。
type Requirement struct {
	ID          string `json:"id"`          // 需求唯一标识
	Description string `json:"description"` // 需求描述
	Priority    string `json:"priority"`    // 优先级
}
