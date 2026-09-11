package handler

import (
	"fmt"
	"orca/internal/event"
	"orca/pkg/command"
)

// WriteHandler serves the write command.
type WriteHandler struct {
	Workspace
}

// Handle rebuilds a file from scratch. Anything that is not part of Content is
// lost, which is why editing an existing file should prefer the edit command.
func (t WriteHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*WriteOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandWrite), nil
	}

	shell := fmt.Sprintf("write %s %s", opt.Filename, opt.Content)
	publish(t.Publisher, event.NewToolBeforeEvent(shell, opt.Reasoning, opt.Id))
	summary, err := t.write(opt)
	publish(t.Publisher, event.NewToolAfterEvent(shell, summary, opt.Id))

	return nil, ResultFor(opt, summary, err)
}

func (t WriteHandler) write(opt *WriteOption) (string, error) {
	resolved, err := writeAll(t.Workspace, opt.Filename, opt.Content)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(opt.Content), resolved), nil
}
