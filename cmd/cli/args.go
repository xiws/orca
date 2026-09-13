package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrHelpRequested 在用户仅请求帮助时返回。此时帮助
// 文本已打印到标准输出，调用方只需正常退出即可。
var ErrHelpRequested = errors.New("help requested")

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
	if len(os.Args) > 1 {
		// 全局帮助请求，优先于其他参数解析进行处理。
		switch os.Args[1] {
		case "-h", "--help", "help":
			PrintHelp()
			return nil, ErrHelpRequested
		}
	}

	// 优先检查子命令
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		// 第一个参数不以 - 开头，可能是子命令
		switch os.Args[1] {
		case "session":
			return parseSessionCommand()
		}
	}

	var files StringSlice

	fs := flag.NewFlagSet("orca", flag.ContinueOnError)
	// 每个解析问题都显示完整帮助，而不是仅显示标志
	// 默认值，后者不了解会话子命令。
	fs.Usage = PrintHelp

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
		// 在其他标志之后发现 -h/--help：flag 已通过 fs.Usage 打印了
		// 帮助信息，然后返回 ErrHelp。
		if errors.Is(err, flag.ErrHelp) {
			return nil, ErrHelpRequested
		}
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

// parseSessionCommand 收集 "session" 子命令的参数。缺少
// 操作时留给处理器处理，由它返回会话帮助信息。
func parseSessionCommand() (*CliArgs, error) {
	return &CliArgs{
		SubCommand: "session",
		SubArgs:    os.Args[2:],
	}, nil
}

// ParseSessionId 将会话 ID 从字符串解析为 int64。
func ParseSessionId(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// helpText 是 orca 命令的完整用法说明，通过 -h/--help 显示，
// 也在参数无法解析时展示。
const helpText = `Orca - AI Agent 命令行工具

Usage:
  orca [options] <prompt>                启动新任务
  orca [options] -session <id> <prompt>  恢复指定会话继续对话
  orca session <command>                 管理已保存的会话

Session Commands:
  list, ls               列出所有已保存的会话
  rm, remove <id...>     删除指定会话
  rm --all, -a           删除所有会话
  help                   显示 session 帮助

Options:
  -p, --provider <name>  指定模型 Provider，须与 -m 一起使用（models.json 中的 provider key，如 otter、ollama）
  -m, --model <id>       指定模型 ID，须与 -p 一起使用（如 chatgpt、deepseek）
  -s, --sp <file>        用文件内容覆盖系统提示词
  -f, --file <path>      附加文件到本次请求，可重复使用
  -session <id>          恢复指定的 session id 继续对话
  -h, --help             显示本帮助

Examples:
  orca "为 internal/llm/openai.go 生成单元测试文件"
  orca -p otter -m chatgpt "这个项目做了什么"
  orca -f main.go -f README.md "解释这两个文件"
  orca -session 1234567890123456789 "继续完成上面的任务"
  orca session list
  orca session rm 1234567890123456789
  orca session rm --all
`

// PrintHelp 将完整的 orca 用法文本写入标准输出。
func PrintHelp() {
	fmt.Fprint(os.Stdout, helpText)
}
