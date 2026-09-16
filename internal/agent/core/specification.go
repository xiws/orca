package core

// Specification 是 Clarifier Agent 输出的结构化需求描述。
// 它将模糊的用户需求转换为可执行的规格，供 Planner 和 Executor 使用。
type Specification struct {
	// Goal 任务目标的一句话描述。
	Goal string `json:"goal"`

	// Requirements 具体的功能或非功能需求列表。
	Requirements []Requirement `json:"requirements"`

	// Constraints 实现约束，如技术栈、兼容性、风格规范等。
	Constraints []string `json:"constraints"`

	// AcceptanceCriteria 验收标准，Verifier 据此判断任务是否完成。
	AcceptanceCriteria []string `json:"acceptance_criteria"`

	// Assumptions Clarifier 做出的合理假设（未向用户确认的）。
	Assumptions []string `json:"assumptions"`
}

// Requirement 描述一条具体需求。
type Requirement struct {
	// ID 需求唯一标识。
	ID string `json:"id"`

	// Description 需求描述。
	Description string `json:"description"`

	// Priority 优先级：high / medium / low。
	Priority string `json:"priority"`
}
