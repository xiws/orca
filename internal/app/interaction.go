// Package app 提供核心应用服务层，协调会话管理、任务提交、工作流驱动和事件发布。
package app

import (
	"errors"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
	"github.com/xiws/orca/pkg/utils"
)

// Request 表示一次用户请求，包含输入、提示词、系统提示和模型信息。
type Request struct {
	Input        string        // 用户原始输入
	Prompt       string        // 实际发送给模型的提示词（为空时使用 Input）
	SystemPrompt string        // 自定义系统提示词（为空时使用默认）
	Provider     llm.ModelInfo // 模型提供商信息
}

// Turn 表示一轮交互，包含创建的任务和对应的调用上下文。
type Turn struct {
	Task       *domain.Task     // 本轮创建的任务
	Invocation *core.Invocation // 模型调用上下文
}

// Executor 定义任务执行器接口。
type Executor interface {
	Run(*domain.Task, *core.Invocation) (string, error)
}

// Prepare 为一轮交互创建任务和调用上下文，包括初始化系统提示、恢复历史消息。
func Prepare(record *session.Record, request Request) *Turn {
	sess := record.Session
	// 创建新任务，使用用户输入和截断标题
	task := domain.NewTask(request.Input, title(request.Input), sess.ID)
	inv := core.NewInvocation(task.ID, sess.ProjectPath, request.Provider)
	// 设置系统提示：优先使用自定义的，否则根据 API 类型生成默认提示
	if request.SystemPrompt != "" {
		inv.AppendMessage(llm.RoleSystem, request.SystemPrompt)
	} else {
		context := core.SystemPromptContext{ProjectPath: sess.ProjectPath, ContextLength: request.Provider.ContextWindow}
		if request.Provider.API == "otter" {
			inv.AppendMessage(llm.RoleSystem, utils.GetOtterSystemPrompt(context))
		} else {
			inv.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(context))
		}
	}
	// 如果模型不支持工具调用，追加工具提示到系统消息
	if !request.Provider.SupportsTools {
		inv.AppendMessage(llm.RoleSystem, handler.ToolPrompt())
	}
	// 恢复会话中的历史消息到调用上下文
	for _, message := range sess.Messages {
		inv.Append(llm.ChatMessage{
			Id: message.ID, Role: string(message.Role), Content: message.Content, CreateTime: message.CreateTime,
		})
	}
	// 确定实际提示词：优先使用 Prompt，否则使用 Input
	prompt := request.Prompt
	if prompt == "" {
		prompt = request.Input
	}
	// 追加用户消息到调用上下文和会话时间线
	inv.AppendMessage(llm.RoleUser, prompt)
	sess.AppendUser(task.ID, prompt)
	// 首次交互时自动设置会话标题
	if sess.Title == "" {
		sess.Title = task.Title
	}
	record.Tasks = append(record.Tasks, task)
	return &Turn{Task: task, Invocation: inv}
}

// Execute 执行一轮交互，运行任务并将结果追加到会话。
func Execute(record *session.Record, turn *Turn, executor Executor) (string, error) {
	// 校验任务、调用和会话的一致性
	if turn.Task.SessionID != record.Session.ID || turn.Invocation.TaskID != turn.Task.ID {
		return "", errors.New("interaction: task, invocation and session do not match")
	}
	// 快照当前调用上下文
	record.Capture(turn.Invocation)
	if err := session.Save(record); err != nil {
		return "", err
	}
	// 执行任务
	result, runErr := executor.Run(turn.Task, turn.Invocation)
	// 执行后再次快照，确保调用上下文更新被持久化
	record.Capture(turn.Invocation)
	if runErr == nil {
		// 仅在成功时将助手回复追加到会话时间线
		record.Session.AppendAssistant(turn.Task.ID, result)
	}
	return result, errors.Join(runErr, session.Save(record))
}

// title 从用户输入中提取第一行作为标题，超过 50 个字符则截断。
func title(input string) string {
	line := strings.SplitN(strings.TrimSpace(input), "\n", 2)[0]
	runes := []rune(line)
	if len(runes) > 50 {
		return string(runes[:47]) + "..."
	}
	return line
}
