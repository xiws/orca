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
	// ErrUnknownCommand 在没有注册对应处理器的命令名时返回。
	ErrUnknownCommand = errors.New("unknown command")
	// ErrMalformedCommand 在命令对象无法解码时返回。
	ErrMalformedCommand = errors.New("malformed command")
	// ErrEmptyPayload 在没有可解析内容时返回。
	ErrEmptyPayload = errors.New("no command to parse")
)

// Parse 将一个命令对象解码为其处理器的 option。
//
// 参数可以位于 "command" 旁边或包裹在 "data" 中，两种形式
// 都被接受，因为不同模型嵌套对象的方式不同：
//
//	{"command": "read", "filename": "a.md", "start": 1, "end": 20}
//	{"command": "write", "data": {"filename": "a.md", "content": "hi"}}
//
// 两个层级上的 "id" 都会被保留，以便结果与产生它的调用匹配，
// "command" 旁边的 id 优先；当两者都未提供时生成雪花 id。
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

	// 嵌套参数优先，否则对象本身以扁平形式携带参数。
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

// ParseAll 解码单个命令对象或对象数组，
// 后者是模型希望在一轮中运行多个工具时的输出形式。
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

// OptionFromCall 从工具调用构建 option，其 name 和 arguments 分别传入。
// arguments 持有参数对象，无论是否有 "data" 包裹；
// 调用的 name 和 id 始终优先于其中同名字段，
// 零 id 则让 Parse 生成一个。
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

// parseOption 解码已知命令的参数对象。
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

// setId 将信封 id 写入 option，解析器知道其具体类型但通用流程不知道。
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

// setReasoning 将推理过程写入 option，以便处理器在发布 ToolBeforeEvent 时使用。
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

// parseId 将 "id" 字段解码为 option 携带的 int64。模型可能
// 将其写为数字或引号包裹的数字，两种形式都被接受；
// 缺失或不可读的 id（包括 "call-1" 这样的调用 id）返回 0，
// 调用方会用生成的 id 替换而不是使命令失败。
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

// ParseTextCalls 提取模型写入回复文本中的命令，
// 这是没有原生函数调用的 Provider 请求工具的方式。
//
// 扫描在内容中查找平衡的 {..} 和 [..] 值——周围的散文和代码块
// 会被忽略——只保留 "command" 命名已知命令的对象或此类对象的数组。
// 其他内容（例如仅提到该字段的 JSON 示例）会被跳过。
// 调用参数是不含 "command" 字段的对象，因此 OptionFromCall
// 会像原生调用一样重建信封。
func ParseTextCalls(content string) []llm.ToolCall {
	var calls []llm.ToolCall
	for _, span := range jsonSpans(content) {
		calls = appendSpanCalls(calls, span)
	}
	return calls
}

// jsonSpans 返回 text 中平衡的 {...} 和 [...] 子串。
// 它是括号扫描器而非解析器：调用方通过反序列化来决定
// 是否真的是它要找的内容。字符串字面量内的括号不计入，
// 因此命令可以在参数中携带任意文本。
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

// jsonSpanEnd 返回 start 处开启的值之后的索引。闭合括号
// 必须与开启括号匹配，因此散落的括号不会将相邻值合并为一个。
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

// appendSpanCalls 保留一个片段中的命令。片段要么是单个命令对象，
// 要么是对象数组，即 ToolSchema 文档记录的两种形式。
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

// appendCommandCall 在对象是运行时可执行的命令时追加其调用。
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

// isCommand 报告 name 是否为处理器接受的命令之一。
func isCommand(name string) bool {
	for _, known := range handler.Commands() {
		if name == known {
			return true
		}
	}
	return false
}
