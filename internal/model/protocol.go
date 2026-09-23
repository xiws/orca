package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var ErrToolProtocol = errors.New("invalid tool call protocol")

// ToolPrompt describes only declarations supplied by the gateway. It does not
// discover tools or authorize execution; schema validation remains with it.
func ToolPrompt(tools []Tool) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	if err := ValidateTools(tools); err != nil {
		return "", err
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return "", ErrToolProtocol
	}
	return "Available tools (JSON Schema):\n" + string(raw) + "\nTo call tools, respond with ONLY <tool_calls>[{\"name\":\"tool name\",\"arguments\":{...}}]</tool_calls>. Do not place calls in prose, quotes, examples, or code fences. Otherwise respond with ordinary text.", nil
}

func ValidateTools(tools []Tool) error {
	seen := map[string]bool{}
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == "" || seen[t.Name] || !jsonObject(t.Parameters) {
			return fmt.Errorf("%w: invalid tool declaration", ErrToolProtocol)
		}
		seen[t.Name] = true
	}
	return nil
}

// ParseTextCalls recognizes only an entire top-level protocol document. It never
// searches prose, quoted examples, code fences, or nested JSON for commands.
// nil, nil means ordinary assistant text; explicit broken calls return an error.
func ParseTextCalls(content string, tools []Tool) ([]Call, error) {
	text := strings.TrimSpace(content)
	if text == "" {
		return nil, nil
	}
	var calls []Call
	var err error
	switch {
	case strings.HasPrefix(text, "<tool_calls"):
		if !strings.HasPrefix(text, "<tool_calls>") || !strings.HasSuffix(text, "</tool_calls>") {
			return nil, ErrToolProtocol
		}
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<tool_calls>"), "</tool_calls>"))
		if !strings.HasPrefix(body, "[") {
			return nil, ErrToolProtocol
		}
		calls, err = parseJSONCalls([]byte(body))
	case strings.HasPrefix(text, "<｜DSML｜"), strings.HasPrefix(text, "<function_calls"), strings.HasPrefix(text, "<invoke"):
		calls, err = parseXMLCalls(text)
	case text[0] == '{' || text[0] == '[':
		// Inspect only top-level object keys (or immediate array elements), even
		// when a document is incomplete. A JSON example nested in an object is text.
		if !hasCallIntent(text) {
			return nil, nil
		}
		calls, err = parseJSONCalls([]byte(text))
	default:
		return nil, nil
	}
	if err != nil {
		return nil, ErrToolProtocol
	}
	return NormalizeCalls(calls, tools)
}

func hasCallIntent(text string) bool {
	dec := json.NewDecoder(strings.NewReader(text))
	depth := 0
	array := strings.HasPrefix(text, "[")
	objectKey := map[int]bool{}
	keys := map[int]map[string]bool{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
				if d == '{' {
					objectKey[depth] = true
					keys[depth] = map[string]bool{}
				}
			case '}', ']':
				delete(objectKey, depth)
				delete(keys, depth)
				depth--
				if _, ok := objectKey[depth]; ok {
					objectKey[depth] = true
				}
			}
			continue
		}
		if key, ok := objectKey[depth]; ok {
			if key {
				if s, ok := tok.(string); ok && (depth == 1 || array && depth == 2) {
					keys[depth][s] = true
					if s == "command" || s == "tool_calls" || keys[depth]["name"] && keys[depth]["arguments"] || keys[depth]["function"] && keys[depth]["type"] {
						return true
					}
				}
			}
			objectKey[depth] = !key
		}
	}
}

func parseJSONCalls(raw []byte) ([]Call, error) {
	if err := StrictJSON(raw); err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, ErrToolProtocol
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
			return nil, ErrToolProtocol
		}
		var calls []Call
		for _, item := range items {
			call, err := parseJSONCall(item)
			if err != nil {
				return nil, err
			}
			calls = append(calls, call)
		}
		return calls, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, ErrToolProtocol
	}
	if body, ok := fields["tool_calls"]; ok {
		if len(fields) != 1 || !bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
			return nil, ErrToolProtocol
		}
		return parseJSONCalls(body)
	}
	call, err := parseJSONCall(raw)
	if err != nil {
		return nil, err
	}
	return []Call{call}, nil
}

func parseJSONCall(raw []byte) (Call, error) {
	var f map[string]json.RawMessage
	var call Call
	if json.Unmarshal(raw, &f) != nil || f == nil {
		return call, ErrToolProtocol
	}
	if id, ok := f["id"]; ok {
		if string(id) == "null" {
			return call, ErrToolProtocol
		}
		if json.Unmarshal(id, &call.ID) != nil {
			var number json.Number
			if json.Unmarshal(id, &number) != nil {
				return call, ErrToolProtocol
			}
			if _, err := strconv.ParseInt(number.String(), 10, 64); err != nil {
				return call, ErrToolProtocol
			}
			call.ID = number.String()
		}
		delete(f, "id")
	}
	if command, ok := f["command"]; ok {
		if json.Unmarshal(command, &call.Name) != nil {
			return call, ErrToolProtocol
		}
		call.Name = strings.ToLower(strings.TrimSpace(call.Name))
		delete(f, "command")
		if data, ok := f["data"]; ok {
			if len(f) != 1 || !jsonObject(data) {
				return call, ErrToolProtocol
			}
			var args map[string]json.RawMessage
			_ = json.Unmarshal(data, &args)
			if nestedID, ok := args["id"]; ok {
				if call.ID == "" {
					var id string
					if json.Unmarshal(nestedID, &id) == nil {
						call.ID = id
					} else {
						var n int64
						if json.Unmarshal(nestedID, &n) != nil {
							return call, ErrToolProtocol
						}
						call.ID = strconv.FormatInt(n, 10)
					}
				}
				delete(args, "id")
			}
			payload, _ := json.Marshal(args)
			call.Arguments = string(payload)
		} else {
			payload, _ := json.Marshal(f)
			call.Arguments = string(payload)
		}
		return call, nil
	}
	if function, ok := f["function"]; ok {
		if typ, ok := f["type"]; ok && string(typ) != `"function"` {
			return call, ErrToolProtocol
		}
		delete(f, "type")
		delete(f, "function")
		if len(f) != 0 || json.Unmarshal(function, &f) != nil {
			return call, ErrToolProtocol
		}
	}
	if json.Unmarshal(f["name"], &call.Name) != nil {
		return call, ErrToolProtocol
	}
	args, ok := f["arguments"]
	if !ok || len(f) != 2 {
		return call, ErrToolProtocol
	}
	if json.Unmarshal(args, &call.Arguments) != nil {
		call.Arguments = string(args)
	}
	return call, nil
}

// NormalizeCalls validates syntax and the declared tool names and supplies
// deterministic missing IDs. It copies the slice and never mutates the input.
func NormalizeCalls(calls []Call, tools []Tool) ([]Call, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	allowed := map[string]bool{}
	for _, t := range tools {
		allowed[t.Name] = true
	}
	out := append([]Call(nil), calls...)
	seen := map[string]bool{}
	for _, c := range out {
		if !allowed[c.Name] || !jsonObject([]byte(c.Arguments)) {
			return nil, ErrToolProtocol
		}
		if c.ID != "" {
			if seen[c.ID] {
				return nil, ErrToolProtocol
			}
			seen[c.ID] = true
		}
	}
	for i := range out {
		if out[i].ID == "" {
			hash := sha256.Sum256([]byte(out[i].Name + "\x00" + out[i].Arguments))
			id := fmt.Sprintf("call_%x_%d", hash[:8], i)
			for seen[id] {
				id += "_"
			}
			out[i].ID = id
			seen[id] = true
		}
	}
	return out, nil
}

func jsonObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{' && StrictJSON(raw) == nil
}

// StrictJSON rejects trailing values and duplicate object keys, avoiding
// ambiguous interpretations between the model adapter and tool gateway.
func StrictJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := jsonValue(dec, 0); err != nil {
		return ErrToolProtocol
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrToolProtocol
	}
	return nil
}

func jsonValue(dec *json.Decoder, depth int) error {
	if depth > 128 {
		return ErrToolProtocol
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := tok.(string)
			if !ok || seen[key] {
				return ErrToolProtocol
			}
			seen[key] = true
			if err := jsonValue(dec, depth+1); err != nil {
				return err
			}
		}
		tok, err = dec.Token()
		if err != nil || tok != json.Delim('}') {
			return ErrToolProtocol
		}
	case '[':
		for dec.More() {
			if err := jsonValue(dec, depth+1); err != nil {
				return err
			}
		}
		tok, err = dec.Token()
		if err != nil || tok != json.Delim(']') {
			return ErrToolProtocol
		}
	default:
		return ErrToolProtocol
	}
	return nil
}
