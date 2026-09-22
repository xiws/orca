package domain

type Specification struct {
	Goal               string        `json:"goal"`
	Requirements       []Requirement `json:"requirements"`
	Constraints        []string      `json:"constraints"`
	AcceptanceCriteria []string      `json:"acceptance_criteria"`
	Assumptions        []string      `json:"assumptions"`
}

type Requirement struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
}
