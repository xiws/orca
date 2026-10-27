package handler

import (
	"fmt"
	"github.com/xiws/orca/internal/event"
	"strings"

	"github.com/xiws/orca/pkg/command"
)

// MaxReadLines 限制单个 read 命令返回的行数。更大的范围会被截断，
// 结果会告诉调用方如何继续。
const MaxReadLines = 2000

// ReadHandler 服务 read 命令。
type ReadHandler struct {
	Workspace
}

// Handle 返回文件的请求行范围，前缀带行号。
//
// 无法读取的文件——缺失、在工作区外、范围超出末尾——是失败的
// CommandResult，而非 Go error。模型必须能读取原因并尝试其他路径；
// 因猜测失败而中止整个任务是过去 read 的做法。
func (t ReadHandler) Handle(cmd command.CommandOption) (error, any) {
	readOption, ok := cmd.(*ReadOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandRead), nil
	}

	meta := t.buildMeta(readOption)
	publish(t.Publisher, event.NewToolBeforeEvent("read", readOption.Filename, meta, readOption.Reasoning, readOption.Id))
	content, err := t.read(readOption)
	okStatus := err == nil
	// After-event 携带读取到的内容，便于追踪和调试。
	publish(t.Publisher, event.NewToolAfterEvent("read", readOption.Filename, content, okStatus, meta, 0, readOption.Id))

	return nil, ResultFor(readOption, content, err)
}

// read 读取文件并按行范围截取，每行添加行号前缀。
func (t ReadHandler) read(opt *ReadOption) (string, error) {
	content, err := readAll(t.Workspace, opt.Filename)
	if err != nil {
		return "", err
	}

	lines := splitLines(content)
	if len(lines) == 0 {
		return "(empty file)", nil
	}

	// 将 0 或负数范围视为开放范围，默认读取整个文件。
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

	// 超过 MaxReadLines 行时截断，并提示调用方如何继续读取。
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
		fmt.Fprintf(&out, "%d\t%s\n", number, lines[number-1])
	}

	out.WriteString(truncated)
	return out.String(), nil
}

// buildMeta 返回 read 操作的上下文描述。
func (t ReadHandler) buildMeta(opt *ReadOption) string {
	if opt.Start > 0 || opt.End > 0 {
		return fmt.Sprintf("lines %d-%d", opt.Start, opt.End)
	}
	return "entire file"
}
