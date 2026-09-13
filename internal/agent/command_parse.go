package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/command"
	"github.com/xiws/orca/pkg/utils"
	"strconv"
	"strings"
)

var (
	// ErrUnknownCommand is returned for a command name no handler is registered
	// for.
	ErrUnknownCommand = errors.New("unknown command")
	// ErrMalformedCommand is returned when a command object cannot be decoded.
	ErrMalformedCommand = errors.New("malformed command")
	// ErrEmptyPayload is returned when there is nothing to parse.
	ErrEmptyPayload = errors.New("no command to parse")
)

// Parse decodes one command object into the option of its handler.
//
// Parameters may sit next to "command" or be wrapped in "data", and both forms
// are accepted, because models differ in how faithfully they nest an object:
//
//	{"command": "read", "filename": "a.md", "start": 1, "end": 20}
//	{"command": "write", "data": {"filename": "a.md", "content": "hi"}}
//
// An "id" on either level is kept so the result can be matched with the call
// that produced it, the one next to "command" winning; when neither is given a
// snowflake id is generated.
func Parse(raw []byte) (command.CommandOption, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrEmptyPayload
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedCommand, err)
	}

	var name string
	if err := unmarshalField(fields, "command", &name); err != nil {
		return nil, err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, fmt.Errorf("%w: missing command name", ErrMalformedCommand)
	}

	// Nested parameters win, otherwise the object itself carries them flat.
	payload := raw
	if data, ok := fields["data"]; ok && len(bytes.TrimSpace(data)) > 0 {
		payload = data
	}

	opt, err := parseOption(name, payload)
	if err != nil {
		return nil, err
	}
	if outer := parseId(fields["id"]); outer != 0 {
		setId(opt, outer)
	} else if opt.GetId() == 0 {
		setId(opt, utils.GetSnowFlakeId())
	}
	return opt, nil
}

// ParseAll decodes a single command object or an array of them, which is what a
// model emits when it wants several tools to run in one turn.
func ParseAll(raw []byte) ([]command.CommandOption, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, ErrEmptyPayload
	}
	if trimmed[0] != '[' {
		opt, err := Parse(trimmed)
		if err != nil {
			return nil, err
		}
		return []command.CommandOption{opt}, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedCommand, err)
	}
	if len(items) == 0 {
		return nil, ErrEmptyPayload
	}
	options := make([]command.CommandOption, 0, len(items))
	for i, item := range items {
		opt, err := Parse(item)
		if err != nil {
			return nil, fmt.Errorf("command %d: %w", i+1, err)
		}
		options = append(options, opt)
	}
	return options, nil
}

// OptionFromCall builds an option out of a tool call, whose name and arguments
// arrive separately. arguments holds the parameter object, with or without the
// "data" wrapper; the name and id of the call always win over fields of the
// same name inside it, and a zero id lets Parse generate one.
func OptionFromCall(id int64, name, arguments string) (command.CommandOption, error) {
	body := strings.TrimSpace(arguments)
	if body == "" {
		body = "{}"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return nil, fmt.Errorf("%w: %s arguments: %v", ErrMalformedCommand, name, err)
	}
	commandName, err := json.Marshal(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
	}
	fields["command"] = commandName
	if id != 0 {
		callId, err := json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		fields["id"] = callId
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
	}
	return Parse(raw)
}

// parseOption decodes the parameter object of a known command.
func parseOption(name string, payload json.RawMessage) (command.CommandOption, error) {
	switch name {
	case handler.CommandCreateTask:
		var params struct {
			Id         json.RawMessage        `json:"id"`
			TaskTarget []handler.TaskBaseInfo `json:"task_target"`
		}

		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return handler.NewCreateTaskOption(parseId(params.Id), params.TaskTarget), nil
	case handler.CommandRead:
		var params struct {
			Id       json.RawMessage `json:"id"`
			Filename string          `json:"filename"`
			Start    int             `json:"start"`
			End      int             `json:"end"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return handler.NewReadOption(parseId(params.Id), params.Filename, params.Start, params.End), nil
	case handler.CommandWrite:
		var params struct {
			Id       json.RawMessage `json:"id"`
			Filename string          `json:"filename"`
			Content  string          `json:"content"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return handler.NewWriteOption(parseId(params.Id), params.Filename, params.Content), nil
	case handler.CommandEdit:
		var params struct {
			Id       json.RawMessage        `json:"id"`
			Filename string                 `json:"filename"`
			Contents []handler.EditFragment `json:"contents"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return handler.NewEditOption(parseId(params.Id), params.Filename, params.Contents), nil
	case handler.CommandBash:
		var params struct {
			Id      json.RawMessage `json:"id"`
			Content string          `json:"content"`
			Workdir string          `json:"workdir"`
			Timeout int             `json:"timeout"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return handler.NewBashOption(parseId(params.Id), params.Content, params.Workdir, params.Timeout), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownCommand, name)
	}
}

// setId writes the envelope id into an option, whose concrete type the parser
// knows but the generic flow does not.
func setId(opt command.CommandOption, id int64) {
	if id == 0 {
		return
	}
	switch target := opt.(type) {
	case *handler.CreateTaskOption:
		target.Id = id
	case *handler.ReadOption:
		target.Id = id
	case *handler.WriteOption:
		target.Id = id
	case *handler.EditOption:
		target.Id = id
	case *handler.BashOption:
		target.Id = id
	}
}

// setReasoning writes the reasoning into an option, so the handler can publish
// it alongside the ToolBeforeEvent.
func setReasoning(opt command.CommandOption, reasoning string) {
	if reasoning == "" {
		return
	}
	switch target := opt.(type) {
	case *handler.CreateTaskOption:
		target.Reasoning = reasoning
	case *handler.ReadOption:
		target.Reasoning = reasoning
	case *handler.WriteOption:
		target.Reasoning = reasoning
	case *handler.EditOption:
		target.Reasoning = reasoning
	case *handler.BashOption:
		target.Reasoning = reasoning
	}
}

// parseId decodes an "id" field into the int64 the options carry. A model may
// spell it either as a number or as a quoted number, so both are accepted; a
// missing or unreadable one, a call id such as "call-1" included, yields 0 so
// the caller replaces it with a generated one instead of failing the command.
func parseId(raw json.RawMessage) int64 {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func unmarshalField(fields map[string]json.RawMessage, name string, target any) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
	}
	return nil
}

// ParseTextCalls extracts the commands a model wrote into its reply text,
// which is how providers without native function calling ask for tools.
//
// The scan looks for balanced {..} and [..] values anywhere in the content —
// prose and code fences around them are ignored — and keeps only objects
// whose "command" names a known command, or arrays of such objects. Anything
// else, a JSON example that merely mentions the field included, is skipped.
// The call arguments are the object without its "command" field, so
// OptionFromCall rebuilds the envelope exactly as it does for native calls.
func ParseTextCalls(content string) []llm.ToolCall {
	var calls []llm.ToolCall
	for _, span := range jsonSpans(content) {
		calls = appendSpanCalls(calls, span)
	}
	return calls
}

// jsonSpans returns the balanced {...} and [...] substrings of text. It is a
// bracket scanner rather than a parser: the caller unmarshals a span to decide
// whether it is really what it looks for. Brackets inside string literals do
// not count, so a command may carry arbitrary text in its arguments.
func jsonSpans(text string) []string {
	var spans []string
	for index := 0; index < len(text); index++ {
		if open := text[index]; open != '{' && open != '[' {
			continue
		}
		end, ok := jsonSpanEnd(text, index)
		if !ok {
			continue
		}
		spans = append(spans, text[index:end])
		index = end - 1
	}
	return spans
}

// jsonSpanEnd returns the index just past the value opened at start. Closing
// brackets must match their openers, so a stray brace cannot glue two
// neighbouring values into one span.
func jsonSpanEnd(text string, start int) (int, bool) {
	var closers []byte
	inString, escaped := false, false
	for index := start; index < len(text); index++ {
		char := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			closers = append(closers, '}')
		case '[':
			closers = append(closers, ']')
		case '}', ']':
			if len(closers) == 0 || closers[len(closers)-1] != char {
				return 0, false
			}
			closers = closers[:len(closers)-1]
			if len(closers) == 0 {
				return index + 1, true
			}
		}
	}
	return 0, false
}

// appendSpanCalls keeps the commands of one span. A span is either a single
// command object or an array of them, the two shapes ToolSchema documents.
func appendSpanCalls(calls []llm.ToolCall, span string) []llm.ToolCall {
	trimmed := strings.TrimSpace(span)
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return calls
		}
		for _, item := range items {
			calls = appendCommandCall(calls, item)
		}
		return calls
	}
	return appendCommandCall(calls, []byte(trimmed))
}

// appendCommandCall appends the call of one object when it is a command the
// runtime can run.
func appendCommandCall(calls []llm.ToolCall, raw []byte) []llm.ToolCall {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return calls
	}
	var name string
	if err := json.Unmarshal(fields["command"], &name); err != nil {
		return calls
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !isCommand(name) {
		return calls
	}

	delete(fields, "command")
	arguments, err := json.Marshal(fields)
	if err != nil {
		return calls
	}
	return append(calls, llm.ToolCall{Name: name, Arguments: string(arguments)})
}

// isCommand reports whether name is one of the commands the handlers accept.
func isCommand(name string) bool {
	for _, known := range handler.Commands() {
		if name == known {
			return true
		}
	}
	return false
}
