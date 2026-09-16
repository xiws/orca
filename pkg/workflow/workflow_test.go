package workflow

import (
	"errors"
	"testing"
)

// --- 测试辅助类型 ---

// testGuard 实现 Guard 接口。
type testGuard struct {
	allow bool
}

func (g testGuard) Check(ctx Context) error {
	if !g.allow {
		return errors.New("access denied")
	}
	return nil
}

// testCondition 实现 Condition 接口。
type testCondition struct {
	value bool
}

func (c testCondition) Evaluate(ctx Context) bool {
	return c.value
}

// testHook 实现 Hook 接口，记录调用顺序。
type testHook struct {
	calls *[]string
	name  string
}

func (h testHook) Before(ctx Context) error {
	*h.calls = append(*h.calls, h.name+":before")
	return nil
}

func (h testHook) After(ctx Context) error {
	*h.calls = append(*h.calls, h.name+":after")
	return nil
}

// failHook Before 阶段返回错误。
type failHook struct{}

func (failHook) Before(ctx Context) error {
	return errors.New("before hook failed")
}

func (failHook) After(ctx Context) error {
	return nil
}

// --- Workflow 测试 ---

func TestNewWorkflow(t *testing.T) {
	wf := NewWorkflow("test")
	if wf == nil {
		t.Fatal("NewWorkflow returned nil")
	}
	if len(wf.States()) != 0 {
		t.Fatalf("expected 0 states, got %d", len(wf.States()))
	}
}

func TestWorkflowRegisterState(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("REJECTED")

	if len(wf.States()) != 2 {
		t.Fatalf("expected 2 states, got %d", len(wf.States()))
	}
	if wf.States()["APPLY"].Name != "APPLY" {
		t.Fatalf("expected state name APPLY, got %s", wf.States()["APPLY"].Name)
	}
}

func TestWorkflowStateTerminal(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED").Terminal()

	if wf.States()["APPLY"].Terminal {
		t.Fatal("APPLY should not be terminal")
	}
	if !wf.States()["FINISHED"].Terminal {
		t.Fatal("FINISHED should be terminal")
	}
}

func TestWorkflowDuplicateState(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	// 重复注册同一状态应返回同一个 builder，不报错
	builder := wf.State("APPLY")
	if builder.state.Name != "APPLY" {
		t.Fatal("duplicate state should return same state")
	}
	// 不应增加新状态
	if len(wf.States()) != 1 {
		t.Fatalf("expected 1 state, got %d", len(wf.States()))
	}
}

func TestTransitionBuilder(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("REJECTED")
	wf.From("APPLY").To("REJECTED").Action("reject")

	transitions := wf.Transitions()
	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(transitions))
	}
	tr := transitions[0]
	if tr.From != "APPLY" || tr.To != "REJECTED" || tr.Action != "reject" {
		t.Fatalf("unexpected transition: %+v", tr)
	}
}

func TestTransitionBuilderWithGuardAndHook(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED")

	var calls []string
	wf.From("APPLY").To("FINISHED").
		Action("special").
		Guard(testGuard{allow: true}).
		Condition(testCondition{value: true}).
		Hook(testHook{calls: &calls, name: "h1"})

	tr := wf.Transitions()[0]
	if len(tr.Guards) != 1 {
		t.Fatalf("expected 1 guard, got %d", len(tr.Guards))
	}
	if len(tr.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(tr.Conditions))
	}
	if len(tr.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(tr.Hooks))
	}
}

// --- Engine 测试 ---

func newApprovalWorkflow() *Workflow {
	wf := NewWorkflow("approval")
	wf.State("APPLY")
	wf.State("REJECTED")
	wf.State("PASSED")
	wf.State("FINISHED").Terminal()

	wf.From("APPLY").To("PASSED").Action("approve")
	wf.From("APPLY").To("REJECTED").Action("reject")
	wf.From("REJECTED").To("APPLY").Action("resubmit")
	wf.From("PASSED").To("FINISHED").Action("finish")

	return wf
}

func TestExecuteSuccess(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	result, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "approve",
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.From != "APPLY" {
		t.Fatalf("From = %s, want APPLY", result.From)
	}
	if result.To != "PASSED" {
		t.Fatalf("To = %s, want PASSED", result.To)
	}
	if result.Action != "approve" {
		t.Fatalf("Action = %s, want approve", result.Action)
	}
}

func TestExecuteInvalidAction(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "unknown",
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestExecuteInvalidState(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "NOT_EXIST",
		Action:       "approve",
	})
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}
}

func TestExecuteGuardPass(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED")
	wf.From("APPLY").To("FINISHED").
		Action("special_pass").
		Guard(testGuard{allow: true})

	engine := NewEngine(wf)
	result, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "special_pass",
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.To != "FINISHED" {
		t.Fatalf("To = %s, want FINISHED", result.To)
	}
}

func TestExecuteGuardReject(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED")
	wf.From("APPLY").To("FINISHED").
		Action("special_pass").
		Guard(testGuard{allow: false})

	engine := NewEngine(wf)
	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "special_pass",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestExecuteConditionMet(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED")
	wf.From("APPLY").To("FINISHED").
		Action("auto_finish").
		Condition(testCondition{value: true})

	engine := NewEngine(wf)
	result, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "auto_finish",
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.To != "FINISHED" {
		t.Fatalf("To = %s, want FINISHED", result.To)
	}
}

func TestExecuteConditionNotMet(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED")
	wf.From("APPLY").To("FINISHED").
		Action("auto_finish").
		Condition(testCondition{value: false})

	engine := NewEngine(wf)
	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "auto_finish",
	})
	if !errors.Is(err, ErrConditionNotMet) {
		t.Fatalf("expected ErrConditionNotMet, got %v", err)
	}
}

func TestExecuteHookOrder(t *testing.T) {
	var calls []string
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("PASSED")
	wf.From("APPLY").To("PASSED").
		Action("approve").
		Hook(testHook{calls: &calls, name: "h1"}).
		Hook(testHook{calls: &calls, name: "h2"})

	engine := NewEngine(wf)
	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "approve",
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	expected := []string{"h1:before", "h2:before", "h1:after", "h2:after"}
	if len(calls) != len(expected) {
		t.Fatalf("expected %d calls, got %d: %v", len(expected), len(calls), calls)
	}
	for i, v := range expected {
		if calls[i] != v {
			t.Fatalf("calls[%d] = %s, want %s", i, calls[i], v)
		}
	}
}

func TestExecuteHookBeforeFail(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("PASSED")
	wf.From("APPLY").To("PASSED").
		Action("approve").
		Hook(failHook{})

	engine := NewEngine(wf)
	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "approve",
	})
	if err == nil {
		t.Fatal("expected error from before hook")
	}
	// 流转不应发生
}

func TestExecuteMultipleTransitions(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	// APPLY + approve -> PASSED
	result, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "approve",
	})
	if err != nil {
		t.Fatalf("approve error = %v", err)
	}
	if result.To != "PASSED" {
		t.Fatalf("approve To = %s, want PASSED", result.To)
	}

	// APPLY + reject -> REJECTED
	result, err = engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "reject",
	})
	if err != nil {
		t.Fatalf("reject error = %v", err)
	}
	if result.To != "REJECTED" {
		t.Fatalf("reject To = %s, want REJECTED", result.To)
	}
}

func TestExecuteWithInstanceID(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	result, err := engine.Execute(ExecuteRequest{
		InstanceID:   "10001",
		CurrentState: "APPLY",
		Action:       "approve",
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.TransitionID == "" {
		t.Fatal("TransitionID should not be empty")
	}
	expected := "10001:APPLY->PASSED"
	if result.TransitionID != expected {
		t.Fatalf("TransitionID = %s, want %s", result.TransitionID, expected)
	}
}

func TestExecuteNilContext(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	// Context 为 nil 时不应 panic
	result, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "approve",
		Context:      nil,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.To != "PASSED" {
		t.Fatalf("To = %s, want PASSED", result.To)
	}
}

// --- 完整审批流程端到端测试 ---

func TestFullApprovalWorkflow(t *testing.T) {
	wf := newApprovalWorkflow()
	engine := NewEngine(wf)

	// 步骤 1: APPLY -> reject -> REJECTED
	result, err := engine.Execute(ExecuteRequest{
		InstanceID:   "10001",
		CurrentState: "APPLY",
		Action:       "reject",
	})
	if err != nil {
		t.Fatalf("step 1 error = %v", err)
	}
	if result.To != "REJECTED" {
		t.Fatalf("step 1 To = %s, want REJECTED", result.To)
	}

	// 步骤 2: REJECTED -> resubmit -> APPLY
	result, err = engine.Execute(ExecuteRequest{
		InstanceID:   "10001",
		CurrentState: "REJECTED",
		Action:       "resubmit",
	})
	if err != nil {
		t.Fatalf("step 2 error = %v", err)
	}
	if result.To != "APPLY" {
		t.Fatalf("step 2 To = %s, want APPLY", result.To)
	}

	// 步骤 3: APPLY -> approve -> PASSED
	result, err = engine.Execute(ExecuteRequest{
		InstanceID:   "10001",
		CurrentState: "APPLY",
		Action:       "approve",
	})
	if err != nil {
		t.Fatalf("step 3 error = %v", err)
	}
	if result.To != "PASSED" {
		t.Fatalf("step 3 To = %s, want PASSED", result.To)
	}

	// 步骤 4: PASSED -> finish -> FINISHED (终态)
	result, err = engine.Execute(ExecuteRequest{
		InstanceID:   "10001",
		CurrentState: "PASSED",
		Action:       "finish",
	})
	if err != nil {
		t.Fatalf("step 4 error = %v", err)
	}
	if result.To != "FINISHED" {
		t.Fatalf("step 4 To = %s, want FINISHED", result.To)
	}

	// 验证终态
	if !wf.States()["FINISHED"].Terminal {
		t.Fatal("FINISHED should be terminal")
	}
}

// --- Guard 使用 Context 测试 ---

// adminGuard 只允许 admin 角色通过。
type adminGuard struct{}

func (adminGuard) Check(ctx Context) error {
	role, _ := ctx["Role"].(string)
	if role != "admin" {
		return errors.New("admin only")
	}
	return nil
}

func TestGuardWithContext(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("APPLY")
	wf.State("FINISHED")
	wf.From("APPLY").To("FINISHED").
		Action("special_pass").
		Guard(adminGuard{})

	engine := NewEngine(wf)

	// 非 admin 应该被拒绝
	_, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "special_pass",
		Context:      Context{"Role": "user"},
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-admin, got %v", err)
	}

	// admin 应该通过
	result, err := engine.Execute(ExecuteRequest{
		CurrentState: "APPLY",
		Action:       "special_pass",
		Context:      Context{"Role": "admin"},
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.To != "FINISHED" {
		t.Fatalf("To = %s, want FINISHED", result.To)
	}
}

func TestWorkflow_full(t *testing.T) {
	wf := NewWorkflow("test")
	var submit = wf.State("submit")
	_ = wf.State("approval")
	_ = wf.State("verify")
	_ = wf.State("finish")

	submit.workflow.From("submit").To("approval").Action("approve")
}

// --- Handler 测试辅助 ---

// constHandler 始终返回固定 action。
type constHandler struct {
	action string
}

func (h constHandler) Handle(ctx Context) (string, error) {
	return h.action, nil
}

// errHandler 始终返回错误。
type errHandler struct{}

func (errHandler) Handle(ctx Context) (string, error) {
	return "", errors.New("handler error")
}

// --- Engine.Run 测试 ---

func TestEngineRun_SimpleFlow(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("execute").Handler(constHandler{action: "complete"})
	wf.State("verify").Handler(constHandler{action: "complete"})
	wf.State("done").Terminal()

	wf.From("execute").To("verify").Action("complete")
	wf.From("verify").To("done").Action("complete")

	engine := NewEngine(wf)
	result, err := engine.Run(RunRequest{
		InitialState: "execute",
	})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.FinalState != "done" {
		t.Fatalf("FinalState = %s, want done", result.FinalState)
	}
	if len(result.History) != 2 {
		t.Fatalf("History length = %d, want 2", len(result.History))
	}
	// 验证流转顺序
	if result.History[0].From != "execute" || result.History[0].To != "verify" {
		t.Errorf("History[0] = %s -> %s, want execute -> verify",
			result.History[0].From, result.History[0].To)
	}
	if result.History[1].From != "verify" || result.History[1].To != "done" {
		t.Errorf("History[1] = %s -> %s, want verify -> done",
			result.History[1].From, result.History[1].To)
	}
}

func TestEngineRun_TerminalInitialState(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("done").Terminal()

	engine := NewEngine(wf)
	result, err := engine.Run(RunRequest{
		InitialState: "done",
	})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.FinalState != "done" {
		t.Fatalf("FinalState = %s, want done", result.FinalState)
	}
	if len(result.History) != 0 {
		t.Fatalf("History should be empty, got %d entries", len(result.History))
	}
}

func TestEngineRun_HandlerError(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("step1").Handler(errHandler{})
	wf.State("step2").Terminal()
	wf.From("step1").To("step2").Action("ok")

	engine := NewEngine(wf)
	result, err := engine.Run(RunRequest{
		InitialState: "step1",
	})
	if err == nil {
		t.Fatal("expected error from handler")
	}
	if result.FinalState != "step1" {
		t.Fatalf("FinalState = %s, want step1", result.FinalState)
	}
}

func TestEngineRun_NoHandler(t *testing.T) {
	wf := NewWorkflow("test")
	// 非终态且无 Handler → 配置错误
	wf.State("orphan")

	engine := NewEngine(wf)
	_, err := engine.Run(RunRequest{
		InitialState: "orphan",
	})
	if err == nil {
		t.Fatal("expected error for state without handler")
	}
}

func TestEngineRun_WithGuards(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("apply").Handler(constHandler{action: "approve"})
	wf.State("finished").Terminal()

	wf.From("apply").To("finished").
		Action("approve").
		Guard(testGuard{allow: false})

	engine := NewEngine(wf)
	_, err := engine.Run(RunRequest{
		InitialState: "apply",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestEngineRun_RetryLoop(t *testing.T) {
	// 模拟 verify 失败回退 execute 的场景，第二次 verify 通过
	callCount := 0
	verifyHandler := funcHandler(func(ctx Context) (string, error) {
		callCount++
		if callCount >= 2 {
			return "complete", nil
		}
		return "fail", nil
	})

	wf := NewWorkflow("test")
	wf.State("execute").Handler(constHandler{action: "complete"})
	wf.State("verify").Handler(verifyHandler)
	wf.State("done").Terminal()

	wf.From("execute").To("verify").Action("complete")
	wf.From("verify").To("done").Action("complete")
	wf.From("verify").To("execute").Action("fail")

	engine := NewEngine(wf)
	result, err := engine.Run(RunRequest{
		InitialState: "execute",
	})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.FinalState != "done" {
		t.Fatalf("FinalState = %s, want done", result.FinalState)
	}
	// 流转历史：execute→verify (fail) → execute→verify (complete) → done = 4 次流转
	if len(result.History) != 4 {
		t.Fatalf("History length = %d, want 4", len(result.History))
	}
}

func TestEngineRun_HistoryRecorded(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("a").Handler(constHandler{action: "go"})
	wf.State("b").Handler(constHandler{action: "go"})
	wf.State("c").Handler(constHandler{action: "go"})
	wf.State("end").Terminal()

	wf.From("a").To("b").Action("go")
	wf.From("b").To("c").Action("go")
	wf.From("c").To("end").Action("go")

	engine := NewEngine(wf)
	result, err := engine.Run(RunRequest{
		InitialState: "a",
		InstanceID:   "inst-1",
	})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(result.History) != 3 {
		t.Fatalf("History length = %d, want 3", len(result.History))
	}
	// 验证 TransitionID 已生成
	for i, h := range result.History {
		if h.TransitionID == "" {
			t.Errorf("History[%d].TransitionID is empty", i)
		}
	}
}

func TestEngineRun_StateNotFound(t *testing.T) {
	wf := NewWorkflow("test")
	wf.State("a").Terminal()

	engine := NewEngine(wf)
	_, err := engine.Run(RunRequest{
		InitialState: "nonexistent",
	})
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}
}

func TestEngineRun_WithContext(t *testing.T) {
	// Handler 将数据写入 Context，验证下游可读取
	wf := NewWorkflow("test")
	wf.State("producer").Handler(funcHandler(func(ctx Context) (string, error) {
		ctx["value"] = 42
		return "done", nil
	}))
	wf.State("consumer").Handler(funcHandler(func(ctx Context) (string, error) {
		v, ok := ctx["value"].(int)
		if !ok || v != 42 {
			return "", errors.New("context value missing")
		}
		return "finish", nil
	}))
	wf.State("end").Terminal()

	wf.From("producer").To("consumer").Action("done")
	wf.From("consumer").To("end").Action("finish")

	engine := NewEngine(wf)
	result, err := engine.Run(RunRequest{
		InitialState: "producer",
		Context:      make(Context),
	})
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.FinalState != "end" {
		t.Fatalf("FinalState = %s, want end", result.FinalState)
	}
}

// funcHandler 将函数适配为 Handler 接口。
type funcHandler func(ctx Context) (string, error)

func (f funcHandler) Handle(ctx Context) (string, error) {
	return f(ctx)
}
