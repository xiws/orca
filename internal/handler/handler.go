// Package handler 实现 agent 可调用的工具命令：read、write、edit 和 bash。
// 每个处理器都满足 command.CommandHandler 接口，并通过 CommandResult
// 报告业务结果，将注册表的 error 通道留给框架级故障。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xiws/orca/pkg/utils"

	"github.com/xiws/orca/pkg/command"
)

// 内置工具命令的名称。它们是命令注册表使用的路由名称，
// 也是线路协议中 "command" 字段的值。
const (
	CommandRead       = "read"
	CommandWrite      = "write"
	CommandEdit       = "edit"
	CommandBash       = "bash"
	CommandCreateTask = "create_task"
)

var (
	// ErrUnsupportedOption 在处理器被分发到无法处理的 option 类型时返回。
	ErrUnsupportedOption = errors.New("unsupported command option")
	// ErrEmptyFilename 在文件命令未携带文件名时返回。
	ErrEmptyFilename = errors.New("filename is required")
	// ErrEmptyCommand 在 bash 命令未携带命令行时返回。
	ErrEmptyCommand = errors.New("command is required")
	// ErrEmptyContents 在 edit 命令没有片段时返回。
	ErrEmptyContents = errors.New("edit contents is empty")
	// ErrOutsideWorkspace 在路径解析到允许的根目录之外时返回。
	ErrOutsideWorkspace = errors.New("path is outside the workspace")
	// ErrFragmentLocator 在 edit 片段未携带 diff 时返回。
	ErrFragmentLocator = errors.New("fragment needs a diff")
)

// CommandResult 是命令执行的统一结果。它是 CommandHandler.Handle
// 第二个返回值位置的值，会被序列化回模型，因此失败是数据而非 Go error。
type CommandResult struct {
	// Id 将结果与产生它的调用关联，匹配调用携带的 id。
	Id      int64  `json:"id"`
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	// Content 携带命令负载：read 的文件内容、bash 的合并 stdout 和 stderr、
	// write 和 edit 的变更摘要。
	Content string `json:"content,omitempty"`
	// Err 持有简短原因，OK 为 true 时为空。
	Err        string         `json:"err,omitempty"`
	TaskTarget []TaskBaseInfo `json:"task_target,omitempty"`
}

type TaskBaseInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (u CommandResult) String() string {
	if u.OK {
		return fmt.Sprintf("task id:%d \ncommand:%s\nresult:%s", u.Id, u.Command, u.Content)
	}
	return fmt.Sprintf("task id:%d \ncommand:%s\nfailed:%s", u.Id, u.Command, u.Err)
}

// JSON 以结果传递到模型的方式渲染：每个工具调用一个结构化对象，
// 携带其身份、结果，以及失败时的原因。模型将其作为数据读取，
// 可以处理 ok:false 而无需运行时将业务失败视为致命错误。
func (u CommandResult) JSON() string {
	data, err := json.Marshal(u)
	if err != nil {
		// 结构体只持有纯值，实际上不可能失败。
		return fmt.Sprintf(`{"ok":false,"command":%q,"err":%q}`, u.Command, err.Error())
	}
	return string(data)
}

// NewResult 为给定身份构建 CommandResult。非 nil 的 err 将结果标记为失败，
// 并将其消息放入 Err。
func NewResult(id int64, name, content string, err error) CommandResult {
	result := CommandResult{Id: id, Command: name, Content: content, OK: err == nil}
	if err != nil {
		result.Err = err.Error()
	}
	return result
}

// ResultFor 为命令选项构建 CommandResult，因此处理器只需产生负载和错误。
// 在代码中构建的没有 id 的选项仍会获得一个，保持每个结果可与调用关联。
func ResultFor(cmd command.CommandOption, content string, err error) CommandResult {
	id := cmd.GetId()
	if id == 0 {
		id = utils.GetSnowFlakeId()
	}
	return NewResult(id, cmd.GetName(), content, err)
}

// Register 为每个内置工具命令向 handle 添加处理器，将文件访问限定在 ws 范围内。
func Register(handle *command.CommandHandle, ws Workspace) error {
	entries := []struct {
		option  command.CommandOption
		handler command.CommandHandler
	}{
		{&ReadOption{}, ReadHandler{Workspace: ws}},
		{&WriteOption{}, WriteHandler{Workspace: ws}},
		{&EditOption{}, EditHandler{Workspace: ws}},
		{&BashOption{}, BashHandler{Workspace: ws}},
		{&CreateTaskOption{}, &CreateTaskHandler{Workspace: ws}},
	}

	for _, entry := range entries {
		if err := handle.Register(entry.option, entry.handler); err != nil {
			return err
		}
	}
	return nil
}
