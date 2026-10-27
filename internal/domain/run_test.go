package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunTransitions(t *testing.T) {
	for _, terminal := range []RunState{Succeeded, Failed, Cancelled} {
		for _, next := range []RunState{Queued, Running, Waiting, Interrupted, Reconciling, Cancelling} {
			if CanTransition(terminal, next) {
				t.Fatalf("terminal %s resurrected as %s", terminal, next)
			}
		}
	}
	for _, pair := range [][2]RunState{{Queued, Running}, {Running, Waiting}, {Waiting, Running}, {Running, Reconciling}, {Reconciling, Interrupted}, {Cancelling, Cancelled}} {
		if !CanTransition(pair[0], pair[1]) {
			t.Fatalf("valid transition rejected: %v", pair)
		}
	}
	if CanTransition(Queued, Succeeded) || CanTransition(Reconciling, Running) {
		t.Fatal("unchecked execution or reconciliation transition")
	}
}

func TestPolicyIntersectionNeverExpands(t *testing.T) {
	parent := Policy{Tools: []string{"read", "bash"}, AutoApprove: []string{"bash"}}
	role := Policy{Tools: []string{"read"}}
	result := parent.Intersect(role)
	if !result.Allows("read") || result.Allows("bash") || result.Approved("bash") {
		t.Fatalf("expanded policy: %+v", result)
	}
	if parent.Intersect(Policy{}).Allows("read") {
		t.Fatal("empty policy expanded privileges")
	}
}

func TestDomainRecordsCannotSerializeCredentials(t *testing.T) {
	for _, value := range []any{Run{}, Invocation{}, Task{}, Session{}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"api_key", "APIKey", "base_url", "Authorization"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("credential field %s", forbidden)
			}
		}
	}
}
