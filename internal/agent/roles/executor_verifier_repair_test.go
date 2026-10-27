package roles

import (
	"reflect"
	"testing"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

func TestFormatSpecification_Nil(t *testing.T) {
	result := FormatSpecification(nil)
	if result != "" {
		t.Errorf("expected empty string for nil spec, got %s", result)
	}
}

func TestFormatSpecification_Full(t *testing.T) {
	spec := &domain.Specification{
		Goal: "优化性能",
		Requirements: []domain.Requirement{
			{ID: "R1", Description: "增加缓存", Priority: "high"},
			{ID: "R2", Description: "优化查询", Priority: "medium"},
		},
		Constraints:        []string{"保持兼容"},
		AcceptanceCriteria: []string{"响应 < 100ms"},
		Assumptions:        []string{"使用 Redis"},
	}

	result := FormatSpecification(spec)
	if result == "" {
		t.Error("expected non-empty formatted spec")
	}

	// 检查各部分
	if !containsStr(result, "优化性能") {
		t.Error("expected goal in formatted spec")
	}
	if !containsStr(result, "增加缓存") {
		t.Error("expected requirement in formatted spec")
	}
	if !containsStr(result, "保持兼容") {
		t.Error("expected constraint in formatted spec")
	}
	if !containsStr(result, "响应 < 100ms") {
		t.Error("expected acceptance criteria in formatted spec")
	}
	if !containsStr(result, "使用 Redis") {
		t.Error("expected assumption in formatted spec")
	}
}

func TestVerifier_BuildVerifyInput(t *testing.T) {
	verifier := &Verifier{}
	task := domain.NewTask("实现功能", "verify", domain.SessionID(7))
	task.Specification = &domain.Specification{
		AcceptanceCriteria: []string{"测试通过", "编译成功"},
	}
	original := *task

	input := verifier.buildVerifyInput(task, "已完成所有修改")
	if input == "" {
		t.Error("expected non-empty verify input")
	}

	if !containsStr(input, "测试通过") {
		t.Error("expected acceptance criteria in input")
	}
	if !containsStr(input, "实现功能") {
		t.Error("expected original input in verify input")
	}
	if !containsStr(input, "已完成所有修改") {
		t.Error("expected explicit execution result in verify input")
	}

	retryInput := verifier.buildVerifyInput(task, "修复后的执行结果")
	if !containsStr(retryInput, "修复后的执行结果") || containsStr(retryInput, "已完成所有修改") {
		t.Error("verification reused a stale execution result")
	}
	if emptyInput := verifier.buildVerifyInput(task, ""); containsStr(emptyInput, "## Executor 的执行结果") {
		t.Error("empty explicit result should not reuse earlier execution output")
	}
	if !reflect.DeepEqual(*task, original) {
		t.Error("building verification input changed the task")
	}
}

func TestParseVerifyResult_Pass(t *testing.T) {
	result, err := parseVerifyResult("所有测试通过，任务完成")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected passed = true")
	}
}

func TestParseVerifyResult_Fail(t *testing.T) {
	result, err := parseVerifyResult("测试失败，存在问题")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected passed = false")
	}
	if len(result.Issues) == 0 {
		t.Error("expected issues for failed verification")
	}
}

func TestContainsKeyword(t *testing.T) {
	tests := []struct {
		text     string
		keywords []string
		expected bool
	}{
		{"PASS all tests", []string{"PASS"}, true},
		{"所有测试通过", []string{"通过"}, true},
		{"FAIL: error", []string{"FAIL"}, true},
		{"no match here", []string{"PASS", "FAIL"}, false},
		{"", []string{"PASS"}, false},
	}

	for _, tt := range tests {
		result := containsKeyword(tt.text, tt.keywords...)
		if result != tt.expected {
			t.Errorf("containsKeyword(%q, %v) = %v, expected %v", tt.text, tt.keywords, result, tt.expected)
		}
	}
}

func TestRepair_BuildRepairInput(t *testing.T) {
	repair := &Repair{}
	task := &domain.Task{
		Input: "实现功能",
		Specification: &domain.Specification{
			AcceptanceCriteria: []string{"测试通过"},
		},
	}
	verifyResult := &VerifyResult{
		Passed:  false,
		Summary: "编译失败",
		Issues: []Issue{
			{Description: "缺少 import", Severity: "critical"},
		},
	}

	input := repair.buildRepairInput(task, verifyResult)
	if input == "" {
		t.Error("expected non-empty repair input")
	}

	if !containsStr(input, "实现功能") {
		t.Error("expected original input in repair input")
	}
	if !containsStr(input, "编译失败") {
		t.Error("expected verify summary in repair input")
	}
	if !containsStr(input, "缺少 import") {
		t.Error("expected issue description in repair input")
	}
	if task.Input != "实现功能" {
		t.Error("building repair input changed the original task input")
	}
}

func TestRestrictToolsIntersection(t *testing.T) {
	tests := []struct {
		name      string
		inherited []string
		allowed   []string
		want      []string
	}{
		{name: "unrestricted", allowed: []string{"read"}, want: []string{"read"}},
		{name: "explicitly disabled", inherited: []string{}, allowed: []string{"read"}, want: []string{}},
		{name: "no overlap", inherited: []string{"bash"}, allowed: []string{"read"}, want: []string{}},
		{name: "read only", inherited: []string{"read"}, allowed: []string{"read", "bash"}, want: []string{"read"}},
		{name: "verification", inherited: []string{"read", "write", "bash"}, allowed: []string{"read", "bash"}, want: []string{"read", "bash"}},
		{name: "no role tools", inherited: []string{"read"}, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := core.NewInvocation(domain.TaskID(1), "/workspace", llm.ModelInfo{AllowedTools: tt.inherited})
			RestrictTools(inv, tt.allowed...)
			if !reflect.DeepEqual(inv.Provider.AllowedTools, tt.want) {
				t.Errorf("tools = %#v, want %#v", inv.Provider.AllowedTools, tt.want)
			}
			// 再次收紧不能让之前禁用的工具重新出现。
			RestrictTools(inv, "read", "bash", "write")
			if !reflect.DeepEqual(inv.Provider.AllowedTools, tt.want) {
				t.Errorf("repeated restriction expanded tools to %#v", inv.Provider.AllowedTools)
			}
		})
	}
}

func TestRoleInvocationIsolation(t *testing.T) {
	provider := llm.ModelInfo{
		Provider: "caller-provider", ModelID: "caller-model", BaseURL: "https://caller.invalid",
		AllowedTools: []string{"read", "write", "bash"},
	}
	parent := core.NewInvocation(domain.TaskID(42), "/caller/workspace", provider)
	parent.AppendMessage(llm.RoleUser, "parent input")
	parent.TotalUsage = llm.Usage{TotalTokens: 12}
	parent.OtterState = &llm.OtterState{ChatSessionID: "parent-session", Delivered: 1}
	messages := append([]llm.ChatMessage(nil), parent.Messages...)

	child := newRoleInvocation(parent, "system prompt", "role input", "read", "bash")
	if len(parent.Children) != 1 || parent.Children[0] != child {
		t.Fatal("role invocation is not attached to its parent")
	}
	if child.ID == parent.ID || child.TaskID != parent.TaskID || child.ProjectPath != parent.ProjectPath {
		t.Error("role must use a new invocation for the same task and workspace")
	}
	wantProvider := provider
	wantProvider.AllowedTools = []string{"read", "bash"}
	if !reflect.DeepEqual(child.Provider, wantProvider) {
		t.Errorf("role did not inherit caller provider: %+v", child.Provider)
	}
	if child.TotalUsage != (llm.Usage{}) || child.OtterState != nil || len(child.Children) != 0 {
		t.Error("role inherited execution state from parent")
	}
	if len(child.Messages) != 2 || child.Messages[0].Role != llm.RoleSystem || child.Messages[0].Content != "system prompt" || child.Messages[1].Role != llm.RoleUser || child.Messages[1].Content != "role input" {
		t.Fatalf("unexpected role messages: %+v", child.Messages)
	}

	child.Messages[0].Content = "changed"
	child.Provider.AllowedTools[0] = "changed"
	child.TotalUsage.TotalTokens = 99
	child.OtterState = &llm.OtterState{ChatSessionID: "child-session"}
	if !reflect.DeepEqual(parent.Messages, messages) || !reflect.DeepEqual(parent.Provider, provider) || parent.TotalUsage.TotalTokens != 12 || parent.OtterState.ChatSessionID != "parent-session" {
		t.Error("role execution state leaked into parent")
	}
}

func TestParseRepairResult(t *testing.T) {
	result, err := parseRepairResult("修改 file.go 增加 import")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FixPlan != "修改 file.go 增加 import" {
		t.Errorf("expected fix plan to match output, got %s", result.FixPlan)
	}
}

func TestVerifyResult_Structure(t *testing.T) {
	result := &VerifyResult{
		Passed: false,
		Issues: []Issue{
			{Description: "编译错误", Severity: "critical"},
			{Description: "缺少测试", Severity: "warning"},
			{Description: "可以优化", Severity: "info"},
		},
		Summary: "存在多个问题",
	}

	if result.Passed {
		t.Error("expected passed = false")
	}
	if len(result.Issues) != 3 {
		t.Errorf("expected 3 issues, got %d", len(result.Issues))
	}
}

func TestRepairResult_Structure(t *testing.T) {
	result := &RepairResult{
		FixPlan:       "修改文件 A 和 B",
		FilesToModify: []string{"a.go", "b.go"},
	}

	if result.FixPlan != "修改文件 A 和 B" {
		t.Errorf("unexpected fix plan: %s", result.FixPlan)
	}
	if len(result.FilesToModify) != 2 {
		t.Errorf("expected 2 files, got %d", len(result.FilesToModify))
	}
}

// containsStr 检查 s 是否包含 substr。
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
