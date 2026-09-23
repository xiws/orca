package workflow

import (
	"fmt"

	"github.com/xiws/orca/internal/domain"
)

type Template struct {
	Name    string
	Version int
	Roles   []string
	Policy  domain.Policy
}

func Mode(name string) (Template, error) {
	if name == "" {
		name = "code"
	}
	t := Template{Name: name, Version: 1, Policy: domain.Policy{Tools: []string{"read", "request_input"}}}
	switch name {
	case "ask":
		t.Roles = []string{"responder"}
	case "review":
		t.Roles = []string{"reviewer"}
	case "plan":
		t.Roles = []string{"planner"}
	case "code":
		t.Roles = []string{"executor"}
		t.Policy.Tools = []string{"read", "write", "edit", "bash", "create_task", "request_input"}
	case "agent":
		t.Roles = []string{"executor", "validator", "verifier"}
		t.Policy.Tools = []string{"read", "write", "edit", "bash", "create_task", "request_input"}
	case "test":
		t.Roles = []string{"executor", "validator", "verifier"}
		t.Policy.Tools = []string{"read", "write", "edit", "bash", "request_input"}
	case "terminal":
		t.Roles = []string{"terminal"}
		t.Policy.Tools = []string{"bash", "request_input"}
	case "deliberate":
		t.Roles = []string{"responder", "critic-correctness", "critic-security", "judge"}
	default:
		return Template{}, fmt.Errorf("unknown mode %q", name)
	}
	return t, nil
}

func (t Template) Nodes() []domain.Node {
	nodes := make([]domain.Node, len(t.Roles))
	for i, role := range t.Roles {
		nodes[i] = domain.Node{ID: fmt.Sprintf("%d-%s", i, role), Role: role, State: "pending", Attempt: 1}
	}
	return nodes
}
