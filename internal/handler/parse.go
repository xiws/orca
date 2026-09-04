package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"orca/pkg/command"
	"orca/pkg/utils"
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

// Command names accepted on the wire, exposed so the prompt and the parser can
// never drift apart.
func Commands() []string {
	return []string{CommandRead, CommandWrite, CommandEdit, CommandBash}
}

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
	case CommandRead:
		var params struct {
			Id       json.RawMessage `json:"id"`
			Filename string          `json:"filename"`
			Start    int             `json:"start"`
			End      int             `json:"end"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return NewReadOption(parseId(params.Id), params.Filename, params.Start, params.End), nil
	case CommandWrite:
		var params struct {
			Id       json.RawMessage `json:"id"`
			Filename string          `json:"filename"`
			Content  string          `json:"content"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return NewWriteOption(parseId(params.Id), params.Filename, params.Content), nil
	case CommandEdit:
		var params struct {
			Id       json.RawMessage `json:"id"`
			Filename string          `json:"filename"`
			Contents []EditFragment  `json:"contents"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return NewEditOption(parseId(params.Id), params.Filename, params.Contents), nil
	case CommandBash:
		var params struct {
			Id      json.RawMessage `json:"id"`
			Content string          `json:"content"`
			Workdir string          `json:"workdir"`
			Timeout int             `json:"timeout"`
		}
		if err := json.Unmarshal(payload, &params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformedCommand, name, err)
		}
		return NewBashOption(parseId(params.Id), params.Content, params.Workdir, params.Timeout), nil
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
	case *ReadOption:
		target.Id = id
	case *WriteOption:
		target.Id = id
	case *EditOption:
		target.Id = id
	case *BashOption:
		target.Id = id
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
