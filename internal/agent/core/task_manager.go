package core

import (
	"fmt"
	"sync"

	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

// validTransitions 定义合法的状态转换表。
var validTransitions = map[TaskStatus][]TaskStatus{
	TaskCreated:    {TaskClarifying, TaskPlanning, TaskRunning, TaskFailed},
	TaskClarifying: {TaskPlanning, TaskRunning, TaskFailed},
	TaskPlanning:   {TaskRunning, TaskFailed},
	TaskRunning:    {TaskVerifying, TaskCompleted, TaskFailed},
	TaskVerifying:  {TaskCompleted, TaskRunning, TaskFailed}, // FAIL -> RUNNING 触发 Repair
	TaskCompleted:  {},
	TaskFailed:     {},
}

// TaskManager 管理任务的创建、状态转换和生命周期。
type TaskManager struct {
	mu    sync.RWMutex
	tasks map[int64]*Task
	bus   event.EventPublisher
}

// NewTaskManager 创建任务管理器。bus 可为 nil，此时不发布状态变更事件。
func NewTaskManager(bus event.EventPublisher) *TaskManager {
	return &TaskManager{
		tasks: make(map[int64]*Task),
		bus:   bus,
	}
}

// CreateTask 创建一个新任务并注册到管理器。
func (tm *TaskManager) CreateTask(input, title string) *Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task := NewTask(input, title)
	tm.tasks[task.Id] = task
	return task
}

// GetTask 按 ID 获取任务。
func (tm *TaskManager) GetTask(id int64) (*Task, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	task, ok := tm.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %d not found", id)
	}
	return task, nil
}

// TransitionStatus 将任务从当前状态转换到新状态。
// 如果转换不合法（不在 validTransitions 中），返回错误。
func (tm *TaskManager) TransitionStatus(id int64, newStatus TaskStatus, reason string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, ok := tm.tasks[id]
	if !ok {
		return fmt.Errorf("task %d not found", id)
	}

	if !tm.isValidTransition(task.Status, newStatus) {
		return fmt.Errorf("invalid transition: %s -> %s for task %d", task.Status, newStatus, id)
	}

	oldStatus := task.Status
	task.Status = newStatus
	task.RecordTransition(oldStatus, newStatus, reason)

	return nil
}

// ListTasks 返回所有已注册的任务。
func (tm *TaskManager) ListTasks() []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make([]*Task, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		result = append(result, task)
	}
	return result
}

// isValidTransition 检查状态转换是否合法。
func (tm *TaskManager) isValidTransition(from, to TaskStatus) bool {
	allowed, exists := validTransitions[from]
	if !exists {
		return false
	}
	for _, status := range allowed {
		if status == to {
			return true
		}
	}
	return false
}

// GetTaskByID 是 GetTask 的包级便捷函数，用于不需要 TaskManager 实例的场景。
// 注意：这要求调用方自行管理任务实例。
func GetTaskByID(tasks map[int64]*Task, id int64) (*Task, error) {
	task, ok := tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %d not found", id)
	}
	return task, nil
}

// NextTaskID 生成新的任务 ID（雪花算法）。
func NextTaskID() int64 {
	return utils.GetSnowFlakeId()
}
