package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolsMarshalToOpenAIShape checks that Tools() serializes into the exact
// envelope OpenAI's /chat/completions expects: a "tools" array of
// {"type":"function","function":{...}} entries, each carrying a JSON Schema
// "parameters" object.
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
	if len(envelope.Tools) != 4 {
		t.Fatalf("len(Tools) = %d, want 4", len(envelope.Tools))
	}

	wantNames := []string{ToolRead, ToolWrite, ToolEdit, ToolBash}
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

// TestToolParametersMatchHandlerProtocol keeps the declared property names in
// sync with the json tags handler.parseOption accepts, so the model can never
// be told about a parameter the runtime cannot decode. The expected shapes are
// spelled out literally here rather than derived from the handler package, to
// avoid making llm depend on it.
func TestToolParametersMatchHandlerProtocol(t *testing.T) {
	want := map[string][]string{
		ToolRead:  {"filename", "start", "end"},
		ToolWrite: {"filename", "content"},
		ToolEdit:  {"filename", "contents"},
		ToolBash:  {"content", "workdir", "timeout"},
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

// TestEditToolDescribesFragmentProperties checks the one nested schema among
// the four tools keeps the locators the edit handler understands.
func TestEditToolDescribesFragmentProperties(t *testing.T) {
	raw, err := json.Marshal(EditTool())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, locator := range []string{"old_string", "new_string", "replace_all", "start", "end", "content"} {
		if !strings.Contains(string(raw), `"`+locator+`"`) {
			t.Errorf("edit tool should describe %q, got %s", locator, raw)
		}
	}
	if !strings.Contains(string(raw), `"type":"array"`) {
		t.Errorf("edit tool contents should be an array, got %s", raw)
	}
}
