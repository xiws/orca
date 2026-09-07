package main

import (
	"flag"
	"fmt"
	"os"
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
}

func ParseArgs() (*CliArgs, error) {
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
	}, nil
}
