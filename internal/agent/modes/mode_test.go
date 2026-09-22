package modes

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

func TestModeNames(t *testing.T) {
	for _, name := range []string{"ask", "code", "plan", "agent", "review", "test", "terminal", "deliberate"} {
		t.Run(name, func(t *testing.T) {
			// 只构造模式，不初始化 Runtime、不访问模型或全局配置。
			mode := For(name, nil)
			if got := mode.Name(); got != name {
				t.Errorf("Name() = %q, want %q", got, name)
			}
		})
	}
}

func TestAppendRepairInstructionPreservesTaskInput(t *testing.T) {
	task := domain.NewTask("为登录功能编写测试", "test", domain.SessionID(7))
	original := *task
	inv := core.NewInvocation(task.ID, "/workspace", llm.ModelInfo{})
	inv.AppendMessage(llm.RoleUser, task.Input)
	inv.AppendAssistant("已添加测试", nil)
	messages := append([]llm.ChatMessage(nil), inv.Messages...)

	appendRepairInstruction(inv, "修复缺失的断言")

	if !reflect.DeepEqual(*task, original) {
		t.Fatalf("repair instruction changed task: %+v", task)
	}
	if len(inv.Messages) != len(messages)+1 {
		t.Fatalf("got %d messages, want %d", len(inv.Messages), len(messages)+1)
	}
	if !reflect.DeepEqual(inv.Messages[:len(messages)], messages) {
		t.Error("repair instruction changed existing execution messages")
	}
	instruction := inv.Messages[len(messages)]
	if instruction.Role != llm.RoleUser || instruction.Content != "## 修复方案\n修复缺失的断言" {
		t.Errorf("unexpected repair instruction: %+v", instruction)
	}
}

func TestCriticInvocationIsolation(t *testing.T) {
	task := domain.NewTask("原始问题", "deliberate", domain.SessionID(7))
	provider := llm.ModelInfo{
		Provider: "caller-provider", ModelID: "caller-model", BaseURL: "https://caller.invalid",
		AllowedTools: []string{"read", "bash"},
	}
	parent := core.NewInvocation(task.ID, "/caller/workspace", provider)
	parent.AppendMessage(llm.RoleUser, task.Input)
	parent.AppendAssistant("初始回答", nil)
	parent.TotalUsage = llm.Usage{TotalTokens: 12}
	parent.OtterState = &llm.OtterState{ChatSessionID: "caller-session", Delivered: 2}
	messages := append([]llm.ChatMessage(nil), parent.Messages...)

	first := newCriticInvocation(parent, task.Input, "初始回答", "第一个视角")
	second := newCriticInvocation(parent, task.Input, "初始回答", "第二个视角")
	if len(parent.Children) != 2 || parent.Children[0] != first || parent.Children[1] != second {
		t.Fatal("critic invocations are not attached to their parent")
	}
	if first.ID == parent.ID || second.ID == first.ID {
		t.Error("critic invocations must have independent IDs")
	}
	wantProvider := provider
	wantProvider.AllowedTools = []string{"read"}
	for _, child := range parent.Children {
		if child.TaskID != task.ID || child.ProjectPath != parent.ProjectPath || !reflect.DeepEqual(child.Provider, wantProvider) {
			t.Errorf("critic did not inherit caller settings: %+v", child)
		}
		if child.OtterState != nil || child.TotalUsage != (llm.Usage{}) {
			t.Error("critic inherited caller execution state")
		}
		if len(child.Messages) != 2 || child.Messages[0].Role != llm.RoleSystem || child.Messages[1].Role != llm.RoleUser {
			t.Fatalf("unexpected critic messages: %+v", child.Messages)
		}
		if !strings.Contains(child.Messages[1].Content, task.Input) || !strings.Contains(child.Messages[1].Content, "初始回答") {
			t.Error("critic input must contain the original question and explicit answer")
		}
	}

	first.Messages[0].Content = "changed"
	first.Provider.AllowedTools[0] = "changed"
	first.TotalUsage.TotalTokens = 99
	first.OtterState = &llm.OtterState{ChatSessionID: "critic-session"}
	if !reflect.DeepEqual(parent.Messages, messages) || !reflect.DeepEqual(parent.Provider, provider) || parent.TotalUsage.TotalTokens != 12 || parent.OtterState.ChatSessionID != "caller-session" {
		t.Error("critic state leaked into parent")
	}
	if second.Messages[0].Content != "第二个视角" || second.Provider.AllowedTools[0] != "read" || second.TotalUsage.TotalTokens != 0 || second.OtterState != nil {
		t.Error("critic state leaked into sibling")
	}
	if task.Input != "原始问题" {
		t.Error("critic changed original task input")
	}
}

func TestCriticInvocationDoesNotExpandTools(t *testing.T) {
	for _, allowed := range [][]string{{}, {"bash"}} {
		parent := core.NewInvocation(domain.TaskID(1), "/workspace", llm.ModelInfo{AllowedTools: allowed})
		child := newCriticInvocation(parent, "input", "answer", "critic")
		if child.Provider.AllowedTools == nil || len(child.Provider.AllowedTools) != 0 {
			t.Errorf("critic expanded tools %v to %v", allowed, child.Provider.AllowedTools)
		}
		if !reflect.DeepEqual(parent.Provider.AllowedTools, allowed) {
			t.Error("critic changed parent tools")
		}
	}
}
