package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolsMarshalToOpenAIShape 检查 Tools() 序列化为 OpenAI /chat/completions
// 期望的确切信封：一个 "tools" 数组，包含 {"type":"function","function":{...}} 条目，
// 每个携带 JSON Schema "parameters" 对象。
func TestToolsMarshalToOpenAIShape(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"tools": Tools()})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var envelope struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string `json:"name"`
				Parameters struct {
					Type       string                     `json:"type"`
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("Unmarshal() error = %v, payload: %s", err, raw)
	}
	if len(envelope.Tools) != 5 {
		t.Fatalf("len(Tools) = %d, want 5", len(envelope.Tools))
	}

	wantNames := []string{ToolRead, ToolWrite, ToolEdit, ToolBash, ToolCreateTask}
	for i, tool := range envelope.Tools {
		if tool.Type != "function" {
			t.Errorf("tool %d type = %q, want %q", i, tool.Type, "function")
		}
		if tool.Function.Name != wantNames[i] {
			t.Errorf("tool %d name = %q, want %q", i, tool.Function.Name, wantNames[i])
		}
		if tool.Function.Parameters.Type != "object" {
			t.Errorf("tool %q parameters type = %q, want object", tool.Function.Name, tool.Function.Parameters.Type)
		}
		if len(tool.Function.Parameters.Properties) == 0 {
			t.Errorf("tool %q has no parameters", tool.Function.Name)
		}
	}
}

// TestToolParametersMatchHandlerProtocol 保持声明的属性名与 handler.parseOption
// 接受的 json 标签同步，使模型永远不会被告知运行时无法解码的参数。
// 预期形式在此处明确写出，而不是从 handler 包派生，以避免 llm 依赖它。
func TestToolParametersMatchHandlerProtocol(t *testing.T) {
	want := map[string][]string{
		ToolRead:       {"filename", "start", "end"},
		ToolWrite:      {"filename", "content"},
		ToolEdit:       {"filename", "contents"},
		ToolBash:       {"content", "workdir", "timeout"},
		ToolCreateTask: {"task_target"},
	}
	for _, tool := range Tools() {
		properties, err := json.Marshal(tool.Function.Parameters.Properties)
		if err != nil {
			t.Fatalf("Marshal() properties of %q: %v", tool.Function.Name, err)
		}
		for _, name := range want[tool.Function.Name] {
			if !strings.Contains(string(properties), `"`+name+`"`) {
				t.Errorf("tool %q is missing parameter %q: %s", tool.Function.Name, name, properties)
			}
		}
	}
}

// TestEditToolDescribesFragmentProperties 检查四个工具中唯一的嵌套 schema
// 保持了 edit handler 理解的唯一定位器：基于内容匹配的统一 diff。
// start/end/content 曾经是第二个定位器，不得再次出现，
// 否则模型会发送运行时拒绝的片段。
func TestEditToolDescribesFragmentProperties(t *testing.T) {
	raw, err := json.Marshal(EditTool())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(raw), `"type":"array"`) {
		t.Errorf("edit tool contents should be an array, got %s", raw)
	}

	contents, ok := EditTool().Function.Parameters.Properties["contents"]
	if !ok || contents.Items == nil {
		t.Fatalf("edit tool contents should be an array with an item schema, got %s", raw)
	}
	properties := contents.Items.Properties
	if _, ok := properties["diff"]; !ok {
		t.Errorf("edit fragment should describe %q, got %s", "diff", raw)
	}
	for _, dropped := range []string{"start", "end", "content"} {
		if _, ok := properties[dropped]; ok {
			t.Errorf("edit fragment should no longer describe %q, got %s", dropped, raw)
		}
	}
}

// TestCreateTaskToolDescribesTaskTargetProperties 固定 task_target 的嵌套形式：
// 一个 {title, description} 对象数组，与 handler.TaskBaseInfo 的 json 标签匹配。
// 此处的偏差会使模型发送运行时无法解码的字符串，导致每次 create_task 调用失败。
func TestCreateTaskToolDescribesTaskTargetProperties(t *testing.T) {
	raw, err := json.Marshal(CreateTaskTool())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(raw), `"type":"array"`) {
		t.Errorf("create_task task_target should be an array, got %s", raw)
	}
	for _, field := range []string{"title", "description"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("create_task should describe sub-task field %q, got %s", field, raw)
		}
	}
}
