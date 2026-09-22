package roles

import (
	"testing"

	"github.com/xiws/orca/internal/agent/core"
)

func TestFormatSpecification_Nil(t *testing.T) {
	result := FormatSpecification(nil)
	if result != "" {
		t.Errorf("expected empty string for nil spec, got %s", result)
	}
}

func TestFormatSpecification_Full(t *testing.T) {
	spec := &core.Specification{
		Goal: "优化性能",
		Requirements: []core.Requirement{
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
	task := &core.Task{
		Input:      "实现功能",
		TaskResult: "已完成所有修改",
		Specification: &core.Specification{
			AcceptanceCriteria: []string{"测试通过", "编译成功"},
		},
	}

	input := verifier.buildVerifyInput(task)
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
		t.Error("expected task result in verify input")
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
	task := &core.Task{
		Input: "实现功能",
		Specification: &core.Specification{
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
