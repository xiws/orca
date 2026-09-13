package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
	"github.com/xiws/orca/pkg/utils"
)

func main() {
	args, err := ParseArgs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "orca:", err)
		os.Exit(1)
	}

	// Handle subcommands first
	if args.SubCommand == "session" {
		if err := HandleSessionCommand(args.SubArgs); err != nil {
			fmt.Fprintln(os.Stderr, "orca:", err)
			os.Exit(1)
		}
		return
	}

	runtime := agent.NewRuntime()

	var task *agent.Task
	if args.SessionId > 0 {
		// Resume existing session
		task, err = ResumeTask(args)
	} else {
		// Create new session
		task, err = CreateTask(args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "orca:", err)
		os.Exit(1)
	}

	err, result := runtime.RunTask(task)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orca:", err)
		os.Exit(1)
	}
	fmt.Println(result)

	// Save session after successful execution
	if err := session.Save(task.SessionInfo); err != nil {
		fmt.Fprintf(os.Stderr, "orca: warning: failed to save session: %v\n", err)
	}
}

// CreateTask builds the conversation handed to the runtime: one system message
// describing the rules the model works under, then one user message carrying the
// attached files and the request itself.
//
// Attached files travel inside the user turn. They used to be appended as tool
// messages, which no server can accept: a tool message must answer a tool call
// made by the assistant message before it, and there is none at the start of a
// conversation.
func CreateTask(args *CliArgs) (*agent.Task, error) {
	var task = agent.NewTask(args.Prompt, "")
	if args.Model != "" && args.Provider != "" {
		task.SessionInfo.SetProvider(args.Provider, args.Model)
	}

	if args.SystemPrompt != "" {
		buffer, err := os.ReadFile(args.SystemPrompt)
		if err != nil {
			return nil, fmt.Errorf("read system prompt %s: %w", args.SystemPrompt, err)
		}
		task.SessionInfo.AppendMessage(llm.RoleSystem, string(buffer))
	} else {
		data := agent.PromptContext{
			ProjectPath:   utils.GetCurrentPath(),
			ContextLength: task.SessionInfo.Provider.ContextWindow,
		}

		if task.SessionInfo.Provider.API != "otter" {
			task.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(data))
			// Providers without native function calling receive the command protocol
			// as part of the system context instead
			task.SessionInfo.AppendToolPrompt()
		} else {
			task.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetOtterSystemPrompt(data))
		}
	}

	prompt, err := userPrompt(args)
	if err != nil {
		return nil, err
	}
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)
	return task, nil
}

// userPrompt renders the user turn: every attached file wrapped in a <file>
// element, followed by the request itself.
func userPrompt(args *CliArgs) (string, error) {
	var builder strings.Builder
	for _, file := range args.Files {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read attached file %s: %w", file, err)
		}
		content := strings.TrimRight(string(data), "\n")
		fmt.Fprintf(&builder, "<file path=%q>\n%s\n</file>\n\n", file, content)
	}
	builder.WriteString(args.Prompt)
	return builder.String(), nil
}

// ResumeTask loads an existing session and prepares a task to continue the conversation.
// It appends the new user message to the existing session history.
func ResumeTask(args *CliArgs) (*agent.Task, error) {
	// Load the existing session
	sess, err := session.Load(args.SessionId)
	if err != nil {
		return nil, fmt.Errorf("load session %d: %w", args.SessionId, err)
	}

	// Create a task with the loaded session
	task := &agent.Task{
		Id:          utils.GetSnowFlakeId(),
		SessionInfo: sess,
		TaskTarget:  args.Prompt,
	}

	// Allow overriding provider if specified
	if args.Model != "" && args.Provider != "" {
		task.SessionInfo.SetProvider(args.Provider, args.Model)
	}

	// Append the new user message
	prompt, err := userPrompt(args)
	if err != nil {
		return nil, err
	}
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)

	return task, nil
}
