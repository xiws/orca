package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionAndTaskHaveIndependentIdentity(t *testing.T) {
	t.Setenv("WORKSPACE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	session := NewSession("/explicit/project")
	first := NewTask("first goal", "first", session.ID)
	second := NewTask("second goal", "second", session.ID)
	standalone := NewTask("background goal", "background", 0)
	if first.ID == second.ID || int64(first.ID) == int64(session.ID) || int64(second.ID) == int64(session.ID) {
		t.Fatal("task identities must not reuse a session identity")
	}
	if first.SessionID != session.ID || second.SessionID != session.ID || standalone.SessionID != 0 {
		t.Fatal("explicit task/session association was lost")
	}
	if session.ProjectPath != "/explicit/project" || len(session.Messages) != 0 {
		t.Fatalf("constructor inferred ambient state: %+v", session)
	}
	session.AppendUser(first.ID, first.Input)
	session.AppendAssistant(first.ID, "first result")
	session.AppendUser(second.ID, second.Input)
	if len(session.Messages) != 3 || session.Messages[0].TaskID != first.ID || session.Messages[2].TaskID != second.ID {
		t.Fatalf("timeline = %+v", session.Messages)
	}
	for _, message := range session.Messages {
		if message.ID == 0 || message.CreateTime == 0 {
			t.Fatalf("message identity missing: %+v", message)
		}
	}
}

func TestBusinessEntitiesDoNotContainExecutionState(t *testing.T) {
	for _, value := range []any{Task{}, Session{}} {
		typ := reflect.TypeOf(value)
		for _, field := range []string{"SessionInfo", "Provider", "OtterState", "TotalUsage", "TaskResult", "WorkflowID", "RetryCount", "MaxRetries"} {
			if _, exists := typ.FieldByName(field); exists {
				t.Errorf("%s still owns %s", typ.Name(), field)
			}
		}
	}
	if _, exists := reflect.TypeOf(Task{}).FieldByName("Messages"); exists {
		t.Error("Task owns model messages")
	}
}

func TestSessionRoundTripPreservesTaskReferences(t *testing.T) {
	session := NewSession("/project")
	task := NewTask("goal", "title", session.ID)
	session.AppendUser(task.ID, task.Input)
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var restored Session
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*session, restored) {
		t.Fatalf("restored = %+v", restored)
	}
}
