package handler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"orca/pkg/command"
)

func TestParseAcceptsFlatAndNestedParameters(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantName string
		check    func(t *testing.T, cmd command.CommandOption)
	}{
		{
			name:     "read flat",
			raw:      `{ "command": "read", "filename":"/home/xiw/test.md", "start":100, "end":101}`,
			wantName: CommandRead,
			check: func(t *testing.T, cmd command.CommandOption) {
				opt := cmd.(*ReadOption)
				if opt.Filename != "/home/xiw/test.md" || opt.Start != 100 || opt.End != 101 {
					t.Fatalf("read option = %+v", opt)
				}
			},
		},
		{
			name:     "read nested",
			raw:      `{ "command": "read", "data": {"filename": "a.md"} }`,
			wantName: CommandRead,
			check: func(t *testing.T, cmd command.CommandOption) {
				if opt := cmd.(*ReadOption); opt.Filename != "a.md" || opt.Start != 0 {
					t.Fatalf("read option = %+v", opt)
				}
			},
		},
		{
			name:     "write",
			raw:      `{ "command": "write", "data":{"filename":"/home/xiw/test.md", "content":"hello"} }`,
			wantName: CommandWrite,
			check: func(t *testing.T, cmd command.CommandOption) {
				opt := cmd.(*WriteOption)
				if opt.Filename != "/home/xiw/test.md" || opt.Content != "hello" {
					t.Fatalf("write option = %+v", opt)
				}
			},
		},
		{
			name:     "edit by line range",
			raw:      `{ "command": "edit", "data":{ "filename":"/home/xiw/test.md", "contents":[{"start":100,"end":101,"content":" modify content"}] } }`,
			wantName: CommandEdit,
			check: func(t *testing.T, cmd command.CommandOption) {
				opt := cmd.(*EditOption)
				if len(opt.Contents) != 1 {
					t.Fatalf("edit option = %+v", opt)
				}
				fragment := opt.Contents[0]
				if fragment.Start != 100 || fragment.End != 101 || fragment.Content != " modify content" {
					t.Fatalf("fragment = %+v", fragment)
				}
			},
		},
		{
			name:     "edit by diff",
			raw:      `{ "command": "edit", "data":{ "filename":"a.md", "contents":[{"diff":"@@\n- foo\n+ bar"}] } }`,
			wantName: CommandEdit,
			check: func(t *testing.T, cmd command.CommandOption) {
				fragment := cmd.(*EditOption).Contents[0]
				if fragment.Diff != "@@\n- foo\n+ bar" {
					t.Fatalf("fragment = %+v", fragment)
				}
			},
		},
		{
			name:     "bash flat",
			raw:      `{ "command": "bash", "content":"ls -a"}`,
			wantName: CommandBash,
			check: func(t *testing.T, cmd command.CommandOption) {
				if opt := cmd.(*BashOption); opt.Content != "ls -a" {
					t.Fatalf("bash option = %+v", opt)
				}
			},
		},
		{
			name:     "bash nested with workdir and timeout",
			raw:      `{ "command": "bash", "data": {"content":"ls -a", "workdir":"/home/xiw", "timeout":5} }`,
			wantName: CommandBash,
			check: func(t *testing.T, cmd command.CommandOption) {
				opt := cmd.(*BashOption)
				if opt.Workdir != "/home/xiw" || opt.Timeout != 5 {
					t.Fatalf("bash option = %+v", opt)
				}
			},
		},
		{
			name:     "name is case insensitive",
			raw:      `{ "command": " READ ", "filename": "a.md" }`,
			wantName: CommandRead,
			check:    func(t *testing.T, cmd command.CommandOption) {},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if cmd.GetName() != tc.wantName {
				t.Fatalf("GetName() = %q, want %q", cmd.GetName(), tc.wantName)
			}
			if cmd.GetId() == 0 {
				t.Fatal("Parse() left the id zero, want a generated one")
			}
			tc.check(t, cmd)
		})
	}
}

func TestParseKeepsProvidedIds(t *testing.T) {
	envelope, err := Parse([]byte(`{"command":"read","id":1,"filename":"a.md"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if envelope.GetId() != 1 {
		t.Fatalf("id = %d, want 1", envelope.GetId())
	}

	nested, err := Parse([]byte(`{"command":"write","data":{"id":2,"filename":"a.md","content":"x"}}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if nested.GetId() != 2 {
		t.Fatalf("id = %d, want 2", nested.GetId())
	}

	// The envelope id wins over one buried in the parameters.
	both, err := Parse([]byte(`{"command":"write","id":3,"data":{"id":4,"filename":"a.md"}}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if both.GetId() != 3 {
		t.Fatalf("id = %d, want 3", both.GetId())
	}
}

// TestParseNormalizesWireIds covers the shapes a model may spell an id with, and
// the ones it has to give up on.
func TestParseNormalizesWireIds(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		// want is the id to keep, or 0 to ask for a generated one.
		want int64
	}{
		{name: "number", raw: `{"command":"read","id":7,"filename":"a.md"}`, want: 7},
		{name: "quoted number", raw: `{"command":"read","id":"7","filename":"a.md"}`, want: 7},
		{name: "nested quoted number", raw: `{"command":"read","data":{"id":"8","filename":"a.md"}}`, want: 8},
		// A call id that is not a number cannot be carried, so it is replaced.
		{name: "opaque call id", raw: `{"command":"read","id":"call-1","filename":"a.md"}`, want: 0},
		{name: "id of the wrong type", raw: `{"command":"read","id":{"nested":"value"},"filename":"a.md"}`, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if tc.want == 0 {
				if cmd.GetId() == 0 {
					t.Fatal("Parse() kept an unusable id, want a generated one")
				}
				return
			}
			if cmd.GetId() != tc.want {
				t.Fatalf("id = %d, want %d", cmd.GetId(), tc.want)
			}
		})
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{name: "empty", raw: "  ", wantErr: ErrEmptyPayload},
		{name: "not json", raw: "read a.md", wantErr: ErrMalformedCommand},
		{name: "array", raw: `[{"command":"read"}]`, wantErr: ErrMalformedCommand},
		{name: "no name", raw: `{"filename":"a.md"}`, wantErr: ErrMalformedCommand},
		{name: "empty name", raw: `{"command":"  "}`, wantErr: ErrMalformedCommand},
		{name: "unknown", raw: `{"command":"delete","filename":"a.md"}`, wantErr: ErrUnknownCommand},
		{name: "wrong type", raw: `{"command":"read","filename":{"nested":"value"}}`, wantErr: ErrMalformedCommand},
	}
	for _, tc := range cases {
		if _, err := Parse([]byte(tc.raw)); !errors.Is(err, tc.wantErr) {
			t.Fatalf("Parse(%s) error = %v, want %v", tc.raw, err, tc.wantErr)
		}
	}
}

func TestParseAll(t *testing.T) {
	single, err := ParseAll([]byte(`{"command":"read","filename":"a.md"}`))
	if err != nil {
		t.Fatalf("ParseAll() error = %v", err)
	}
	if len(single) != 1 || single[0].GetName() != CommandRead {
		t.Fatalf("ParseAll() = %+v, want one read command", single)
	}

	many, err := ParseAll([]byte(`[
		{"command":"read","filename":"a.md"},
		{"command":"bash","data":{"content":"ls"}},
		{"command":"write","data":{"filename":"b.md","content":"x"}}
	]`))
	if err != nil {
		t.Fatalf("ParseAll() error = %v", err)
	}
	if len(many) != 3 {
		t.Fatalf("ParseAll() returned %d commands, want 3", len(many))
	}
	want := []string{CommandRead, CommandBash, CommandWrite}
	for i, name := range want {
		if many[i].GetName() != name {
			t.Fatalf("command %d = %q, want %q", i+1, many[i].GetName(), name)
		}
	}

	for _, raw := range []string{"", "[]", `[{"command":"read"},{"command":"nope"}]`} {
		if _, err := ParseAll([]byte(raw)); err == nil {
			t.Fatalf("ParseAll(%s) error = nil, want a failure", raw)
		}
	}
}

func TestOptionFromCall(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
		wantName  string
	}{
		{name: "flat", arguments: `{"filename":"a.md","start":3}`, wantName: CommandRead},
		{name: "nested", arguments: `{"data":{"filename":"a.md"}}`, wantName: CommandEdit},
		{name: "empty", arguments: ``, wantName: CommandBash},
		{name: "object", arguments: `{}`, wantName: CommandWrite},
	}
	for _, tc := range cases {
		cmd, err := OptionFromCall(9, tc.wantName, tc.arguments)
		if err != nil {
			t.Fatalf("OptionFromCall(%s) error = %v", tc.name, err)
		}
		if cmd.GetName() != tc.wantName {
			t.Fatalf("name = %q, want %q", cmd.GetName(), tc.wantName)
		}
		if cmd.GetId() != 9 {
			t.Fatalf("id = %d, want 9", cmd.GetId())
		}
	}

	// A call whose arguments are not an object cannot be turned into a command.
	if _, err := OptionFromCall(9, CommandRead, `not json`); !errors.Is(err, ErrMalformedCommand) {
		t.Fatalf("OptionFromCall() error = %v, want %v", err, ErrMalformedCommand)
	}
	// The name of the call decides, even when the payload disagrees.
	mismatched, err := OptionFromCall(9, CommandBash, `{"command":"read","content":"ls"}`)
	if err != nil {
		t.Fatalf("OptionFromCall() error = %v", err)
	}
	if mismatched.GetName() != CommandBash {
		t.Fatalf("name = %q, want %q", mismatched.GetName(), CommandBash)
	}
}

func TestCommandResultRoundTrips(t *testing.T) {
	result := NewResult(1, CommandRead, "1\thello\n", nil)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(raw), `"ok":true`) || strings.Contains(string(raw), `"err"`) {
		t.Fatalf("marshalled result = %s", raw)
	}

	failed := NewResult(2, CommandBash, "partial", errors.New("exit status 1"))
	raw, err = json.Marshal(failed)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded CommandResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.OK || decoded.Err != "exit status 1" || decoded.Content != "partial" {
		t.Fatalf("decoded result = %+v", decoded)
	}
}

func TestToolPromptCoversEveryCommand(t *testing.T) {
	prompt := ToolPrompt()
	for _, name := range Commands() {
		if !strings.Contains(prompt, `"`+name+`"`) {
			t.Fatalf("tool prompt misses %q:\n%s", name, prompt)
		}
	}
	if !strings.Contains(prompt, "\"diff\"") && !strings.Contains(prompt, "diff is a unified") {
		t.Fatalf("tool prompt should explain the diff locator:\n%s", prompt)
	}
}
