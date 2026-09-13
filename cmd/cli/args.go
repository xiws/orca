package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type StringSlice []string

func (s *StringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *StringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type CliArgs struct {
	Files        []string // 附加文件
	SystemPrompt string   // 系统提示词文件
	Model        string   // 模型
	Provider     string   // 模型 Provider
	Prompt       string   // 用户输入
	SessionId    int64    // 恢复的 session id
	SubCommand   string   // 子命令 (如 "session")
	SubArgs      []string // 子命令参数
}

func ParseArgs() (*CliArgs, error) {
	// Check for subcommands first
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		// First arg doesn't start with -, could be a subcommand
		switch os.Args[1] {
		case "session":
			return parseSessionCommand()
		}
	}

	var files StringSlice

	fs := flag.NewFlagSet("orca", flag.ContinueOnError)

	systemPrompt := fs.String("sp", "", "覆盖系统提示词，传入文件名称")
	fs.StringVar(systemPrompt, "s", "", "覆盖系统提示词，传入文件名称")

	model := fs.String("model", "", "指定模型")
	fs.StringVar(model, "m", "", "指定模型")

	fs.Var(&files, "file", "用户要传入的附加文件，可重复使用")
	fs.Var(&files, "f", "用户要传入的附加文件，可重复使用")

	// -p, --provider
	var provider string
	fs.StringVar(&provider, "p", "", "指定模型 Provider")
	fs.StringVar(&provider, "provider", "", "指定模型 Provider")

	// --session
	var sessionId int64
	fs.Int64Var(&sessionId, "session", 0, "恢复指定的 session id")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	args := fs.Args()
	if len(args) == 0 {
		return nil, fmt.Errorf("missing prompt")
	}

	return &CliArgs{
		Files:        files,
		SystemPrompt: *systemPrompt,
		Model:        *model,
		Provider:     provider,
		Prompt:       strings.Join(args, " "),
		SessionId:    sessionId,
	}, nil
}

// parseSessionCommand parses the "session" subcommand and its arguments.
func parseSessionCommand() (*CliArgs, error) {
	if len(os.Args) < 3 {
		return nil, fmt.Errorf("session command requires a subcommand (list, rm)")
	}

	return &CliArgs{
		SubCommand: "session",
		SubArgs:    os.Args[2:],
	}, nil
}

// ParseSessionId parses a session ID from string to int64.
func ParseSessionId(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
