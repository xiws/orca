// Package memory 提供多维度记忆服务，为 Agent 提供上下文。
//
// 记忆分为三层：
//   - SessionMemory: 短期记忆，单次任务上下文
//   - WorkflowMemory: 流程记忆，workflow 实例状态
//   - ProjectMemory: 长期记忆，项目级知识
package memory

// MemoryService 是记忆服务的统一入口。
type MemoryService struct {
	session  *SessionMemory
	workflow *WorkflowMemory
	project  *ProjectMemory
}

// NewMemoryService 创建记忆服务。
func NewMemoryService() *MemoryService {
	return &MemoryService{
		session:  NewSessionMemory(),
		workflow: NewWorkflowMemory(),
		project:  NewProjectMemory(),
	}
}

// Session 返回会话记忆管理器。
func (s *MemoryService) Session() *SessionMemory {
	return s.session
}

// Workflow 返回工作流记忆管理器。
func (s *MemoryService) Workflow() *WorkflowMemory {
	return s.workflow
}

// Project 返回项目记忆管理器。
func (s *MemoryService) Project() *ProjectMemory {
	return s.project
}

// Reset 重置所有记忆（用于测试或新任务开始）。
func (s *MemoryService) Reset() {
	s.session = NewSessionMemory()
	s.workflow = NewWorkflowMemory()
	// ProjectMemory 不重置，因为它是长期记忆
}
