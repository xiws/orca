// Package agent 提供 Agent 的高层编排逻辑，包括角色定义（profile）、模式编排（modes）
// 和执行状态机（runner）。Agent 通过组合不同角色完成复杂的开发任务。
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

// Profile 描述一个 Agent 角色的完整配置。
// 包含角色名称、版本、系统提示词、工具权限和输出契约。
type Profile struct {
	// Name 角色名称（如 executor、planner、verifier 等）。
	Name string
	// Version 角色配置版本号，用于兼容性检查。
	Version int
	// Prompt 角色的系统提示词。
	Prompt string
	// Capabilities 角色的工具权限策略。
	Capabilities domain.Policy
	// Contract 角色的输出契约（如 "review"、"plan"、"verification"），
	// 用于 ValidateResult 验证输出格式。空字符串表示无结构化约束。
	Contract string
}

// Role 按名称构造角色配置。
// 支持的名称：executor / responder / reviewer / planner / verifier / validator / repair /
// critic-correctness / critic-security / judge / terminal。
// 每个角色预设了不同的工具权限、系统提示词和输出契约。
func Role(name string) (Profile, error) {
	// 默认的只读权限
	read := domain.Policy{Tools: []string{"read", "request_input"}}
	// 完整的读写权限
	write := domain.Policy{Tools: []string{"read", "write", "edit", "bash", "create_task", "request_input"}}
	p := Profile{Name: name, Version: 1, Capabilities: read}
	switch name {
	case "executor":
		// 执行者：拥有完整工具权限，负责实际完成任务
		p.Capabilities = write
		p.Prompt = "Complete the goal using evidence from the workspace. Use request_input for missing requirements and create_task only for independently verifiable subgoals. Never claim a tool succeeded without its result."
	case "responder":
		// 回答者：只读权限，生成回答但不修改文件
		p.Prompt = "Answer the user's question using available evidence. Do not modify files or execute shell commands."
	case "reviewer":
		// 评审者：只读权限，输出结构化的评审结果
		p.Prompt = "Review without making changes. Return JSON {\"summary\":string,\"findings\":[{\"file\":string,\"line\":number,\"description\":string}]}. Every finding must cite evidence."
		p.Contract = "review"
	case "planner":
		// 规划者：只读权限，输出结构化的执行计划
		p.Prompt = "Clarify missing requirements with request_input. Return JSON {\"goal\":string,\"steps\":[{\"id\":string,\"description\":string,\"dependencies\":[string]}]}. Do not execute the plan."
		p.Contract = "plan"
	case "verifier":
		// 验证者：只读权限，独立验证任务完成情况
		p.Prompt = "Independently evaluate the goal against provided changes and tool evidence. Return JSON {\"status\":\"passed\"|\"failed\"|\"inconclusive\",\"summary\":string,\"evidence\":[string]}. Passed requires concrete acceptance evidence, never just the executor's claim. You cannot execute tests yourself."
		p.Contract = "verification"
	case "validator":
		// 验证执行者：拥有 read + bash 权限，可运行测试验证
		p.Capabilities = domain.Policy{Tools: []string{"read", "bash", "request_input"}}
		p.Prompt = "Validate the changes against the goal. Tests execute arbitrary project code and need tool authorization. Execute relevant checks and report their exit codes and evidence. Do not edit source files."
	case "repair":
		// 修复者：拥有完整工具权限，针对验证失败进行修复
		p.Capabilities = write
		p.Prompt = "Repair only the failed acceptance criteria using the supplied verification evidence. Preserve the original goal. Recheck changes and describe actual results."
	case "critic-correctness":
		// 正确性评论者：只读权限，从正确性角度点评
		p.Prompt = "Critique the proposed answer independently for correctness. Cite specific contradictory evidence; do not modify files."
	case "critic-security":
		// 安全性评论者：只读权限，从安全和边界情况角度点评
		p.Prompt = "Critique the proposed answer independently for security and edge cases. Cite specific evidence; do not run shell commands."
	case "judge":
		// 裁判：只读权限，汇总多方观点给出最终判断
		p.Prompt = "Synthesize the original answer and independent critiques in their given order. Resolve contradictions using evidence and disclose uncertainty."
	case "terminal":
		// 终端操作者：只有 bash 权限，执行终端命令
		p.Capabilities = domain.Policy{Tools: []string{"bash", "request_input"}}
		p.Prompt = "Fulfil the user's terminal operation through the bash tool. Do not claim commands ran without tool results. Commands require explicit authorization."
	default:
		return Profile{}, fmt.Errorf("unknown role %q", name)
	}
	return p, nil
}

// Step 描述执行计划中的一个步骤。
type Step struct {
	// ID 步骤唯一标识。
	ID string `json:"id"`
	// Description 步骤描述。
	Description string `json:"description"`
	// Dependencies 该步骤依赖的其他步骤 ID 列表。
	Dependencies []string `json:"dependencies"`
}

// Plan 描述一个带依赖关系的执行计划。
type Plan struct {
	// Goal 计划目标。
	Goal string `json:"goal"`
	// Steps 步骤列表。
	Steps []Step `json:"steps"`
}

// Verification 描述验证结果。
type Verification struct {
	// Status 验证状态：passed / failed / inconclusive。
	Status string `json:"status"`
	// Summary 验证总结。
	Summary string `json:"summary"`
	// Evidence 验证依据列表（passed 时必须非空）。
	Evidence []string `json:"evidence"`
}

// Finding 描述代码评审中的一个发现。
type Finding struct {
	// File 相关源文件路径。
	File string `json:"file"`
	// Line 相关代码行号。
	Line int `json:"line"`
	// Description 发现描述。
	Description string `json:"description"`
}

// Review 描述代码评审结果。
type Review struct {
	// Summary 评审总结。
	Summary string `json:"summary"`
	// Findings 评审发现列表。
	Findings []Finding `json:"findings"`
}

// decodeResult 从模型输出中解码结构化 JSON 结果。
// 支持 ```json...``` 代码块包裹格式。使用严格解码（不允许未知字段）。
func decodeResult(text string, v any) error {
	text = strings.TrimSpace(text)
	// 去除 ```json...``` 代码块包裹
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
	}
	// 严格 JSON 格式校验
	if err := model.StrictJSON([]byte(text)); err != nil {
		return fmt.Errorf("invalid structured result: %w", err)
	}
	d := json.NewDecoder(bytes.NewBufferString(text))
	// 不允许未知字段，确保模型输出与预期结构完全匹配
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("invalid structured result: %w", err)
	}
	// 检查是否有多余的尾部数据
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("structured result contains trailing data")
	}
	return nil
}

// ValidateResult 根据角色的输出契约验证模型输出格式。
// 不同契约（verification / plan / review）有不同的验证规则。
func ValidateResult(profile Profile, text string) error {
	switch profile.Contract {
	case "verification":
		_, err := ParseVerification(text)
		return err
	case "plan":
		var p Plan
		if err := decodeResult(text, &p); err != nil {
			return err
		}
		// 校验计划的基本约束
		if p.Goal == "" || len(p.Steps) == 0 || len(p.Steps) > 32 {
			return fmt.Errorf("plan requires a goal and 1..32 steps")
		}
		steps := make(map[string]Step, len(p.Steps))
		for _, s := range p.Steps {
			if s.ID == "" || s.Description == "" {
				return fmt.Errorf("plan step missing identity or description")
			}
			if _, ok := steps[s.ID]; ok {
				return fmt.Errorf("duplicate plan step %s", s.ID)
			}
			steps[s.ID] = s
		}
		// 检测依赖环
		seen := map[string]int{}
		var visit func(string) error
		visit = func(id string) error {
			s, ok := steps[id]
			if !ok {
				return fmt.Errorf("unknown dependency %s", id)
			}
			if seen[id] == 1 {
				return fmt.Errorf("cyclic plan dependency %s", id)
			}
			if seen[id] == 2 {
				return nil
			}
			seen[id] = 1 // 标记为访问中
			for _, dep := range s.Dependencies {
				if err := visit(dep); err != nil {
					return err
				}
			}
			seen[id] = 2 // 标记为已完成
			return nil
		}
		for id := range steps {
			if err := visit(id); err != nil {
				return err
			}
		}
	case "review":
		var r Review
		if err := decodeResult(text, &r); err != nil {
			return err
		}
		if r.Summary == "" {
			return fmt.Errorf("review summary required")
		}
		// 校验每个发现必须包含文件来源
		for _, f := range r.Findings {
			if f.File == "" || f.Line < 1 || f.Description == "" {
				return fmt.Errorf("review finding lacks source evidence")
			}
		}
	default:
		// 无结构化契约时，至少要求输出非空
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("empty role result")
		}
	}
	return nil
}

// ParseVerification 从模型输出中解析验证结果。
// 校验 status 必须为 passed/failed/inconclusive，passed 时必须提供 evidence。
func ParseVerification(text string) (Verification, error) {
	var v Verification
	if err := decodeResult(text, &v); err != nil {
		return v, err
	}
	if v.Status != "passed" && v.Status != "failed" && v.Status != "inconclusive" {
		return v, fmt.Errorf("invalid verification status %q", v.Status)
	}
	if v.Summary == "" || (v.Status == "passed" && len(v.Evidence) == 0) {
		return v, fmt.Errorf("verification lacks summary or evidence")
	}
	return v, nil
}
