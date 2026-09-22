package roles

import (
	"testing"

	"github.com/xiws/orca/internal/domain"
)

func TestClarifyStatus_Values(t *testing.T) {
	if ClarifyReady != "ready" {
		t.Errorf("expected ClarifyReady = 'ready', got %s", ClarifyReady)
	}
	if ClarifyQuestion != "question" {
		t.Errorf("expected ClarifyQuestion = 'question', got %s", ClarifyQuestion)
	}
	if ClarifyAssumption != "assumption" {
		t.Errorf("expected ClarifyAssumption = 'assumption', got %s", ClarifyAssumption)
	}
}

func TestClarifyResult_Ready(t *testing.T) {
	result := &ClarifyResult{
		Status: ClarifyReady,
		Specification: &domain.Specification{
			Goal: "实现用户登录功能",
			Requirements: []domain.Requirement{
				{ID: "R1", Description: "支持邮箱登录", Priority: "high"},
			},
			AcceptanceCriteria: []string{"用户可以成功登录"},
		},
	}

	if result.Status != ClarifyReady {
		t.Errorf("expected status ready, got %s", result.Status)
	}
	if result.Specification == nil {
		t.Fatal("expected non-nil specification")
	}
	if result.Specification.Goal != "实现用户登录功能" {
		t.Errorf("expected goal '实现用户登录功能', got %s", result.Specification.Goal)
	}
	if len(result.Specification.Requirements) != 1 {
		t.Errorf("expected 1 requirement, got %d", len(result.Specification.Requirements))
	}
}

func TestClarifyResult_Question(t *testing.T) {
	result := &ClarifyResult{
		Status: ClarifyQuestion,
		Questions: []string{
			"已支付订单是否允许删除？",
			"删除采用软删除还是物理删除？",
		},
	}

	if result.Status != ClarifyQuestion {
		t.Errorf("expected status question, got %s", result.Status)
	}
	if len(result.Questions) != 2 {
		t.Errorf("expected 2 questions, got %d", len(result.Questions))
	}
}

func TestClarifyResult_Assumption(t *testing.T) {
	result := &ClarifyResult{
		Status: ClarifyAssumption,
		Specification: &domain.Specification{
			Goal: "增加用户删除功能",
		},
		Assumptions: []string{
			"未明确说明，默认采用软删除",
			"未明确说明，默认只有管理员可以删除",
		},
	}

	if result.Status != ClarifyAssumption {
		t.Errorf("expected status assumption, got %s", result.Status)
	}
	if len(result.Assumptions) != 2 {
		t.Errorf("expected 2 assumptions, got %d", len(result.Assumptions))
	}
}

func TestParseClarifyResult_Simple(t *testing.T) {
	// 当前简化实现：任何输入都视为 ready 状态
	result, err := parseClarifyResult("实现某个功能")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ClarifyReady {
		t.Errorf("expected status ready, got %s", result.Status)
	}
	if result.Specification == nil {
		t.Fatal("expected non-nil specification")
	}
	if result.Specification.Goal != "实现某个功能" {
		t.Errorf("expected goal '实现某个功能', got %s", result.Specification.Goal)
	}
}

func TestSpecification_Structure(t *testing.T) {
	spec := &domain.Specification{
		Goal: "优化系统性能",
		Requirements: []domain.Requirement{
			{ID: "R1", Description: "增加缓存层", Priority: "high"},
			{ID: "R2", Description: "优化数据库查询", Priority: "medium"},
		},
		Constraints: []string{"保持向后兼容", "不改变 API 签名"},
		AcceptanceCriteria: []string{
			"响应时间 < 100ms",
			"测试覆盖率 > 80%",
		},
		Assumptions: []string{"使用 Redis 作为缓存后端"},
	}

	if spec.Goal != "优化系统性能" {
		t.Errorf("unexpected goal: %s", spec.Goal)
	}
	if len(spec.Requirements) != 2 {
		t.Errorf("expected 2 requirements, got %d", len(spec.Requirements))
	}
	if len(spec.Constraints) != 2 {
		t.Errorf("expected 2 constraints, got %d", len(spec.Constraints))
	}
	if len(spec.AcceptanceCriteria) != 2 {
		t.Errorf("expected 2 acceptance criteria, got %d", len(spec.AcceptanceCriteria))
	}
	if len(spec.Assumptions) != 1 {
		t.Errorf("expected 1 assumption, got %d", len(spec.Assumptions))
	}
}

func TestRequirement_Priority(t *testing.T) {
	tests := []struct {
		priority string
		valid    bool
	}{
		{"high", true},
		{"medium", true},
		{"low", true},
		{"critical", false}, // 不在约定范围内
	}

	for _, tt := range tests {
		req := domain.Requirement{ID: "R1", Description: "test", Priority: tt.priority}
		// Priority 只是字符串，不做验证，但约定为 high/medium/low
		if req.Priority == "" {
			t.Error("priority should not be empty")
		}
	}
}
