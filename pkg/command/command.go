// Package command provides synchronous command registration and dispatch.
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

// CommandOption describes a command registration and execution parameter.
type CommandOption interface {
	GetId() int64
	GetName() string
}

// Command executes a command parameter and returns its result.
type Command interface {
	Execute(cmd CommandOption) (error, any)
}

// CommandHandler handles a command parameter and returns its result.
type CommandHandler interface {
	Handle(cmd CommandOption) (error, any)
}

// CommandHandle stores command handlers keyed by command name.
type CommandHandle struct {
	mu       sync.RWMutex
	handlers map[string]CommandHandler
}

// NewCommandHandle creates an empty command registry.
func NewCommandHandle() *CommandHandle {
	return &CommandHandle{handlers: make(map[string]CommandHandler)}
}

// Register adds a handler for the command option's name.
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

// Execute dispatches a command synchronously to the handler registered for its name.
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
