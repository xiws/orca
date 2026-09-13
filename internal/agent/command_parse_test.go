package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/xiws/orca/internal/llm"
)

// assertCall checks one parsed call's name and arguments. Arguments are
// compared as decoded JSON, since key order in the marshalled object is an
// implementation detail.
func assertCall(t *testing.T, call llm.ToolCall, name, arguments string) {
	t.Helper()
	if call.Name != name {
		t.Errorf("call name = %q, want %q", call.Name, name)
	}
	var got, want any
	if err := json.Unmarshal([]byte(call.Arguments), &got); err != nil {
		t.Fatalf("call arguments %q are not JSON: %v", call.Arguments, err)
	}
	if err := json.Unmarshal([]byte(arguments), &want); err != nil {
		t.Fatalf("want arguments %q are not JSON: %v", arguments, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("call arguments = %s, want %s", call.Arguments, arguments)
	}
}

// TestParseTextCallsPlainObject covers the documented shape: a command object
// surrounded by the model's prose, followed by nothing else.
func TestParseTextCallsPlainObject(t *testing.T) {
	content := "I will look at the file now.\n" +
		`{"command": "read", "id": 3, "filename": "a.md", "start": 1}`
	calls := ParseTextCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parsed %d calls, want 1: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "read", `{"id": 3, "filename": "a.md", "start": 1}`)
}

// TestParseTextCallsFencedArray covers several commands in one fenced JSON
// block, the other shape ToolSchema documents.
func TestParseTextCallsFencedArray(t *testing.T) {
	content := "Here is what I will run:\n```json\n[\n" +
		`{"command":"read","data":{"filename":"a.md"}},` + "\n" +
		`{"command":"bash","data":{"content":"ls"}}` + "\n```\n"
	calls := ParseTextCalls(content)
	if len(calls) != 2 {
		t.Fatalf("parsed %d calls, want 2: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "read", `{"data":{"filename":"a.md"}}`)
	assertCall(t, calls[1], "bash", `{"data":{"content":"ls"}}`)
}

// TestParseTextCallsBraceInsideArgument checks the scanner honours string
// literals: braces inside a content argument must not cut the command short.
func TestParseTextCallsBraceInsideArgument(t *testing.T) {
	content := `{"command":"write","data":{"filename":"main.go","content":"func main() {}"}}`
	calls := ParseTextCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parsed %d calls, want 1: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "write", `{"data":{"filename":"main.go","content":"func main() {}"}}`)
}

// TestParseTextCallsNormalizesCommandName mirrors Parse: the name is matched
// case-insensitively and travels lowercased.
func TestParseTextCallsNormalizesCommandName(t *testing.T) {
	content := `{"command": " READ ", "filename": "a.md"}`
	calls := ParseTextCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parsed %d calls, want 1: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "read", `{"filename":"a.md"}`)
}

// TestParseTextCallsIgnoresNonCommands keeps prose and examples safe: only
// objects naming a known command count, and a malformed one is skipped rather
// than failing the whole scan.
func TestParseTextCallsIgnoresNonCommands(t *testing.T) {
	content := "An envelope looks like this:\n" +
		`{"command": "teleport", "target": "moon"}` + "\n" +
		"settings can also appear: " + `{"timeout": 5}` + "\n" +
		"and this one is broken: " + `{"command": "read", "filename": }`
	calls := ParseTextCalls(content)
	if len(calls) != 0 {
		t.Fatalf("parsed %d calls, want none: %+v", len(calls), calls)
	}
}

// TestParseTextCallsSkipsNestedExamples makes sure a command object nested
// inside a larger JSON value is treated as an example, not as a call: the
// outer value is scanned once and the inner one never separately.
func TestParseTextCallsSkipsNestedExamples(t *testing.T) {
	content := `{"note": "the protocol in one line", "example": {"command": "bash", "data": {"content": "rm -rf /"}}}`
	calls := ParseTextCalls(content)
	if len(calls) != 0 {
		t.Fatalf("parsed %d calls, want none: %+v", len(calls), calls)
	}
}
