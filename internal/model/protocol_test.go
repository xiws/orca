package model

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testTools() []Tool {
	return []Tool{{Name: "read", Description: "gateway read", Parameters: json.RawMessage(`{"type":"object","properties":{"filename":{"type":"string"}}}`)}, {Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}}
}

func TestParseTextCallsExplicitDocuments(t *testing.T) {
	for _, tc := range []struct{ name, input, args, id string }{
		{"legacy flat", `{"command":" READ ","filename":"a.go","id":7}`, `{"filename":"a.go"}`, "7"},
		{"legacy data", `[{"command":"read","data":{"filename":"a.go","id":"nested"}}]`, `{"filename":"a.go"}`, "nested"},
		{"outer id", `{"command":"read","id":"outer","data":{"id":8,"filename":"a.go"}}`, `{"filename":"a.go"}`, "outer"},
		{"marker", `<tool_calls>[{"name":"read","arguments":{"filename":"a.go"}}]</tool_calls>`, `{"filename":"a.go"}`, ""},
		{"native arguments", `{"name":"read","arguments":"{\"filename\":\"a.go\"}"}`, `{"filename":"a.go"}`, ""},
		{"json envelope", `{"tool_calls":[{"type":"function","function":{"name":"read","arguments":"{\"filename\":\"a.go\"}"}}]}`, `{"filename":"a.go"}`, ""},
		{"xml", `<function_calls><invoke name="read"><parameter name="filename" string="true">a.go</parameter></invoke></function_calls>`, `{"filename":"a.go"}`, ""},
		{"deepseek", `<｜DSML｜function_calls><｜DSML｜invoke name="read" id="ds"><｜DSML｜parameter name="filename" string="true">a.go</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜function_calls>`, `{"filename":"a.go"}`, "ds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := ParseTextCalls(tc.input, testTools())
			if err != nil || len(calls) != 1 {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
			if calls[0].Name != "read" || calls[0].Arguments != tc.args || calls[0].ID == "" {
				t.Fatalf("unexpected call: %+v", calls[0])
			}
			if tc.id != "" && calls[0].ID != tc.id {
				t.Fatalf("id=%q", calls[0].ID)
			}
			again, err := ParseTextCalls(tc.input, testTools())
			if err != nil || !reflect.DeepEqual(again, calls) {
				t.Fatal("IDs not deterministic")
			}
		})
	}
}

func TestParseDoesNotExecuteExamples(t *testing.T) {
	for _, text := range []string{
		"hello", `The command is {"command":"bash","content":"danger"}`,
		"```json\n{\"command\":\"read\",\"filename\":\"a\"}\n```",
		`> <tool_calls>[{"name":"bash","arguments":{}}]</tool_calls>`,
		`"{\"command\":\"bash\",\"content\":\"danger\"}"`,
		`{"example":{"command":"bash","content":"danger"}}`,
		`[{"example":{"command":"bash"}}]`,
		`["task one","task two"]`, `{"name":"Jane"}`, `{"value":"command"}`,
		`An example: <function_calls><invoke name="read"></invoke></function_calls>`,
	} {
		calls, err := ParseTextCalls(text, testTools())
		if err != nil || len(calls) != 0 {
			t.Errorf("executed ordinary text %q: %v %v", text, calls, err)
		}
	}
}

func TestExplicitMalformedCallsFailAtomically(t *testing.T) {
	for _, text := range []string{
		`<tool_calls>[{"name":"read","arguments":{}}]`,
		`<tool_calls>[]</tool_calls>`, `<tool_calls>{"command":"read"}</tool_calls>`,
		`<tool_calls>[{"name":"read","arguments":{}}]</tool_calls> prose`,
		`{"command":`, `{"command":"read","data":null}`, `{"command":"read","data":{},"filename":"mixed"}`,
		`{"command":"read","data":{}} {"command":"bash","data":{}}`,
		`{"command":"read","command":"bash"}`, `{"command":"read","data":{"x":1,"x":2}}`,
		`[{"command":"read","data":{}},{"command":"not-declared","data":{}}]`,
		`[{"command":"read","data":{}},"example"]`,
		`{"name":"read","arguments":[]}`, `{"name":"read","arguments":"{"}`,
		`{"tool_calls":[],"example":true}`, `{"command":"read","id":null}`, `{"command":"read","id":1.5}`,
		`<function_calls><invoke name="read"></function_calls>`,
		`<function_calls><invoke name="read"><parameter name="x" string="false">{</parameter></invoke></function_calls>`,
		`<function_calls><invoke name="read"><parameter name="x" string="true">a</parameter><parameter name="x" string="true">b</parameter></invoke></function_calls>`,
		`<function_calls><invoke name="read"><unknown/></invoke></function_calls>`,
		`<function_calls><invoke name="read" name="bash"/></function_calls>`,
		`<function_calls><!--example--><invoke name="read"/></function_calls>`,
		`<function_calls><invoke name="read"/></function_calls><function_calls/>`,
		`<｜DSML｜function_calls><｜DSML｜invoke name="read">`,
	} {
		calls, err := ParseTextCalls(text, testTools())
		if !errors.Is(err, ErrToolProtocol) || len(calls) != 0 {
			t.Errorf("accepted malformed %q: %v %v", text, calls, err)
		}
	}
}

func TestDeepSeekTypedParameters(t *testing.T) {
	text := `<｜DSML｜function_calls><｜DSML｜invoke name="bash"><｜DSML｜parameter name="content" string="true">a &lt; b &amp; c</｜DSML｜parameter><｜DSML｜parameter name="timeout" string="false">12</｜DSML｜parameter><｜DSML｜parameter name="nested" string="false">{"values":[true,1]}</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜function_calls>`
	calls, err := ParseTextCalls(text, testTools())
	if err != nil {
		t.Fatal(err)
	}
	if calls[0].Arguments != `{"content":"a \u003c b \u0026 c","nested":{"values":[true,1]},"timeout":12}` {
		t.Fatal(calls[0].Arguments)
	}
}

func TestNormalizeCallsAndGatewayDeclarations(t *testing.T) {
	calls := []Call{{Name: "read", Arguments: `{}`}, {Name: "read", Arguments: `{}`}}
	normalized, err := NormalizeCalls(calls, testTools())
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].ID == normalized[1].ID || calls[0].ID != "" {
		t.Fatal("IDs not unique or input mutated")
	}
	normalized[1].ID = normalized[0].ID
	if _, err := NormalizeCalls(normalized, testTools()); !errors.Is(err, ErrToolProtocol) {
		t.Fatal("duplicate IDs accepted")
	}
	if _, err := ParseTextCalls(`{"command":"read","data":{}}`, nil); !errors.Is(err, ErrToolProtocol) {
		t.Fatal("undeclared tool accepted")
	}
	prompt, err := ToolPrompt([]Tool{{Name: "custom_gateway", Description: "only this declaration", Parameters: json.RawMessage(`{"type":"object","properties":{"unique":{"type":"boolean"}}}`)}})
	if err != nil || !strings.Contains(prompt, "custom_gateway") || !strings.Contains(prompt, "unique") || strings.Contains(prompt, `"bash"`) {
		t.Fatalf("prompt=%s err=%v", prompt, err)
	}
	if _, err := ToolPrompt([]Tool{{Name: "bad", Parameters: json.RawMessage(`null`)}}); !errors.Is(err, ErrToolProtocol) {
		t.Fatal("bad declaration accepted")
	}
}

func FuzzProtocolDoesNotParseProse(f *testing.F) {
	f.Add(`{"command":"read","data":{}}`)
	f.Fuzz(func(t *testing.T, value string) {
		calls, err := ParseTextCalls("This is an example: "+value, testTools())
		if len(calls) != 0 || err != nil {
			t.Fatal("parsed prose")
		}
	})
}
