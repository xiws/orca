// Package command 提供同步命令注册和分发。
package command

import (
	"errors"
	"reflect"
	"sync"
)

var (
	ErrNilCommandOption  = errors.New("command option is nil")
	ErrNilCommandHandler = errors.New("command handler is nil")
	ErrNilCommandParam   = errors.New("command parameter is nil")
	ErrDuplicateCommand  = errors.New("command name is already registered")
	ErrCommandNotFound   = errors.New("command name is not registered")
)

// CommandOption 描述命令的注册和执行参数。
type CommandOption interface {
	GetId() int64
	GetName() string
}

// Command 执行命令参数并返回其结果。
type Command interface {
	Execute(cmd CommandOption) (error, any)
}

// CommandHandler 处理命令参数并返回其结果。
type CommandHandler interface {
	Handle(cmd CommandOption) (error, any)
}

// CommandHandle 存储按命令名键控的命令处理器。
type CommandHandle struct {
	mu       sync.RWMutex
	handlers map[string]CommandHandler
}

// NewCommandHandle 创建一个空的命令注册表。
func NewCommandHandle() *CommandHandle {
	return &CommandHandle{handlers: make(map[string]CommandHandler)}
}

// Register 为命令选项的名称添加处理器。
func (t *CommandHandle) Register(option CommandOption, handler CommandHandler) error {
	if isNil(option) {
		return ErrNilCommandOption
	}
	if isNil(handler) {
		return ErrNilCommandHandler
	}
	name := option.GetName()
	if name == "" {
		return ErrNilCommandOption
	}

	if t.handlers == nil {
		t.handlers = make(map[string]CommandHandler)
	}
	if _, exists := t.handlers[name]; exists {
		return ErrDuplicateCommand
	}
	t.handlers[name] = handler
	return nil
}

// Execute 将命令同步分发到为其名称注册的处理器。
func (t *CommandHandle) Execute(cmd CommandOption) (error, any) {
	if isNil(cmd) {
		return ErrNilCommandParam, nil
	}
	name := cmd.GetName()
	if name == "" {
		return ErrNilCommandParam, nil
	}
	handler, exists := t.handlers[name]
	if !exists {
		return ErrCommandNotFound, nil
	}
	return handler.Handle(cmd)
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
