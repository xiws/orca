// Package main 实现 Orca 的命令行界面（CLI），支持提交任务、管理会话和执行各种运行操作。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ErrHelpRequested 表示用户请求了帮助信息，不应视为错误。
var ErrHelpRequested = errors.New("help requested")

// StringSlice 实现 flag.Value 接口，支持通过重复 flag 传递多个字符串值。
type StringSlice []string

func (s *StringSlice) String() string         { return strings.Join(*s, ",") }
func (s *StringSlice) Set(value string) error { *s = append(*s, value); return nil }

// CliArgs 保存命令行解析后的所有参数。
type CliArgs struct {
	Files        []string // 附加的文件路径列表
	SystemPrompt string   // 系统提示词文件路径
	Model        string   // 模型名称
	Provider     string   // 提供者名称
	Mode         string   // 工作流模式（ask/code/plan/agent 等）
	Prompt       string   // 用户输入的提示词
	SessionId    int64    // 要继续的会话 ID
	SubCommand   string   // 子命令（run/session/task）
	SubArgs      []string // 子命令的参数
	After        int64    // show 命令的事件序列号起点
}

// ParseArgs 从 os.Args 解析命令行参数。
func ParseArgs() (*CliArgs, error) { return parseArgs(os.Args[1:], os.Stdout) }

// parseArgs 解析命令行参数，支持帮助、子命令和直接提交两种模式。
func parseArgs(argv []string, out io.Writer) (*CliArgs, error) {
	// 处理帮助请求
	if len(argv) > 0 && (argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help") {
		if len(argv) != 1 {
			return nil, fmt.Errorf("unexpected help arguments")
		}
		fmt.Fprint(out, helpText)
		return nil, ErrHelpRequested
	}
	// 处理子命令（run/session/task），将剩余参数透传
	if len(argv) > 0 && (argv[0] == "run" || argv[0] == "session" || argv[0] == "task") {
		a := &CliArgs{SubCommand: argv[0], SubArgs: append([]string(nil), argv[1:]...)}
		if err := validateCommand(a); err != nil {
			return nil, err
		}
		return a, nil
	}
	// 普通提交模式：解析 flags
	a := &CliArgs{}
	var files StringSlice
	fs := flag.NewFlagSet("orca", flag.ContinueOnError)
	// Return parse errors to main, which quotes them before terminal output.
	fs.SetOutput(io.Discard)
	fs.Usage = func() { fmt.Fprint(out, helpText) }
	fs.StringVar(&a.SystemPrompt, "sp", "", "system prompt file")
	fs.StringVar(&a.SystemPrompt, "s", "", "system prompt file")
	fs.StringVar(&a.Provider, "p", "", "provider (requires -m)")
	fs.StringVar(&a.Provider, "provider", "", "provider (requires -m)")
	fs.StringVar(&a.Model, "m", "", "model (requires -p)")
	fs.StringVar(&a.Model, "model", "", "model (requires -p)")
	fs.Var(&files, "f", "attached file; repeatable")
	fs.Var(&files, "file", "attached file; repeatable")
	fs.Func("session", "continue session with a NEW task/run", func(value string) error {
		id, err := ParseSessionId(value)
		if err == nil {
			a.SessionId = id
		}
		return err
	})
	fs.StringVar(&a.Mode, "mode", "", "workflow mode")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, ErrHelpRequested
		}
		return nil, err
	}
	a.Files, a.Prompt = files, strings.Join(fs.Args(), " ")
	// 合并剩余位置参数为提示词，并校验整体参数合法性
	if err := validateSubmitArgs(a); err != nil {
		return nil, err
	}
	return a, nil
}

// validateSubmitArgs 校验直接提交模式下的参数合法性。
func validateSubmitArgs(a *CliArgs) error {
	if (a.Provider == "") != (a.Model == "") {
		return fmt.Errorf("provider and model must be supplied together (-p PROVIDER -m MODEL)")
	}
	if a.SessionId < 0 {
		return fmt.Errorf("session ID must be positive")
	}
	if err := validateMode(a.Mode); err != nil {
		return err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return fmt.Errorf("missing prompt")
	}
	return nil
}

// validateMode 校验工作流模式名称是否合法。
func validateMode(name string) error {
	switch name {
	case "", "ask", "code", "plan", "agent", "review", "test", "terminal", "deliberate":
		return nil
	default:
		return fmt.Errorf("unknown mode %q", name)
	}
}

// ParseSessionId 将字符串解析为正整数 session ID，拒绝非数字或负值。
func ParseSessionId(s string) (int64, error) {
	if strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, fmt.Errorf("invalid positive ID %q", s)
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid positive ID %q", s)
	}
	return id, nil
}

// validateCommand 校验子命令及其参数的格式和合法性。
func validateCommand(a *CliArgs) error {
	if a.SubCommand != "run" && a.SubCommand != "session" && a.SubCommand != "task" {
		return fmt.Errorf("unknown command %q", a.SubCommand)
	}
	args := a.SubArgs
	if len(args) == 0 {
		return nil
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if len(args) != 1 {
			return fmt.Errorf("unexpected help arguments")
		}
		return nil
	}
	if a.SubCommand == "task" {
		if args[0] != "revise" || len(args) < 3 || strings.TrimSpace(strings.Join(args[2:], " ")) == "" {
			return fmt.Errorf("usage: orca task revise ID INPUT")
		}
		_, err := ParseSessionId(args[1])
		return err
	}
	if args[0] == "list" || args[0] == "ls" {
		if len(args) != 1 {
			return fmt.Errorf("list takes no arguments")
		}
		return nil
	}
	if a.SubCommand == "session" {
		switch args[0] {
		case "import":
			if len(args) != 4 || args[2] != "--owner" || strings.TrimSpace(args[1]) == "" || strings.TrimSpace(args[3]) == "" {
				return fmt.Errorf("usage: orca session import PATH --owner WORKSPACE")
			}
			return nil
		case "rm", "remove", "delete":
			if len(args) < 2 {
				return fmt.Errorf("rm requires a session ID or --all")
			}
			if len(args) == 2 && (args[1] == "--all" || args[1] == "-a") {
				return nil
			}
			for _, arg := range args[1:] {
				if _, err := ParseSessionId(arg); err != nil {
					return err
				}
			}
			return nil
		case "show":
			if len(args) != 2 {
				return fmt.Errorf("usage: orca session show ID")
			}
		default:
			return fmt.Errorf("unknown session subcommand %q", args[0])
		}
	} else {
		switch args[0] {
		case "show":
			if len(args) == 4 && args[2] == "--after" {
				after, err := strconv.ParseInt(args[3], 10, 64)
				if err != nil || after < 0 {
					return fmt.Errorf("after must be a non-negative event sequence")
				}
				a.After = after
			} else if len(args) != 2 {
				return fmt.Errorf("usage: orca run show ID [--after SEQUENCE]")
			}
		case "reconcile":
			if len(args) != 4 || args[2] != "--note" || strings.TrimSpace(args[3]) == "" {
				return fmt.Errorf("usage: orca run reconcile ID --note TEXT (only after manual verification; never replays)")
			}
		case "resume", "cancel", "retry", "approve", "reject":
			if len(args) != 2 {
				return fmt.Errorf("usage: orca run %s ID", args[0])
			}
		case "respond":
			if len(args) < 3 || strings.TrimSpace(strings.Join(args[2:], " ")) == "" {
				return fmt.Errorf("usage: orca run respond INPUT_ID TEXT")
			}
		default:
			return fmt.Errorf("unknown run subcommand %q", args[0])
		}
	}
	_, err := ParseSessionId(args[1])
	return err
}

// helpText 包含 CLI 的帮助文本，描述所有子命令和选项的用法。
const helpText = `Orca — durable tasks and runs

Usage:
  orca [options] PROMPT
  orca -session ID [options] PROMPT    Continue a session with a NEW task/run
  orca run list
  orca run show ID [--after SEQUENCE]  Replay all durable events, including child runs
  orca run resume ID                  Resume an interrupted run (not a new task)
  orca run cancel ID
  orca run retry TASK_ID
  orca run respond INPUT_ID TEXT
  orca run approve INPUT_ID
  orca run reject INPUT_ID
  orca run reconcile ID --note TEXT   Manually verified unknown outcome; mark failed, NEVER replay
  orca task revise ID INPUT           Create a task revision, without executing it
  orca session list
  orca session show ID
  orca session import PATH --owner WORKSPACE
  orca session rm ID...|--all

Options (before PROMPT):
  -p, --provider NAME   Must be paired with -m
  -m, --model NAME      Must be paired with -p
  -session ID          Continue a session
  -mode NAME           ask/code/plan/agent/review/test/terminal/deliberate
  -f, --file PATH       Attach file; repeatable
  -sp, -s FILE          Override system prompt from file
  -h, --help

Waiting input/approval is saved and exits successfully; use its INPUT_ID to reply.
Delta notifications are advisory; run show --after replays durable events.
`

// PrintHelp 向标准输出打印帮助信息。
func PrintHelp() { fmt.Fprint(os.Stdout, helpText) }
