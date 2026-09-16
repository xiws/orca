package memory

import "sync"

// ProjectMemory 是长期记忆，存储项目级知识。
//
// 生命周期：跨任务持久化。
// 内容：项目架构、编码规范、约定、约束。
//
// 这些知识可以跨任务复用，为 Agent 提供项目上下文。
type ProjectMemory struct {
	mu sync.RWMutex

	// ProjectPath 项目根目录路径。
	ProjectPath string

	// Architecture 项目架构描述。
	Architecture string

	// Conventions 编码规范列表。
	Conventions []string

	// Constraints 项目约束列表。
	Constraints []string

	// Knowledge 额外知识，key-value 形式。
	Knowledge map[string]string
}

// NewProjectMemory 创建项目记忆。
func NewProjectMemory() *ProjectMemory {
	return &ProjectMemory{
		Conventions: make([]string, 0),
		Constraints: make([]string, 0),
		Knowledge:   make(map[string]string),
	}
}

// SetProjectPath 设置项目路径。
func (m *ProjectMemory) SetProjectPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProjectPath = path
}

// GetProjectPath 获取项目路径。
func (m *ProjectMemory) GetProjectPath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ProjectPath
}

// SetArchitecture 设置项目架构描述。
func (m *ProjectMemory) SetArchitecture(arch string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Architecture = arch
}

// GetArchitecture 获取项目架构描述。
func (m *ProjectMemory) GetArchitecture() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Architecture
}

// AddConvention 添加编码规范。
func (m *ProjectMemory) AddConvention(convention string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Conventions = append(m.Conventions, convention)
}

// GetConventions 获取所有编码规范。
func (m *ProjectMemory) GetConventions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.Conventions))
	copy(result, m.Conventions)
	return result
}

// AddConstraint 添加项目约束。
func (m *ProjectMemory) AddConstraint(constraint string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Constraints = append(m.Constraints, constraint)
}

// GetConstraints 获取所有项目约束。
func (m *ProjectMemory) GetConstraints() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.Constraints))
	copy(result, m.Constraints)
	return result
}

// SetKnowledge 设置知识条目。
func (m *ProjectMemory) SetKnowledge(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Knowledge[key] = value
}

// GetKnowledge 获取知识条目。
func (m *ProjectMemory) GetKnowledge(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.Knowledge[key]
	return value, ok
}

// Clear 清空项目记忆（谨慎使用）。
func (m *ProjectMemory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProjectPath = ""
	m.Architecture = ""
	m.Conventions = make([]string, 0)
	m.Constraints = make([]string, 0)
	m.Knowledge = make(map[string]string)
}
