package agent

import (
	"testing"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

func TestStructuredContractsRejectInventedSuccess(t *testing.T) {
	for _, text := range []string{"completed successfully", `{"status":"passed","summary":"ok","evidence":[]}`, `{"status":"unknown","summary":"ok","evidence":["x"]}`, `{"status":"passed","summary":"ok","evidence":["x"],"secret":1}`, `{"status":"passed","summary":"ok","evidence":["x"]} {}`} {
		if _, err := ParseVerification(text); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
	if _, err := ParseVerification(`{"status":"passed","summary":"ok","evidence":["exit 0"]}`); err != nil {
		t.Fatal(err)
	}
	p, _ := Role("planner")
	for _, text := range []string{`{"goal":"g","steps":[]}`, `{"goal":"g","steps":[{"id":"a","description":"d","dependencies":["a"]}]}`, `{"goal":"g","steps":[{"id":"a","description":"d","dependencies":["missing"]}]}`} {
		if err := ValidateResult(p, text); err == nil {
			t.Fatalf("accepted plan %s", text)
		}
	}
}

func TestReadOnlyRolesHaveNoShell(t *testing.T) {
	for _, name := range []string{"responder", "reviewer", "planner", "verifier", "critic-security", "critic-correctness", "judge"} {
		p, err := Role(name)
		if err != nil {
			t.Fatal(err)
		}
		if p.Capabilities.Allows("bash") || p.Capabilities.Allows("write") {
			t.Fatalf("unsafe role %s", name)
		}
	}
}

func TestRebaseInvalidatesCursor(t *testing.T) {
	inv := &domain.Invocation{Phase: "model", Thread: domain.Thread{ContextVersion: 1, Cursor: &model.Cursor{Sequence: 4}}}
	if err := Rebase(inv, []model.Message{{Role: "user", Content: "goal"}}); err != nil {
		t.Fatal(err)
	}
	if inv.Thread.ContextVersion != 2 || inv.Thread.Cursor != nil {
		t.Fatal("stale cursor survived context replacement")
	}
	inv.Phase = "tools"
	inv.Pending = []model.Call{{ID: "x"}}
	if err := Rebase(inv, nil); err == nil {
		t.Fatal("lost outstanding tool call")
	}
}
