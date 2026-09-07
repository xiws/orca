package handler

// ReadOption is the "read" command parameter: it returns the lines Start
// through End of Filename, 1 based and inclusive. Zero means open ended, so the
// default reads the whole file.
type ReadOption struct {
	Id        int64
	Reasoning string
	Filename  string
	Start     int
	End       int
	SetNumber bool
}

// GetId returns the invocation id that correlates the result with this call.
func (t ReadOption) GetId() int64 { return t.Id }

// GetName returns the routing name of the command.
func (ReadOption) GetName() string { return CommandRead }

// WriteOption is the "write" command parameter: it overwrites Filename with
// Content, creating the file and its parent directories when missing.
type WriteOption struct {
	Id       int64
	Reasoning string
	Filename string
	Content  string
}

// GetId returns the invocation id that correlates the result with this call.
func (t WriteOption) GetId() int64 { return t.Id }

// GetName returns the routing name of the command.
func (WriteOption) GetName() string { return CommandWrite }

// EditOption is the "edit" command parameter: Contents are applied to Filename
// in order, each fragment receiving the output of the previous one.
type EditOption struct {
	Id       int64
	Reasoning string
	Filename string
	Contents []EditFragment
}

// GetId returns the invocation id that correlates the result with this call.
func (t EditOption) GetId() int64 { return t.Id }

// GetName returns the routing name of the command.
func (EditOption) GetName() string { return CommandEdit }

// EditFragment is a single replacement inside an edit command. OldString is the
// preferred locator; Start and End describe a 1 based line range and are either
// used as the locator, when OldString is empty, or as a verification of where
// OldString matched.
type EditFragment struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	Start      int    `json:"start,omitempty"`
	End        int    `json:"end,omitempty"`
	Content    string `json:"content,omitempty"`
}

// BashOption is the "bash" command parameter: it runs Content in Workdir. A zero
// Timeout means DefaultBashTimeout seconds.
type BashOption struct {
	Id      int64
	Reasoning string
	Content string
	Workdir string
	Timeout int
}

// GetId returns the invocation id that correlates the result with this call.
func (t BashOption) GetId() int64 { return t.Id }

// GetName returns the routing name of the command.
func (BashOption) GetName() string { return CommandBash }

// Options are handed to their handlers as pointers, which is what Parse and the
// constructors below produce.

// NewReadOption builds a read command.
func NewReadOption(id int64, filename string, start, end int) *ReadOption {
	return &ReadOption{Id: id, Filename: filename, Start: start, End: end}
}

// NewWriteOption builds a write command.
func NewWriteOption(id int64, filename, content string) *WriteOption {
	return &WriteOption{Id: id, Filename: filename, Content: content}
}

// NewEditOption builds an edit command.
func NewEditOption(id int64, filename string, contents []EditFragment) *EditOption {
	return &EditOption{Id: id, Filename: filename, Contents: contents}
}

// NewBashOption builds a bash command.
func NewBashOption(id int64, content, workdir string, timeout int) *BashOption {
	return &BashOption{Id: id, Content: content, Workdir: workdir, Timeout: timeout}
}
