package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

func TestWorkflowRunner_FindInitialNode(t *testing.T) {
	wr := &WorkflowRunner{}

	tests := []struct {
		name     string
		plan     *roles.WorkflowPlan
		expected string
	}{
		{
			name: "find execute node",
			plan: &roles.WorkflowPlan{
				Nodes: []roles.PlanNode{
					{ID: "verify", Type: "verify"},
					{ID: "execute", Type: "execute"},
				},
			},
			expected: "execute",
		},
		{
			name: "return first node if no execute",
			plan: &roles.WorkflowPlan{
				Nodes: []roles.PlanNode{
					{ID: "step1", Type: "verify"},
					{ID: "step2", Type: "verify"},
				},
			},
			expected: "step1",
		},
		{
			name:     "empty plan",
			plan:     &roles.WorkflowPlan{Nodes: []roles.PlanNode{}},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wr.findInitialNode(tt.plan)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestWorkflowRunner_FindNode(t *testing.T) {
	wr := &WorkflowRunner{}
	plan := &roles.WorkflowPlan{
		Nodes: []roles.PlanNode{
			{ID: "node1", Type: "execute"},
			{ID: "node2", Type: "verify"},
		},
	}

	node := wr.findNode(plan, "node1")
	if node == nil {
		t.Fatal("expected to find node1")
	}
	if node.ID != "node1" {
		t.Errorf("expected node1, got %s", node.ID)
	}

	node = wr.findNode(plan, "nonexistent")
	if node != nil {
		t.Error("expected nil for nonexistent node")
	}
}

func TestWorkflowRunner_IsTerminal(t *testing.T) {
	wr := &WorkflowRunner{}
	plan := &roles.WorkflowPlan{
		Nodes: []roles.PlanNode{
			{ID: "execute", Type: "execute"},
			{ID: "verify", Type: "verify"},
			{ID: "done", Type: "execute"},
		},
		Edges: []roles.PlanEdge{
			{From: "execute", To: "verify", Action: "complete"},
			{From: "verify", To: "execute", Action: "fail"},
			// "done" has no outgoing edges -> terminal
		},
	}

	if wr.isTerminal(plan, "execute") {
		t.Error("execute should not be terminal")
	}
	if wr.isTerminal(plan, "verify") {
		t.Error("verify should not be terminal")
	}
	if !wr.isTerminal(plan, "done") {
		t.Error("done should be terminal")
	}
}

func TestWorkflowRunner_BuildWorkflow(t *testing.T) {
	wr := &WorkflowRunner{}
	plan := roles.DefaultWorkflowPlan()
	task := core.NewTask("test", "test")

	wf := wr.buildWorkflow(plan, task)
	if wf == nil {
		t.Fatal("expected non-nil workflow")
	}

	states := wf.States()
	if len(states) != 3 {
		t.Errorf("expected 3 states, got %d", len(states))
	}

	transitions := wf.Transitions()
	if len(transitions) != 3 {
		t.Errorf("expected 3 transitions, got %d", len(transitions))
	}

	// 非终态节点应挂载 Handler
	for _, s := range states {
		if s.Name == "done" {
			// done 是终态，不需要 Handler
			if !s.Terminal {
				t.Error("done should be terminal")
			}
			continue
		}
		if s.Handler == nil {
			t.Errorf("state %q should have a handler", s.Name)
		}
	}
}

func TestWorkflowRunner_ResolveAction(t *testing.T) {
	wr := &WorkflowRunner{}

	tests := []struct {
		name     string
		nodeType string
		result   string
		expected string
	}{
		// execute 节点：始终返回 complete
		{"execute returns complete", "execute", "any result", "complete"},
		// verify 节点：PASS → complete，FAIL → fail
		{"verify PASS returns complete", "verify", "PASS", "complete"},
		{"verify FAIL returns fail", "verify", "FAIL: something broke", "fail"},
		// repair 节点：始终返回 complete
		{"repair returns complete", "repair", "fix plan", "complete"},
		// human 节点：APPROVED → complete，REJECTED → fail
		{"human APPROVED returns complete", "human", "APPROVED", "complete"},
		{"human REJECTED returns fail", "human", "REJECTED", "fail"},
		// 未知类型：默认 complete
		{"unknown returns complete", "unknown", "anything", "complete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &roles.PlanNode{ID: "test", Type: tt.nodeType}
			action := wr.resolveAction(node, tt.result)
			if action != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, action)
			}
		})
	}
}

func TestNewWorkflowRunner(t *testing.T) {
	// 测试创建 WorkflowRunner（不需要实际 Runtime）
	wr := NewWorkflowRunner(nil)
	if wr == nil {
		t.Fatal("expected non-nil WorkflowRunner")
	}
}

// approvalHandler 订阅 HumanApprovalEvent 并自动批准。
type approvalHandler struct{}

func (h approvalHandler) Handle(e event.Event) {
	if he, ok := e.(HumanApprovalEvent); ok {
		he.Node.Approve()
	}
}

// rejectionHandler 订阅 HumanApprovalEvent 并自动拒绝。
type rejectionHandler struct{}

func (h rejectionHandler) Handle(e event.Event) {
	if he, ok := e.(HumanApprovalEvent); ok {
		he.Node.Reject()
	}
}

func TestWorkflowRunner_ExecuteNode_Human(t *testing.T) {
	rt := core.NewRuntime()
	defer rt.Close()

	err := rt.Subscribe(HumanApprovalEvent{}, approvalHandler{})
	if err != nil {
		t.Fatal(err)
	}

	wr := &WorkflowRunner{runtime: rt}
	node := &roles.PlanNode{ID: "approval", Type: "human", Description: "Approve deployment"}
	task := core.NewTask("test", "test")

	done := make(chan struct{})
	var result string
	var execErr error

	go func() {
		result, execErr = wr.ExecuteNode(node, task)
		close(done)
	}()

	select {
	case <-done:
		if execErr != nil {
			t.Fatalf("unexpected error: %v", execErr)
		}
		if result != "APPROVED" {
			t.Errorf("expected APPROVED, got %s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for human approval")
	}
}

func TestWorkflowRunner_ExecuteNode_Human_Rejected(t *testing.T) {
	rt := core.NewRuntime()
	defer rt.Close()

	err := rt.Subscribe(HumanApprovalEvent{}, rejectionHandler{})
	if err != nil {
		t.Fatal(err)
	}

	wr := &WorkflowRunner{runtime: rt}
	node := &roles.PlanNode{ID: "approval", Type: "human", Description: "Approve deployment"}
	task := core.NewTask("test", "test")

	done := make(chan struct{})
	var result string
	var execErr error

	go func() {
		result, execErr = wr.ExecuteNode(node, task)
		close(done)
	}()

	select {
	case <-done:
		if execErr != nil {
			t.Fatalf("unexpected error: %v", execErr)
		}
		if result != "REJECTED" {
			t.Errorf("expected REJECTED, got %s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for human rejection")
	}
}

func TestWorkflowRunner_EngineField(t *testing.T) {
	wr := &WorkflowRunner{}
	if wr.engine != nil {
		t.Error("expected nil engine before Run")
	}

	// Run 会在 ExecuteNode 时失败（无 LLM provider），但 engine 应已被赋值
	rt := core.NewRuntime()
	defer rt.Close()
	wr.runtime = rt

	plan := roles.DefaultWorkflowPlan()
	task := core.NewTask("test", "test")
	// 空的 Provider 使模型请求失败，但 engine 在 ExecuteNode 之前已赋值
	task.SessionInfo.Provider = llm.ModelInfo{}

	_ = wr.Run(plan, task)

	if wr.engine == nil {
		t.Error("expected engine to be set after Run")
	}
}

// TestWorkflowRunner_LiveExecuteVerify 真实拉 provider 跑完整
// execute → verify 链路。任务：在临时目录创建一个文件并写入指定内容，
// Verifier 检查文件是否存在且内容正确。
//
// 需要 .orca/setting.json 中的 provider 可达，-short 模式跳过。
func TestWorkflowRunner_LiveExecuteVerify(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live workflow test: requires reachable model endpoint")
	}

	// 临时目录作为工作区，避免污染项目树
	tmpDir := t.TempDir()

	rt := core.NewRuntime()
	defer rt.Close()

	wr := NewWorkflowRunner(rt)

	// 构建任务：创建文件并写入内容
	goal := "在目录 " + tmpDir + " 下创建一个名为 hello.txt 的文件，内容为 hello from workflow"
	task := core.NewTask(goal, "live-workflow-test")
	task.Specification = &core.Specification{
		Goal: goal,
		Requirements: []core.Requirement{
			{ID: "R1", Description: "创建文件 " + filepath.Join(tmpDir, "hello.txt"), Priority: "high"},
			{ID: "R2", Description: "文件内容为 hello from workflow", Priority: "high"},
		},
		AcceptanceCriteria: []string{
			"文件 " + filepath.Join(tmpDir, "hello.txt") + " 存在",
			"文件内容为 hello from workflow",
		},
	}

	// 设置 session：系统提示 + 工具提示 + 用户输入
	data := core.PromptContext{
		ProjectPath:   tmpDir,
		ContextLength: 8192,
	}
	task.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(data))
	task.SessionInfo.AppendToolPrompt()
	task.SessionInfo.AppendMessage(llm.RoleUser, goal)

	// 使用默认 workflow 计划：execute → verify
	plan := roles.DefaultWorkflowPlan()

	err := wr.Run(plan, task)
	if err != nil {
		t.Fatalf("workflow Run() error = %v", err)
	}

	// 验证文件确实被创建（echo 命令会带末尾换行符）
	content, err := os.ReadFile(filepath.Join(tmpDir, "hello.txt"))
	if err != nil {
		t.Fatalf("expected hello.txt to exist: %v", err)
	}
	if strings.TrimSpace(string(content)) != "hello from workflow" {
		t.Errorf("hello.txt content = %q, want %q", string(content), "hello from workflow")
	}

	t.Logf("workflow completed. task result:\n%s", task.TaskResult)
}
