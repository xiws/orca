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

type Request struct {
	Input        string
	Prompt       string
	SystemPrompt string
	Provider     llm.ModelInfo
}

type Turn struct {
	Task       *domain.Task
	Invocation *core.Invocation
}

type Executor interface {
	Run(*domain.Task, *core.Invocation) (string, error)
}

func Prepare(record *session.Record, request Request) *Turn {
	sess := record.Session
	task := domain.NewTask(request.Input, title(request.Input), sess.ID)
	inv := core.NewInvocation(task.ID, sess.ProjectPath, request.Provider)
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
	if !request.Provider.SupportsTools {
		inv.AppendMessage(llm.RoleSystem, handler.ToolPrompt())
	}
	for _, message := range sess.Messages {
		inv.Append(llm.ChatMessage{
			Id: message.ID, Role: string(message.Role), Content: message.Content, CreateTime: message.CreateTime,
		})
	}
	prompt := request.Prompt
	if prompt == "" {
		prompt = request.Input
	}
	inv.AppendMessage(llm.RoleUser, prompt)
	sess.AppendUser(task.ID, prompt)
	if sess.Title == "" {
		sess.Title = task.Title
	}
	record.Tasks = append(record.Tasks, task)
	return &Turn{Task: task, Invocation: inv}
}

func Execute(record *session.Record, turn *Turn, executor Executor) (string, error) {
	if turn.Task.SessionID != record.Session.ID || turn.Invocation.TaskID != turn.Task.ID {
		return "", errors.New("interaction: task, invocation and session do not match")
	}
	record.Capture(turn.Invocation)
	if err := session.Save(record); err != nil {
		return "", err
	}
	result, runErr := executor.Run(turn.Task, turn.Invocation)
	record.Capture(turn.Invocation)
	if runErr == nil {
		record.Session.AppendAssistant(turn.Task.ID, result)
	}
	return result, errors.Join(runErr, session.Save(record))
}

func title(input string) string {
	line := strings.SplitN(strings.TrimSpace(input), "\n", 2)[0]
	runes := []rune(line)
	if len(runes) > 50 {
		return string(runes[:47]) + "..."
	}
	return line
}
