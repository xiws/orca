package handler

import (
	"fmt"
	"orca/internal/event"
	"strings"

	"orca/pkg/command"
)

// MaxReadLines caps how many lines one read command returns. Larger ranges are
// cut short and the result tells the caller how to continue.
const MaxReadLines = 2000

// ReadHandler serves the read command.
type ReadHandler struct {
	Workspace
}

// Handle returns the requested line range of a file, prefixed by line numbers.
//
// A file that cannot be read — missing, outside the workspace, with a range
// beyond its end — is a failed CommandResult, not a Go error. The model has to
// be able to read the reason and try another path; aborting the whole task on a
// guess that did not pan out is what a failed read used to do.
func (t ReadHandler) Handle(cmd command.CommandOption) (error, any) {
	readOption, ok := cmd.(*ReadOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandRead), nil
	}

	var shell = t.getShell(readOption)
	publish(t.Publisher, event.NewToolBeforeEvent(shell, readOption.Reasoning, readOption.Id))
	content, err := t.read(readOption)
	publish(t.Publisher, event.NewToolAfterEvent(shell, content, readOption.Id))

	return nil, ResultFor(readOption, content, err)
}

func (t ReadHandler) read(opt *ReadOption) (string, error) {
	content, err := readAll(t.Workspace, opt.Filename)
	if err != nil {
		return "", err
	}

	lines := splitLines(content)
	if len(lines) == 0 {
		return "(empty file)", nil
	}

	start, end := opt.Start, opt.End
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}

	if start > len(lines) {
		return "", fmt.Errorf("start line %d is beyond the end of the file (%d lines)", start, len(lines))
	}

	if start > end {
		return "", fmt.Errorf("start line %d is after end line %d", start, end)
	}

	var truncated string
	if end-start+1 > MaxReadLines {
		last := start + MaxReadLines - 1
		truncated = fmt.Sprintf(
			"... stopped after %d lines, continue with \"start\": %d, \"end\": %d",
			MaxReadLines,
			last+1,
			end,
		)
		end = last
	}

	var out strings.Builder

	// Every line carries its 1 based number, which the tool description promises
	// the model and which the model needs to talk about a range it just read.
	for number := start; number <= end; number++ {
		fmt.Fprintf(&out, "%d\t%s\n", number, lines[number-1])
	}

	out.WriteString(truncated)
	return out.String(), nil
}

func (t ReadHandler) getShell(opt *ReadOption) string {
	var cmd = fmt.Sprintf("read %s", opt.Filename)
	if opt.Start > 0 || opt.End > 0 {
		cmd = fmt.Sprintf("%s-%d:%d", cmd, opt.Start, opt.End)
	}

	return cmd
}
