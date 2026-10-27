// 工作流模板定义：根据模式名称配置角色列表和工具策略
package workflow

import (
	"fmt"

	"github.com/xiws/orca/internal/domain"
)

// Template 定义工作流模板，包含角色序列和工具授权策略
type Template struct {
	Name    string        // 模板名称
	Version int           // 模板版本
	Roles   []string      // 角色列表（executor, validator, verifier 等）
	Policy  domain.Policy // 工具授权策略
}

// Mode 根据名称返回对应的工作流模板。默认模式为 "code"。
func Mode(name string) (Template, error) {
	if name == "" {
		name = "code"
	}
	t := Template{Name: name, Version: 1, Policy: domain.Policy{Tools: []string{"read", "request_input"}}}
	switch name {
	case "ask": // 问答模式：仅回复，不使用工具
		t.Roles = []string{"responder"}
	case "review": // 代码审查模式
		t.Roles = []string{"reviewer"}
	case "plan": // 计划模式：生成执行计划
		t.Roles = []string{"planner"}
	case "code": // 编码模式：单轮执行，支持读写和 bash
		t.Roles = []string{"executor"}
		t.Policy.Tools = []string{"read", "write", "edit", "bash", "create_task", "request_input"}
	case "agent": // 智能体模式：执行-验证-确认多角色协作
		t.Roles = []string{"executor", "validator", "verifier"}
		t.Policy.Tools = []string{"read", "write", "edit", "bash", "create_task", "request_input"}
	case "test": // 测试模式：含验证角色，但不创建子任务
		t.Roles = []string{"executor", "validator", "verifier"}
		t.Policy.Tools = []string{"read", "write", "edit", "bash", "request_input"}
	case "terminal": // 终端模式：仅执行 bash
		t.Roles = []string{"terminal"}
		t.Policy.Tools = []string{"bash", "request_input"}
	case "deliberate": // 深思模式：回复 + 多角度评审
		t.Roles = []string{"responder", "critic-correctness", "critic-security", "judge"}
	default:
		return Template{}, fmt.Errorf("unknown mode %q", name)
	}
	return t, nil
}

// Nodes 将角色列表转换为工作流节点序列，每个节点初始状态为 pending
func (t Template) Nodes() []domain.Node {
	nodes := make([]domain.Node, len(t.Roles))
	for i, role := range t.Roles {
		nodes[i] = domain.Node{ID: fmt.Sprintf("%d-%s", i, role), Role: role, State: "pending", Attempt: 1}
	}
	return nodes
}
