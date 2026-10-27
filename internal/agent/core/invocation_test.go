package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

func TestInvocationOwnsModelState(t *testing.T) {
	provider := llm.ModelInfo{Provider: "test", ModelID: "model", APIKey: "secret-value", AllowedTools: []string{"read"}}
	inv := NewInvocation(domain.TaskID(7), "/project", provider)
	provider.AllowedTools[0] = "write"
	if inv.Provider.AllowedTools[0] != "read" {
		t.Fatal("provider tools share caller memory")
	}
	calls := []llm.ToolCall{{ID: "call", Name: "read", Arguments: `{}`}}
	inv.AppendAssistant("inspect", calls)
	calls[0].Name = "write"
	if inv.Messages[0].ToolCalls[0].Name != "read" {
		t.Fatal("tool calls share caller memory")
	}
	inv.AppendToolResult("call", "read result")
	inv.AddUsage(llm.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	state := llm.OtterState{ChatSessionID: "remote", RemoteMetadata: map[string]string{"cursor": "one"}, Delivered: 2}
	inv.SetOtterState(state)
	state.RemoteMetadata["cursor"] = "two"
	restored := inv.GetOtterState()
	restored.RemoteMetadata["cursor"] = "three"
	if inv.OtterState.RemoteMetadata["cursor"] != "one" {
		t.Fatal("provider cursor shares mutable maps")
	}
	data, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-value") || strings.Contains(string(data), "AllowedTools") {
		t.Fatalf("runtime connection settings leaked: %s", data)
	}
	child := inv.NewChild()
	if child.ID == inv.ID || child.TaskID != inv.TaskID || child.ProjectPath != inv.ProjectPath || len(child.Messages) != 0 || child.TotalUsage.TotalTokens != 0 || child.OtterState != nil {
		t.Fatalf("child inherits execution state: %+v", child)
	}
	child.Provider.AllowedTools[0] = "write"
	if inv.Provider.AllowedTools[0] != "read" {
		t.Fatal("child permissions mutate parent")
	}
}

func TestInvocationPreservesExplicitlyEmptyTools(t *testing.T) {
	for _, allowed := range [][]string{nil, {}} {
		inv := NewInvocation(1, "/project", llm.ModelInfo{AllowedTools: allowed})
		if (inv.Provider.AllowedTools == nil) != (allowed == nil) {
			t.Fatal("constructor changed nil/empty tool policy")
		}
		child := inv.NewChild()
		if (child.Provider.AllowedTools == nil) != (allowed == nil) {
			t.Fatal("child changed nil/empty tool policy")
		}
	}
}
