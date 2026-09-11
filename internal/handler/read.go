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
func (t ReadHandler) Handle(cmd command.CommandOption) (error, any) {
	readOption, ok := cmd.(*ReadOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandRead), nil
	}

	var shell = t.getShell(readOption)
	t.Publisher.Publish(event.NewToolBeforeEvent(shell, readOption.Reasoning, readOption.Id))
	content, err := t.read(readOption)
	t.Publisher.Publish(event.NewToolAfterEvent(shell, content, readOption.Id))

	if err != nil {
		return err, CommandResult{}
	}

	return nil, CommandResult{
		Content:    content,
		Id:         readOption.Id,
		Command:    fmt.Sprintf("read %s ", readOption.Filename),
		OK:         true,
		Err:        "",
		TaskTarget: make([]TaskBaseInfo, 0),
	}
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

	for number := start; number <= end; number++ {
		if opt.SetNumber {
			fmt.Fprintf(&out, "%d\t%s\n", number, lines[number-1])
		} else {
			fmt.Fprintf(&out, "%s\n", lines[number-1])
		}
	}

	out.WriteString(truncated)
	return out.String(), nil
}

func (t ReadHandler) getShell(opt *ReadOption) string {
	var cmd = fmt.Sprintf("read %s", opt.Filename)
	if opt.SetNumber {

		cmd = fmt.Sprintf("%s-%d:%d", cmd, opt.Start, opt.End)
	}

	return cmd
}
