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

var ErrHelpRequested = errors.New("help requested")

type StringSlice []string

func (s *StringSlice) String() string         { return strings.Join(*s, ",") }
func (s *StringSlice) Set(value string) error { *s = append(*s, value); return nil }

type CliArgs struct {
	Files        []string
	SystemPrompt string
	Model        string
	Provider     string
	Mode         string
	Prompt       string
	SessionId    int64
	SubCommand   string
	SubArgs      []string
	After        int64
}

func ParseArgs() (*CliArgs, error) { return parseArgs(os.Args[1:], os.Stdout) }

func parseArgs(argv []string, out io.Writer) (*CliArgs, error) {
	if len(argv) > 0 && (argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help") {
		if len(argv) != 1 {
			return nil, fmt.Errorf("unexpected help arguments")
		}
		fmt.Fprint(out, helpText)
		return nil, ErrHelpRequested
	}
	if len(argv) > 0 && (argv[0] == "run" || argv[0] == "session" || argv[0] == "task") {
		a := &CliArgs{SubCommand: argv[0], SubArgs: append([]string(nil), argv[1:]...)}
		if err := validateCommand(a); err != nil {
			return nil, err
		}
		return a, nil
	}
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
	if err := validateSubmitArgs(a); err != nil {
		return nil, err
	}
	return a, nil
}

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

func validateMode(name string) error {
	switch name {
	case "", "ask", "code", "plan", "agent", "review", "test", "terminal", "deliberate":
		return nil
	default:
		return fmt.Errorf("unknown mode %q", name)
	}
}

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

func PrintHelp() { fmt.Fprint(os.Stdout, helpText) }
