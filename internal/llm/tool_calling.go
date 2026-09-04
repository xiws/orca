package llm

// This file mirrors the read, write, edit and bash commands implemented by
// internal/handler as OpenAI function-calling tool definitions, so a model that
// supports tools can invoke them natively instead of through the free-form
// JSON protocol described by handler.ToolPrompt.
//
// The "name" of every tool and the "properties" of its parameters must stay in
// sync with the json tags handler.parseOption accepts: a drift there would let
// the model call something the runtime cannot decode back into an option.

// ToolType is the only discriminator value OpenAI's chat completions protocol
// accepts for entries of the "tools" array today.
const ToolType = "function"

// Names of the built-in tools, matching handler.CommandRead/Write/Edit/Bash.
const (
	ToolRead  = "read"
	ToolWrite = "write"
	ToolEdit  = "edit"
	ToolBash  = "bash"
)

// Tool is a single entry of the "tools" array sent with a chat completion
// request, in the shape described by the OpenAI function-calling spec.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the "function" payload of a Tool: a name the model may call,
// a description that tells it when to, and a JSON Schema for its arguments.
type ToolFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  ToolSchema `json:"parameters"`
}

// ToolSchema is a minimal, self-referential JSON Schema object, enough to
// describe the flat and one-level-nested parameter shapes every tool command
// accepts (strings, integers, booleans and arrays of objects).
type ToolSchema struct {
	Type        string                `json:"type"`
	Description string                `json:"description,omitempty"`
	Properties  map[string]ToolSchema `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
	Items       *ToolSchema           `json:"items,omitempty"`
}

// StringProperty builds a schema for a string parameter.
func StringProperty(description string) ToolSchema {
	return ToolSchema{Type: "string", Description: description}
}

// IntegerProperty builds a schema for an integer parameter.
func IntegerProperty(description string) ToolSchema {
	return ToolSchema{Type: "integer", Description: description}
}

// BooleanProperty builds a schema for a boolean parameter.
func BooleanProperty(description string) ToolSchema {
	return ToolSchema{Type: "boolean", Description: description}
}

// ObjectProperty builds a schema for a nested object parameter out of its
// properties and the subset of them that are mandatory.
func ObjectProperty(description string, properties map[string]ToolSchema, required ...string) ToolSchema {
	return ToolSchema{Type: "object", Description: description, Properties: properties, Required: required}
}

// ArrayProperty builds a schema for an array parameter whose elements are all
// described by items.
func ArrayProperty(description string, items ToolSchema) ToolSchema {
	return ToolSchema{Type: "array", Description: description, Items: &items}
}

// NewTool wraps name, description and a parameter schema into a Tool.
func NewTool(name, description string, parameters ToolSchema) Tool {
	return Tool{
		Type:     ToolType,
		Function: ToolFunction{Name: name, Description: description, Parameters: parameters},
	}
}

// ReadTool describes the read command: it returns a 1 based, inclusive line
// range of a file, defaulting to the whole file when start and end are
// omitted, and caps how many lines one call returns.
func ReadTool() Tool {
	return NewTool(ToolRead,
		"Read a file from the workspace and return it with 1 based line numbers. Omit start and end to read the whole file.",
		ObjectProperty("", map[string]ToolSchema{
			"filename": StringProperty("Absolute or workspace-relative path of the file to read"),
			"start":    IntegerProperty("First line to return, 1 based and inclusive; defaults to the first line"),
			"end":      IntegerProperty("Last line to return, 1 based and inclusive; defaults to the last line"),
		}, "filename"))
}

// WriteTool describes the write command: it rebuilds a file from scratch,
// creating missing parent directories, which is why editing an existing file
// should prefer the edit tool instead.
func WriteTool() Tool {
	return NewTool(ToolWrite,
		"Create or overwrite a file with the given content, creating missing parent directories. Everything not present in content is lost; prefer edit for files that already exist.",
		ObjectProperty("", map[string]ToolSchema{
			"filename": StringProperty("Absolute or workspace-relative path of the file to write"),
			"content":  StringProperty("Full new content of the file"),
		}, "filename", "content"))
}

// EditTool describes the edit command: a list of fragments applied to a file in
// order, each located either by an exactly-matching old_string or by a 1 based
// line range.
func EditTool() Tool {
	fragment := ObjectProperty("A single replacement to apply to the file", map[string]ToolSchema{
		"old_string":  StringProperty("Exact text to find; must match uniquely unless replace_all is true"),
		"new_string":  StringProperty("Replacement text for old_string"),
		"replace_all": BooleanProperty("Replace every occurrence of old_string instead of requiring a unique match"),
		"start":       IntegerProperty("First line of the range to replace, 1 based and inclusive; an alternative locator to old_string, or a check on where it matched"),
		"end":         IntegerProperty("Last line of the range to replace, 1 based and inclusive; defaults to start"),
		"content":     StringProperty("Replacement lines when the fragment is located by start/end instead of old_string"),
	})
	return NewTool(ToolEdit,
		"Apply one or more replacements to an existing file. Fragments are applied in order and the file is only written when every one of them matches.",
		ObjectProperty("", map[string]ToolSchema{
			"filename": StringProperty("Absolute or workspace-relative path of the file to edit"),
			"contents": ArrayProperty("Fragments to apply, in order", fragment),
		}, "filename", "contents"))
}

// BashTool describes the bash command: it runs a command line in a shell,
// optionally inside a working directory and with a custom timeout, and returns
// stdout and stderr merged.
func BashTool() Tool {
	return NewTool(ToolBash,
		"Run a command line in a shell and return stdout and stderr merged. The command is killed and reported as failed once timeout elapses.",
		ObjectProperty("", map[string]ToolSchema{
			"content": StringProperty("The command line to run"),
			"workdir": StringProperty("Absolute or workspace-relative directory to run the command in; defaults to the workspace root"),
			"timeout": IntegerProperty("Seconds the command may run before it is killed; defaults to 60"),
		}, "content"))
}

// Tools returns the definitions of every built-in command, in the order they
// are registered by handler.Register.
func Tools() []Tool {
	return []Tool{ReadTool(), WriteTool(), EditTool(), BashTool()}
}
