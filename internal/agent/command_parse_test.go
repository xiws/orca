package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/xiws/orca/internal/llm"
)

// assertCall 检查一个解析后调用的 name 和 arguments。参数以
// 解码后的 JSON 进行比较，因为序列化对象中的键顺序是实现细节。
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

// TestParseTextCallsPlainObject 覆盖文档化的形式：命令对象
// 周围是模型的散文，后面没有其他内容。
func TestParseTextCallsPlainObject(t *testing.T) {
	content := "I will look at the file now.\n" +
		`{"command": "read", "id": 3, "filename": "a.md", "start": 1}`
	calls := ParseTextCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parsed %d calls, want 1: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "read", `{"id": 3, "filename": "a.md", "start": 1}`)
}

// TestParseTextCallsFencedArray 覆盖一个围栏 JSON 块中的多个命令，
// 即 ToolSchema 文档记录的另一形式。
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

// TestParseTextCallsBraceInsideArgument 检查扫描器尊重字符串字面量：
// content 参数内的花括号不会截断命令。
func TestParseTextCallsBraceInsideArgument(t *testing.T) {
	content := `{"command":"write","data":{"filename":"main.go","content":"func main() {}"}}`
	calls := ParseTextCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parsed %d calls, want 1: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "write", `{"data":{"filename":"main.go","content":"func main() {}"}}`)
}

// TestParseTextCallsNormalizesCommandName 与 Parse 一致：名称以
// 不区分大小写的方式匹配，并以小写形式传递。
func TestParseTextCallsNormalizesCommandName(t *testing.T) {
	content := `{"command": " READ ", "filename": "a.md"}`
	calls := ParseTextCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parsed %d calls, want 1: %+v", len(calls), calls)
	}
	assertCall(t, calls[0], "read", `{"filename":"a.md"}`)
}

// TestParseTextCallsIgnoresNonCommands 保持散文和示例安全：只有
// 命名已知命令的对象才算，格式错误的对象会被跳过而不是让整个扫描失败。
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

// TestParseTextCallsSkipsNestedExamples 确保嵌套在更大 JSON 值内的
// 命令对象被视为示例而非调用：外层值只扫描一次，内层不会单独扫描。
func TestParseTextCallsSkipsNestedExamples(t *testing.T) {
	content := `{"note": "the protocol in one line", "example": {"command": "bash", "data": {"content": "rm -rf /"}}}`
	calls := ParseTextCalls(content)
	if len(calls) != 0 {
		t.Fatalf("parsed %d calls, want none: %+v", len(calls), calls)
	}
}
