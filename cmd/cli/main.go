package main

import (
	"errors"
	"fmt"
	"orca/internal/agent"
	"orca/internal/handler"
	"orca/internal/llm"
	"orca/pkg/utils"
	"os"
	"strings"
)

func main() {
	args, err := ParseArgs()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	runtime := agent.NewRuntime()
	task := CreateTask(args, runtime)
	err, result := runtime.RunTask(task)
	if err != nil {
		panic(err)
	}
	fmt.Println(result)
}

func CreateTask(args *CliArgs, runtime *agent.Runtime) *agent.Task {
	var task = agent.NewTask(args.Prompt, "")
	if args.Model != "" && args.Provider != "" {
		task.SessionInfo.SetProvider(args.Provider, args.Model)
	}

	if args.SystemPrompt != "" {
		buffer, err := os.ReadFile(args.SystemPrompt)
		if err != nil {
			panic(err)
		}

		task.SessionInfo.AppendMessage(llm.RoleSystem, string(buffer))
	} else {
		data := agent.PromptContext{
			ProjectPath:   utils.GetCurrentPath(),
			ContextLength: task.SessionInfo.Provider.ContextWindow,
		}

		var defaultPrompt = utils.GetSystemPrompt(data)
		task.SessionInfo.AppendMessage(llm.RoleSystem, defaultPrompt)
	}

	if args.Files != nil && len(args.Files) > 0 {
		for _, file := range args.Files {
			task.SessionInfo.AppendMessage(llm.RoleTool, readTxt(file, runtime))
		}
	}

	prompt := fmt.Sprintf("attach files: %s;user description:%s", strings.Join(args.Files, ","), args.Prompt)
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)

	return task
}

func readTxt(filename string, runtime *agent.Runtime) string {
	var id = utils.GetSnowFlakeId()
	cmd := handler.NewReadOption(id, filename, 0, 0)
	var err, res = runtime.ExecuteCommand(cmd)
	if err != nil {
		panic(err)
	}

	msg, ok := res.(handler.CommandResult)
	if !ok {
		panic("should be string")
	}
	if !msg.OK {
		panic(errors.New(msg.Err))
	}
	return msg.Content
}
