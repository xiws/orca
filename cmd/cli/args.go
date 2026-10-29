// Package main 实现 Orca 的命令行界面：提交一个任务，实时输出执行过程，
// 在需要回答或审批时从标准输入读取。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	Mode         string   // 工作流模式
	Prompt       string   // 用户输入的提示词
}

// ParseArgs 从 os.Args 解析命令行参数。
func ParseArgs() (*CliArgs, error) { return parseArgs(os.Args[1:], os.Stdout) }

// parseArgs 解析帮助请求和提交参数。
func parseArgs(argv []string, out io.Writer) (*CliArgs, error) {
	if len(argv) > 0 && (argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help") {
		if len(argv) != 1 {
			return nil, fmt.Errorf("unexpected help arguments")
		}
		fmt.Fprint(out, helpText)
		return nil, ErrHelpRequested
	}
	a := &CliArgs{}
	var files StringSlice
	fs := flag.NewFlagSet("orca", flag.ContinueOnError)
	// 解析错误交给 main 统一加引号输出
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
	fs.StringVar(&a.Mode, "mode", "", "workflow mode")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, ErrHelpRequested
		}
		return nil, err
	}
	a.Files, a.Prompt = files, strings.Join(fs.Args(), " ")
	if err := validateSubmitArgs(a); err != nil {
		return nil, err
	}
	return a, nil
}

// validateSubmitArgs 校验提交参数是否合法。
func validateSubmitArgs(a *CliArgs) error {
	if (a.Provider == "") != (a.Model == "") {
		return fmt.Errorf("provider and model must be supplied together (-p PROVIDER -m MODEL)")
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

// helpText 是 CLI 的帮助文本。
const helpText = `Orca — run an agent task in this workspace

Usage:
  orca [options] PROMPT

Options (before PROMPT):
  -p, --provider NAME   Must be paired with -m
  -m, --model NAME      Must be paired with -p
  -mode NAME            ask/code/plan/agent/review/test/terminal/deliberate
  -f, --file PATH       Attach file; repeatable
  -sp, -s FILE          Override system prompt from file
  -h, --help

When the agent needs an answer or an approval, orca asks on stdin.
History is appended to .orca/history.jsonl, one JSON object per line.
`

// PrintHelp 向标准输出打印帮助信息。
func PrintHelp() { fmt.Fprint(os.Stdout, helpText) }
