package handler

// ReadOption 是 "read" 命令参数：返回 Filename 的 Start 到 End 行，
// 1 起始且包含边界。零表示开放范围，因此默认读取整个文件。
// 每行携带行号。
type ReadOption struct {
	Id        int64
	Reasoning string
	Filename  string
	Start     int
	End       int
}

// GetId 返回将结果与此调用关联的调用 id。
func (t ReadOption) GetId() int64 { return t.Id }

// GetName 返回命令的路由名称。
func (ReadOption) GetName() string { return CommandRead }

// WriteOption 是 "write" 命令参数：用 Content 覆盖 Filename，
// 在文件及其父目录不存在时创建。
type WriteOption struct {
	Id        int64
	Reasoning string
	Filename  string
	Content   string
}

// GetId 返回将结果与此调用关联的调用 id。
func (t WriteOption) GetId() int64 { return t.Id }

// GetName 返回命令的路由名称。
func (WriteOption) GetName() string { return CommandWrite }

// EditOption 是 "edit" 命令参数：Contents 按顺序应用到 Filename，
// 每个片段接收前一个片段的输出。
type EditOption struct {
	Id        int64
	Reasoning string
	Filename  string
	Contents  []EditFragment
}

// GetId 返回将结果与此调用关联的调用 id。
func (t EditOption) GetId() int64 { return t.Id }

// GetName 返回命令的路由名称。
func (EditOption) GetName() string { return CommandEdit }

// EditFragment 是 edit 命令中的单个替换。Diff 是统一 diff，
// 由 hunkpatch 基于内容应用，因此不需要行号和精确的上下文。
type EditFragment struct {
	Diff string `json:"diff,omitempty"`
}

// BashOption 是 "bash" 命令参数：在 Workdir 中运行 Content。
// Timeout 为零表示使用 DefaultBashTimeout 秒。
type BashOption struct {
	Id        int64
	Reasoning string
	Content   string
	Workdir   string
	Timeout   int
}

// GetId 返回将结果与此调用关联的调用 id。
func (t BashOption) GetId() int64 { return t.Id }

// GetName 返回命令的路由名称。
func (BashOption) GetName() string { return CommandBash }

// Option 以指针形式传递给处理器，以下是 Parse 和下方构造函数产生的形式。

// NewReadOption 构建 read 命令。
func NewReadOption(id int64, filename string, start, end int) *ReadOption {
	return &ReadOption{Id: id, Filename: filename, Start: start, End: end}
}

// NewWriteOption 构建 write 命令。
func NewWriteOption(id int64, filename, content string) *WriteOption {
	return &WriteOption{Id: id, Filename: filename, Content: content}
}

// NewEditOption 构建 edit 命令。
func NewEditOption(id int64, filename string, contents []EditFragment) *EditOption {
	return &EditOption{Id: id, Filename: filename, Contents: contents}
}

// NewBashOption 构建 bash 命令。
func NewBashOption(id int64, content, workdir string, timeout int) *BashOption {
	return &BashOption{Id: id, Content: content, Workdir: workdir, Timeout: timeout}
}
