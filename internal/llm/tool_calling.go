package llm

// 本文件将 internal/handler 实现的 read、write、edit 和 bash 命令
// 镜像为 OpenAI 函数调用的工具定义，使支持工具的模型可以原生调用它们，
// 而不必通过 handler.ToolPrompt 描述的自由格式 JSON 协议。
//
// 每个工具的 "name" 及其参数的 "properties" 必须与 handler.parseOption
// 接受的 json 标签保持同步：否则模型可能调用运行时无法解码为 option 的内容。

// ToolType 是 OpenAI chat completions 协议中 "tools" 数组目前唯一接受的分隔符值。
const ToolType = "function"

// 内置工具名称，与 handler.CommandRead/Write/Edit/Bash/CreateTask 对应。
const (
	ToolRead       = "read"
	ToolWrite      = "write"
	ToolEdit       = "edit"
	ToolBash       = "bash"
	ToolCreateTask = "create_task"
)

// Tool 是 chat completion 请求中 "tools" 数组的单个条目，
// 采用 OpenAI 函数调用规范描述的形式。
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 是 Tool 的 "function" 载荷：模型可调用的名称、
// 告知何时调用的描述，以及参数的 JSON Schema。
type ToolFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  ToolSchema `json:"parameters"`
}

// ToolSchema 是一个最小化的自引用 JSON Schema 对象，
// 足以描述每个工具命令接受的扁平和一层嵌套参数形式
// （字符串、整数、布尔值和对象数组）。
type ToolSchema struct {
	Type        string                `json:"type"`
	Description string                `json:"description,omitempty"`
	Properties  map[string]ToolSchema `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
	Items       *ToolSchema           `json:"items,omitempty"`
}

// StringProperty 构建字符串参数的 schema。
func StringProperty(description string) ToolSchema {
	return ToolSchema{Type: "string", Description: description}
}

// IntegerProperty 构建整数参数的 schema。
func IntegerProperty(description string) ToolSchema {
	return ToolSchema{Type: "integer", Description: description}
}

// BooleanProperty 构建布尔参数的 schema。
func BooleanProperty(description string) ToolSchema {
	return ToolSchema{Type: "boolean", Description: description}
}

// ObjectProperty 从属性及其必须子集构建嵌套对象参数的 schema。
func ObjectProperty(description string, properties map[string]ToolSchema, required ...string) ToolSchema {
	return ToolSchema{Type: "object", Description: description, Properties: properties, Required: required}
}

// ArrayProperty 构建数组参数的 schema，元素由 items 描述。
func ArrayProperty(description string, items ToolSchema) ToolSchema {
	return ToolSchema{Type: "array", Description: description, Items: &items}
}

// NewTool 将名称、描述和参数 schema 包装为 Tool。
func NewTool(name, description string, parameters ToolSchema) Tool {
	return Tool{
		Type:     ToolType,
		Function: ToolFunction{Name: name, Description: description, Parameters: parameters},
	}
}

// ReadTool 描述 read 命令：返回文件的基于 1 的行范围（包含两端），
// 省略 start 和 end 时默认读取整个文件，并限制单次调用返回的最大行数。
func ReadTool() Tool {
	return NewTool(ToolRead,
		"Read a file from the workspace and return it with 1 based line numbers. Omit start and end to read the whole file.",
		ObjectProperty("", map[string]ToolSchema{
			"filename": StringProperty("Absolute or workspace-relative path of the file to read"),
			"start":    IntegerProperty("First line to return, 1 based and inclusive; defaults to the first line"),
			"end":      IntegerProperty("Last line to return, 1 based and inclusive; defaults to the last line"),
		}, "filename"))
}

// WriteTool 描述 write 命令：从头重建文件，创建缺失的父目录，
// 因此编辑已有文件应优先使用 edit 工具。
func WriteTool() Tool {
	return NewTool(ToolWrite,
		"Create or overwrite a file with the given content, creating missing parent directories. Everything not present in content is lost; prefer edit for files that already exist.",
		ObjectProperty("", map[string]ToolSchema{
			"filename": StringProperty("Absolute or workspace-relative path of the file to write"),
			"content":  StringProperty("Full new content of the file"),
		}, "filename", "content"))
}

// EditTool 描述 edit 命令：按顺序应用到文件的片段列表，
// 每个片段是 hunkpatch 基于内容模糊匹配的统一 diff，可容忍模型的不精确。
func EditTool() Tool {
	fragment := ObjectProperty("A single replacement to apply to the file", map[string]ToolSchema{
		"diff": StringProperty("Unified diff to apply; line numbers are ignored, matching is content-based and tolerates model imprecision via hunkpatch's fuzzy algorithm"),
	})
	return NewTool(ToolEdit,
		"Apply one or more replacements to an existing file. Fragments are applied in order and the file is only written when every one of them matches.",
		ObjectProperty("", map[string]ToolSchema{
			"filename": StringProperty("Absolute or workspace-relative path of the file to edit"),
			"contents": ArrayProperty("Fragments to apply, in order", fragment),
		}, "filename", "contents"))
}

// BashTool 描述 bash 命令：在 shell 中运行命令行，
// 可选指定工作目录和自定义超时，返回合并的 stdout 和 stderr。
func BashTool() Tool {
	return NewTool(ToolBash,
		"Run a command line in a shell and return stdout and stderr merged. The command is killed and reported as failed once timeout elapses.",
		ObjectProperty("", map[string]ToolSchema{
			"content": StringProperty("The command line to run"),
			"workdir": StringProperty("Absolute or workspace-relative directory to run the command in; defaults to the workspace root"),
			"timeout": IntegerProperty("Seconds the command may run before it is killed; defaults to 60"),
		}, "content"))
}

// CreateTaskTool 描述 create_task 命令：将高层目标分解为子任务，
// 独立运行每个子任务，并将结果聚合为摘要。
func CreateTaskTool() Tool {
	subTask := ObjectProperty("A single sub-task to create and run", map[string]ToolSchema{
		"title":       StringProperty("Short title of the sub-task"),
		"description": StringProperty("What the sub-task must achieve, in enough detail to act on"),
	}, "title", "description")
	return NewTool(ToolCreateTask,
		"Decompose a high-level goal into multiple sub-tasks, run each sub-task independently, and aggregate the results. Use this when the user's request is complex and can be broken into parallel work streams.",
		ObjectProperty("", map[string]ToolSchema{
			"task_target": ArrayProperty("Sub-tasks to create and run", subTask),
		}, "task_target"))
}

// Tools 返回所有内置命令的定义，顺序与 handler.Register 注册时一致。
func Tools() []Tool {
	return []Tool{ReadTool(), WriteTool(), EditTool(), BashTool(), CreateTaskTool()}
}
