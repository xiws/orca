package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
	"github.com/xiws/orca/pkg/utils"
)

func main() {
	args, err := ParseArgs()
	if err != nil {
		// 帮助信息已由 ParseArgs 自身打印。
		if errors.Is(err, ErrHelpRequested) {
			return
		}
		fmt.Fprintln(os.Stderr, "orca:", err)
		os.Exit(1)
	}

	// 优先处理子命令
	if args.SubCommand == "session" {
		if err := HandleSessionCommand(args.SubArgs); err != nil {
			fmt.Fprintln(os.Stderr, "orca:", err)
			os.Exit(1)
		}
		return
	}

	runtime := core.NewRuntime()

	var task *core.Task
	if args.SessionId > 0 {
		// 恢复已有会话
		task, err = ResumeTask(args)
	} else {
		// 创建新会话
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

	// 成功执行后保存会话
	if err := session.Save(task.SessionInfo); err != nil {
		fmt.Fprintf(os.Stderr, "orca: warning: failed to save session: %v\n", err)
	}
}

// CreateTask 构建交给运行时的对话：一条系统消息描述模型工作的规则，
// 然后一条用户消息携带附加文件和请求本身。
//
// 附加文件在用户消息中传递。它们曾经作为工具消息附加，
// 但没有服务器能接受这种方式：工具消息必须回复之前助手消息发出的工具调用，
// 而对话开始时并没有这样的调用。
//
// 不支持原生函数调用的 Provider 会在系统上下文中
// 接收命令协议描述。
func CreateTask(args *CliArgs) (*core.Task, error) {
	var task = core.NewTask(args.Prompt, "")
	task.SessionInfo.Title = generateTitle(args.Prompt)
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
		data := core.PromptContext{
			ProjectPath:   utils.GetCurrentPath(),
			ContextLength: task.SessionInfo.Provider.ContextWindow,
		}

		if task.SessionInfo.Provider.API != "otter" {
			task.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(data))
			// 不支持原生函数调用的 Provider 会在系统上下文中
			// 接收命令协议描述
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

// userPrompt 渲染用户消息：每个附加文件包裹在 <file>
// 元素中，后面跟着请求本身。
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

// generateTitle 从用户首条 prompt 生成会话标题。
// 取第一行，截断到 maxTitleLen 个字符。
func generateTitle(prompt string) string {
	const maxTitleLen = 50
	// 取第一行
	line := strings.SplitN(strings.TrimSpace(prompt), "\n", 2)[0]
	line = strings.TrimSpace(line)
	if len([]rune(line)) <= maxTitleLen {
		return line
	}
	return string([]rune(line)[:maxTitleLen-3]) + "..."
}

// ResumeTask 加载已有会话并准备任务以继续对话。
// 它将新的用户消息追加到已有会话历史中。
func ResumeTask(args *CliArgs) (*core.Task, error) {
	// 加载已有会话
	sess, err := session.Load(args.SessionId)
	if err != nil {
		return nil, fmt.Errorf("load session %d: %w", args.SessionId, err)
	}

	// 使用已加载的会话创建任务
	task := &core.Task{
		Id:          utils.GetSnowFlakeId(),
		SessionInfo: sess,
		TaskTarget:  args.Prompt,
	}

	// 如果指定了 provider 则允许覆盖
	if args.Model != "" && args.Provider != "" {
		task.SessionInfo.SetProvider(args.Provider, args.Model)
	}

	// 追加新的用户消息
	prompt, err := userPrompt(args)
	if err != nil {
		return nil, err
	}
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)

	return task, nil
}
