package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/modes"
	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
	"github.com/xiws/orca/internal/tool"
	"github.com/xiws/orca/pkg/utils"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "orca:", err)
		os.Exit(1)
	}
}

func run() error {
	args, err := ParseArgs()
	if errors.Is(err, ErrHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	if args.SubCommand == "session" {
		return HandleSessionCommand(args.SubArgs)
	}

	record, turn, err := prepareTurn(args)
	if err != nil {
		return err
	}
	if record.Legacy != nil {
		fmt.Fprintln(os.Stderr, "orca: 旧会话已保留归档；原始文件或 .legacy.json 备份可能含凭据，请勿公开。")
	}
	runtime := core.NewRuntime(core.WithWorkspace(record.Session.ProjectPath))
	defer runtime.Close()
	modeName := args.Mode
	if modeName == "" {
		modeName = "code"
	}
	result, err := app.Execute(record, turn, modes.For(modeName, runtime))
	if err != nil {
		return err
	}
	fmt.Println(result)
	return nil
}

func prepareTurn(args *CliArgs) (*session.Record, *app.Turn, error) {
	prompt, err := userPrompt(args)
	if err != nil {
		return nil, nil, err
	}
	var systemPrompt string
	if args.SystemPrompt != "" {
		data, err := os.ReadFile(args.SystemPrompt)
		if err != nil {
			return nil, nil, fmt.Errorf("read system prompt %s: %w", args.SystemPrompt, err)
		}
		systemPrompt = string(data)
	}

	var record *session.Record
	if args.SessionId > 0 {
		record, err = session.Load(args.SessionId)
		if err != nil {
			return nil, nil, fmt.Errorf("load session %d: %w", args.SessionId, err)
		}
	} else {
		record = &session.Record{Session: domain.NewSession(utils.GetCurrentPath())}
	}
	model := record.LastModel()
	if args.Model != "" && args.Provider != "" {
		model = session.ModelRef{Provider: args.Provider, ModelID: args.Model}
	}
	if model.Provider == "" || model.ModelID == "" {
		model = session.ModelRef{
			Provider: tool.Get(tool.KeyDefaultProvider),
			ModelID:  tool.Get(tool.KeyDefaultModel),
		}
	}
	provider := llm.GetProvider(model.Provider, model.ModelID)
	if provider.ModelID == "" {
		return nil, nil, fmt.Errorf("unknown model %s/%s", model.Provider, model.ModelID)
	}
	turn := app.Prepare(record, app.Request{
		Input: args.Prompt, Prompt: prompt, SystemPrompt: systemPrompt, Provider: provider,
	})
	return record, turn, nil
}

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
