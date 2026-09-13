package handler

import (
	"fmt"
	"github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/pkg/command"
)

// WriteHandler 服务 write 命令。
type WriteHandler struct {
	Workspace
}

// Handle 从头重建文件。不属于 Content 的部分会丢失，
// 这就是编辑已有文件应优先使用 edit 命令的原因。
func (t WriteHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*WriteOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandWrite), nil
	}

	meta := fmt.Sprintf("%d bytes", len(opt.Content))
	publish(t.Publisher, event.NewToolBeforeEvent("write", opt.Filename, meta, opt.Reasoning, opt.Id))
	summary, err := t.write(opt)
	okStatus := err == nil
	publish(t.Publisher, event.NewToolAfterEvent("write", opt.Filename, "", okStatus, summary, 0, opt.Id))

	return nil, ResultFor(opt, summary, err)
}

func (t WriteHandler) write(opt *WriteOption) (string, error) {
	resolved, err := writeAll(t.Workspace, opt.Filename, opt.Content)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(opt.Content), resolved), nil
}
