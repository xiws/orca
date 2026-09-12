package command

import (
	"errors"
	"sync"
	"testing"

	"github.com/xiws/orca/pkg/utils"
)

// testCommand is a CommandOption the registry can route; the name is a constant,
// so even a zero value resolves, and the id correlates a call with its result.
type testCommand struct {
	Id int64
}

func (t testCommand) GetId() int64  { return t.Id }
func (testCommand) GetName() string { return "test" }

func newTestCommand() testCommand {
	return testCommand{Id: utils.GetSnowFlakeId()}
}

// missingCommand is never registered, so dispatching it must fail.
type missingCommand struct {
	Id int64
}

func (t missingCommand) GetId() int64  { return t.Id }
func (missingCommand) GetName() string { return "missing" }

func newMissingCommand() missingCommand {
	return missingCommand{Id: utils.GetSnowFlakeId()}
}

type testCommandHandler struct {
	err   error
	value any
}

func (h testCommandHandler) Handle(CommandOption) (error, any) {
	return h.err, h.value
}

func TestCommandHandleExecute(t *testing.T) {
	handle := NewCommandHandle()
	command := newTestCommand()
	wantErr := errors.New("handler failed")
	wantValue := "result"
	if err := handle.Register(command, testCommandHandler{err: wantErr, value: wantValue}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	gotErr, gotValue := handle.Execute(command)
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("Execute() error = %v, want %v", gotErr, wantErr)
	}
	if gotValue != wantValue {
		t.Fatalf("Execute() value = %v, want %v", gotValue, wantValue)
	}
}

func TestCommandHandleRejectsInvalidRegistrationAndExecution(t *testing.T) {
	handle := NewCommandHandle()
	option := newTestCommand()
	handler := testCommandHandler{}

	if err := handle.Register(nil, handler); !errors.Is(err, ErrNilCommandOption) {
		t.Fatalf("nil option error = %v", err)
	}
	if err := handle.Register(option, nil); !errors.Is(err, ErrNilCommandHandler) {
		t.Fatalf("nil handler error = %v", err)
	}
	if err := handle.Register(option, handler); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if err := handle.Register(option, handler); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("duplicate Register() error = %v", err)
	}
	if err, _ := handle.Execute(newMissingCommand()); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("unknown Execute() error = %v", err)
	}
	if err, _ := handle.Execute(nil); !errors.Is(err, ErrNilCommandParam) {
		t.Fatalf("nil Execute() error = %v", err)
	}
}

func TestCommandHandleConcurrentExecute(t *testing.T) {
	handle := NewCommandHandle()
	command := newTestCommand()
	if err := handle.Register(command, testCommandHandler{value: true}); err != nil {
		t.Fatal(err)
	}

	const calls = 100
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			if err, value := handle.Execute(command); err != nil || value != true {
				t.Errorf("Execute() = (%v, %v), want (nil, true)", err, value)
			}
		}()
	}
	wg.Wait()
}
