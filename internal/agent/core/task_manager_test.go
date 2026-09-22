package core

import (
	"testing"
)

func TestNewTask_HasInitialStatus(t *testing.T) {
	task := NewTask("fix bug", "Bug Fix")
	if task.Status != TaskCreated {
		t.Errorf("expected status CREATED, got %s", task.Status)
	}
	if task.Input != "fix bug" {
		t.Errorf("expected input 'fix bug', got %s", task.Input)
	}
	if task.Title != "Bug Fix" {
		t.Errorf("expected title 'Bug Fix', got %s", task.Title)
	}
	if task.MaxRetries != 3 {
		t.Errorf("expected max retries 3, got %d", task.MaxRetries)
	}
}

func TestTask_RecordTransition(t *testing.T) {
	task := NewTask("test", "")
	task.RecordTransition(TaskCreated, TaskRunning, "starting execution")

	if len(task.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(task.History))
	}

	event := task.History[0]
	if event.From != TaskCreated || event.To != TaskRunning {
		t.Errorf("expected CREATED->RUNNING, got %s->%s", event.From, event.To)
	}
	if event.Reason != "starting execution" {
		t.Errorf("expected reason 'starting execution', got %s", event.Reason)
	}
	if event.Timestamp == 0 {
		t.Error("expected non-zero timestamp")
	}
}

func TestTaskManager_CreateTask(t *testing.T) {
	tm := NewTaskManager(nil)
	task := tm.CreateTask("implement feature", "Feature")

	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.Input != "implement feature" {
		t.Errorf("expected input 'implement feature', got %s", task.Input)
	}
	if task.Status != TaskCreated {
		t.Errorf("expected status CREATED, got %s", task.Status)
	}

	// 可以通过 ID 获取
	got, err := tm.GetTask(task.Id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != task {
		t.Error("expected same task instance")
	}
}

func TestTaskManager_GetTask_NotFound(t *testing.T) {
	tm := NewTaskManager(nil)
	_, err := tm.GetTask(999)
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

func TestTaskManager_TransitionStatus_Valid(t *testing.T) {
	tm := NewTaskManager(nil)
	task := tm.CreateTask("test", "")

	tests := []struct {
		from TaskStatus
		to   TaskStatus
	}{
		{TaskCreated, TaskClarifying},
		{TaskClarifying, TaskPlanning},
		{TaskPlanning, TaskRunning},
		{TaskRunning, TaskVerifying},
		{TaskVerifying, TaskCompleted},
	}

	for _, tt := range tests {
		// 确保任务处于正确的起始状态
		task.Status = tt.from
		err := tm.TransitionStatus(task.Id, tt.to, "test")
		if err != nil {
			t.Errorf("expected valid transition %s->%s, got error: %v", tt.from, tt.to, err)
		}
	}
}

func TestTaskManager_TransitionStatus_Invalid(t *testing.T) {
	tm := NewTaskManager(nil)
	task := tm.CreateTask("test", "")

	tests := []struct {
		from TaskStatus
		to   TaskStatus
	}{
		{TaskCreated, TaskCompleted},  // 不能直接完成
		{TaskCreated, TaskVerifying},  // 不能直接验证
		{TaskCompleted, TaskRunning},  // 终态不能转换
		{TaskFailed, TaskRunning},     // 终态不能转换
		{TaskPlanning, TaskVerifying}, // 不能跳过 RUNNING
	}

	for _, tt := range tests {
		task.Status = tt.from
		err := tm.TransitionStatus(task.Id, tt.to, "test")
		if err == nil {
			t.Errorf("expected invalid transition %s->%s to fail", tt.from, tt.to)
		}
	}
}

func TestTaskManager_TransitionStatus_NotFound(t *testing.T) {
	tm := NewTaskManager(nil)
	err := tm.TransitionStatus(999, TaskRunning, "test")
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

func TestTaskManager_TransitionStatus_RecordsHistory(t *testing.T) {
	tm := NewTaskManager(nil)
	task := tm.CreateTask("test", "")

	_ = tm.TransitionStatus(task.Id, TaskRunning, "start")
	_ = tm.TransitionStatus(task.Id, TaskVerifying, "verify")
	_ = tm.TransitionStatus(task.Id, TaskCompleted, "done")

	if len(task.History) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(task.History))
	}

	if task.Status != TaskCompleted {
		t.Errorf("expected final status COMPLETED, got %s", task.Status)
	}
}

func TestTaskManager_ListTasks(t *testing.T) {
	tm := NewTaskManager(nil)
	tm.CreateTask("task1", "")
	tm.CreateTask("task2", "")
	tm.CreateTask("task3", "")

	tasks := tm.ListTasks()
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
}

func TestTaskManager_VerifyToRunning_ForRepair(t *testing.T) {
	tm := NewTaskManager(nil)
	task := tm.CreateTask("test", "")

	// 模拟正常流程到 VERIFYING
	task.Status = TaskRunning
	_ = tm.TransitionStatus(task.Id, TaskVerifying, "verify")

	// 验证失败，回到 RUNNING 触发 Repair
	err := tm.TransitionStatus(task.Id, TaskRunning, "verify failed, triggering repair")
	if err != nil {
		t.Errorf("expected VERIFYING->RUNNING to be valid for repair, got: %v", err)
	}

	if task.RetryCount != 0 {
		t.Errorf("expected retry count 0 (not auto-incremented), got %d", task.RetryCount)
	}
}
