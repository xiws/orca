// 工具定义注册表：JSON Schema 验证 + 参数解析
package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

// schema 既是发布给模型的契约，也是运行时验证器。
// 参数解析规则必须与此注册表保持一致。
type schema struct {
	Type                 string             `json:"type"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *schema            `json:"items,omitempty"`
	MinLength            int                `json:"minLength,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
	Minimum              int                `json:"minimum,omitempty"`
	Maximum              int                `json:"maximum,omitempty"`
	MinItems             int                `json:"minItems,omitempty"`
}

// entry 定义单个工具的注册条目
type entry struct {
	name, description string  // 工具名称和描述
	parameters        *schema // 参数 Schema
	sideEffect        bool    // 是否有副作用（需要审批）
	control           bool    // 是否为控制流工具（不直接执行）
}

// object 构建 object 类型的 Schema
func object(properties map[string]*schema, required ...string) *schema {
	no := false
	return &schema{Type: "object", Properties: properties, Required: required, AdditionalProperties: &no}
}

// text 构建 string 类型的 Schema，nonempty 时要求非空
func text(description string, nonempty bool) *schema {
	s := &schema{Type: "string", Description: description}
	if nonempty {
		s.MinLength = 1
		s.Pattern = `\S`
	}
	return s
}

// integer 构建 integer 类型的 Schema，带最小/最大值约束
func integer(description string, min, max int) *schema {
	return &schema{Type: "integer", Description: description, Minimum: min, Maximum: max}
}

// hash 构建 SHA-256 哈希验证的 Schema，allowAbsent 允许 "absent" 表示新建文件
func hash(allowAbsent bool) *schema {
	pattern := `^[0-9a-f]{64}$`
	if allowAbsent {
		pattern = `^([0-9a-f]{64}|absent)$`
	}
	return &schema{Type: "string", Description: "SHA-256 from read; use absent only to create a new file", Pattern: pattern}
}

// registry 注册所有内置工具的定义
var registry = []entry{
	{"read", "Read a workspace file. Return at most 1 MiB and 2000 lines, with a SHA-256 of the entire file (streamed). Line bounds are inclusive.", object(map[string]*schema{
		"filename": text("Absolute or workspace-relative filename", true),
		"start":    integer("First line, default 1", 1, 0),
		"end":      integer("Last line; at most 2000 lines are returned", 1, 0),
	}, "filename"), false, false},
	{"write", "Atomically create or replace a workspace file with a matching baseline. Requires approval unless explicitly authorized.", object(map[string]*schema{
		"filename":      text("Absolute or workspace-relative filename", true),
		"content":       text("Full new file content", false),
		"expected_hash": hash(true),
	}, "filename", "content", "expected_hash"), true, false},
	{"edit", "Replace exactly one occurrence of old_string in a workspace file, only with a matching baseline. Requires approval unless explicitly authorized.", object(map[string]*schema{
		"filename":      text("Absolute or workspace-relative filename", true),
		"old_string":    {Type: "string", MinLength: 1},
		"new_string":    text("Replacement text", false),
		"expected_hash": hash(false),
	}, "filename", "old_string", "new_string", "expected_hash"), true, false},
	{"bash", "Run bash with an opened workspace directory as cwd. This is NOT a shell sandbox: commands can access outside the workspace. Output is limited to 64 KiB; cancellation kills the process group. Requires approval unless explicitly authorized.", object(map[string]*schema{
		"content": text("Shell command", true),
		"workdir": text("Workspace directory, default workspace root", false),
		"timeout": integer("Timeout in seconds, default 60, maximum 120", 1, 120),
	}, "content"), true, false},
	{"create_task", "Return proposed child goals to the orchestrator. Does not create tasks, runs, call a model, or schedule work.", object(map[string]*schema{
		"task_target": {Type: "array", MinItems: 1, Items: object(map[string]*schema{
			"title":       text("Short goal title", true),
			"description": text("Goal description", true),
		}, "title", "description")},
	}, "task_target"), false, true},
	{"request_input", "Pause for user input. Only the orchestrator persists the waiting request; an answered request supplies the tool result.", object(map[string]*schema{
		"prompt": text("Question for the user", true),
	}, "prompt"), false, true},
}

// Definitions 根据策略过滤后返回可用的工具定义列表
func (g *Gateway) Definitions(policy domain.Policy) []model.Tool {
	out := make([]model.Tool, 0, len(registry))
	for _, e := range registry {
		if policy.Allows(e.name) {
			b, _ := json.Marshal(e.parameters)
			out = append(out, model.Tool{Name: e.name, Description: e.description, Parameters: b})
		}
	}
	return out
}

// lookup 按名称查找工具注册条目
func lookup(name string) *entry {
	for i := range registry {
		if registry[i].name == name {
			return &registry[i]
		}
	}
	return nil
}

// decodeJSON 严格解码 JSON：拒绝重复键和尾部数据。
// 普通的 encoding/json map 解码器会静默接受重复键，对审批绑定不安全。
func decodeJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("JSON nesting is too deep")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("object key must be a string")
			}
			if _, exists := m[key]; exists {
				return nil, fmt.Errorf("duplicate field %q", key)
			}
			v, err := decodeJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		_, err := d.Token()
		return m, err
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := decodeJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := d.Token()
		return a, err
	default:
		return t, nil
	}
}

// validate 根据 Schema 验证参数值，递归检查类型、必填字段、模式匹配等
func (s *schema) validate(v any, path string) error {
	bad := func(message string) error { return fmt.Errorf("%s: %s", path, message) }
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return bad("must be an object")
		}
		for _, key := range s.Required {
			if _, ok := m[key]; !ok {
				return bad("missing field " + key)
			}
		}
		for key, value := range m {
			child, ok := s.Properties[key]
			if !ok {
				return bad("unknown field " + key)
			}
			if err := child.validate(value, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return bad("must be an array")
		}
		if len(a) < s.MinItems {
			return bad("array is empty")
		}
		for i, value := range a {
			if err := s.Items.validate(value, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		value, ok := v.(string)
		if !ok {
			return bad("must be a string")
		}
		if utf8.RuneCountInString(value) < s.MinLength {
			return bad("string is too short")
		}
		if s.Pattern != "" && !regexp.MustCompile(s.Pattern).MatchString(value) {
			return bad("string does not match " + s.Pattern)
		}
	case "integer":
		value, ok := v.(json.Number)
		if !ok {
			return bad("must be an integer")
		}
		n, err := strconv.Atoi(string(value))
		if err != nil {
			return bad("must be an integer within machine range")
		}
		if n < s.Minimum || (s.Maximum != 0 && n > s.Maximum) {
			return bad("integer is outside permitted bounds")
		}
	default:
		return bad("unsupported schema")
	}
	return nil
}

// parse 解析并验证工具的 JSON 参数，返回解析后的参数 map、规范化 JSON 字符串
func (e *entry) parse(raw string) (map[string]any, string, error) {
	if !utf8.ValidString(raw) {
		return nil, "", fmt.Errorf("arguments must be UTF-8")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	v, err := decodeJSON(d, 0)
	if err != nil {
		return nil, "", err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, "", fmt.Errorf("trailing JSON data")
	}
	if err := e.parameters.validate(v, "arguments"); err != nil {
		return nil, "", err
	}
	args := v.(map[string]any)
	if e.name == "read" {
		start := intArg(args, "start", 1)
		if end := intArg(args, "end", 0); end != 0 && end < start {
			return nil, "", fmt.Errorf("end must not precede start")
		}
	}
	b, err := json.Marshal(args)
	return args, string(b), err
}

// stringArg 从参数 map 中提取字符串值
func stringArg(args map[string]any, name string) string { s, _ := args[name].(string); return s }

// intArg 从参数 map 中提取整数值，不存在时返回默认值
func intArg(args map[string]any, name string, fallback int) int {
	if n, ok := args[name].(json.Number); ok {
		v, _ := strconv.Atoi(string(n))
		return v
	}
	return fallback
}
