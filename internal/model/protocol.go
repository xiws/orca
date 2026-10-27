// Package model 定义 LLM 客户端交互所需的核心数据类型，包括请求、响应、消息、工具调用等。
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

// ErrToolProtocol 工具调用协议格式错误。
var ErrToolProtocol = errors.New("invalid tool call protocol")

// ToolPrompt 根据网关注册的工具声明生成提示词。
// 仅展示网关提供的声明，不负责工具发现或授权。
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

// ValidateTools 校验工具声明列表：名称非空、不重复、参数为 JSON 对象。
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

// ParseTextCalls 解析助手文本中的工具调用。仅识别顶层协议文档，
// 不搜索散文、引用示例、代码块或嵌套 JSON 中的命令。
// 返回 nil, nil 表示普通助手文本；显式错误的调用返回 error。
func ParseTextCalls(content string, tools []Tool) ([]Call, error) {
	text := strings.TrimSpace(content)
	if text == "" {
		return nil, nil
	}
	var calls []Call
	var err error
	switch {
	case strings.HasPrefix(text, "<tool_calls"):
		// 标准 XML 标签格式的工具调用。
		if !strings.HasPrefix(text, "<tool_calls>") || !strings.HasSuffix(text, "</tool_calls>") {
			return nil, ErrToolProtocol
		}
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<tool_calls>"), "</tool_calls>"))
		if !strings.HasPrefix(body, "[") {
			return nil, ErrToolProtocol
		}
		calls, err = parseJSONCalls([]byte(body))
	case strings.HasPrefix(text, "<｜DSML｜"), strings.HasPrefix(text, "<function_calls"), strings.HasPrefix(text, "<invoke"):
		// DeepSeek DSML XML 格式或其他 XML 工具调用格式。
		calls, err = parseXMLCalls(text)
	case text[0] == '{' || text[0] == '[':
		// JSON 格式：仅检查顶层对象键或数组元素是否具有调用意图。
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

// hasCallIntent 检查 JSON 文本的顶层键是否暗示这是一个工具调用请求，
// 避免将嵌套在对象中的 JSON 示例误判为调用。
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
					// 检测到工具调用的标志性键组合。
					if s == "command" || s == "tool_calls" || keys[depth]["name"] && keys[depth]["arguments"] || keys[depth]["function"] && keys[depth]["type"] {
						return true
					}
				}
			}
			objectKey[depth] = !key
		}
	}
}

// parseJSONCalls 解析 JSON 格式的工具调用，支持数组、单对象和 {tool_calls:[...]} 包装。
func parseJSONCalls(raw []byte) ([]Call, error) {
	if err := StrictJSON(raw); err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, ErrToolProtocol
	}
	// 数组格式：每个元素是一个调用。
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
	// 对象格式：检查是否为 {tool_calls:[...]} 包装。
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
	// 单个调用对象。
	call, err := parseJSONCall(raw)
	if err != nil {
		return nil, err
	}
	return []Call{call}, nil
}

// parseJSONCall 解析单个工具调用 JSON 对象，兼容多种格式：
// {name, arguments}、{command, data}、{function:{name,arguments}}。
func parseJSONCall(raw []byte) (Call, error) {
	var f map[string]json.RawMessage
	var call Call
	if json.Unmarshal(raw, &f) != nil || f == nil {
		return call, ErrToolProtocol
	}
	// 解析可选的 id 字段。
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
	// command 格式（部分提供方的非标准格式）。
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
	// function 格式（OpenAI 标准格式）。
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
	// 标准 {name, arguments} 格式。
	if json.Unmarshal(f["name"], &call.Name) != nil {
		return call, ErrToolProtocol
	}
	args, ok := f["arguments"]
	if !ok || len(f) != 2 {
		return call, ErrToolProtocol
	}
	if json.Unmarshal(args, &call.Arguments) != nil {
		// arguments 可能是字符串化的 JSON。
		call.Arguments = string(args)
	}
	return call, nil
}

// NormalizeCalls 校验工具调用的语法和名称，并为缺失的 ID 生成确定性标识。
// 返回新切片副本，不修改输入。
func NormalizeCalls(calls []Call, tools []Tool) ([]Call, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	// 构建允许的工具名称集合。
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
	// 为缺失 ID 的调用生成基于内容哈希的确定性 ID。
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

// jsonObject 检查字节切片是否为合法的 JSON 对象。
func jsonObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{' && StrictJSON(raw) == nil
}

// StrictJSON 严格校验 JSON：拒绝尾随数据和重复对象键，
// 避免模型适配器和工具网关之间的歧义解读。
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

// jsonValue 递归校验 JSON 值结构，限制嵌套深度为 128。
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
		return nil // 标量值，无需递归。
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
				return ErrToolProtocol // 重复键。
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
