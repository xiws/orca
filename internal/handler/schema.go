package handler

import (
	"fmt"
	"strings"
)

// ToolPrompt returns the description of the command protocol for the tool part
// of a prompt. It is generated from the same constants the parser accepts, so
// the model is never told about a command that cannot run.
func ToolPrompt() string {
	var out strings.Builder
	fmt.Fprintf(&out, "You can act on the workspace by replying with one or more of these JSON commands: %s.\n",
		strings.Join(quoteAll(Commands()), ", "))
	out.WriteString(ToolSchema)
	out.WriteString(fmt.Sprintf(
		"\nA read returns at most %d lines per call, a bash command runs for at most %d seconds and its output is cut at %d bytes.\n",
		MaxReadLines, DefaultBashTimeout, MaxBashOutput))
	out.WriteString("Every command takes an optional numeric \"id\", which its result repeats so the two can be matched; leave it out and one is generated.\n")
	out.WriteString("Prefer edit over write when a file already exists: write rebuilds the whole file and discards everything it is not given.")
	return out.String()
}

// ToolSchema documents the accepted shape of every command. Parameters may be
// placed next to "command" or nested in "data".
const ToolSchema = `
{ "command": "read",  "id": 1, "filename": "/abs/or/relative/path", "start": 1, "end": 200 }
    Returns the line range with 1 based line numbers. Omit start and end for the whole file.

{ "command": "write", "id": 2, "data": { "filename": "/abs/or/relative/path", "content": "full new file" } }
    Rebuilds the file from scratch, creating missing parent directories. Everything not in content is lost.

{ "command": "edit",  "id": 3, "data": { "filename": "/abs/or/relative/path", "contents": [
    { "diff": "@@\n- old line\n+ new line" },
    { "diff": "@@\n- another old line\n+ another new line" }
] } }
    Applies the fragments in order and only writes the file if all of them match.
    diff is a unified diff applied by hunkpatch — line numbers are ignored,
    matching is content-based and tolerant of model imprecision.

{ "command": "bash",  "id": 4, "data": { "content": "ls -a", "workdir": "relative/or/absolute/dir", "timeout": 60 } }
    Runs the command line in a shell and returns stdout and stderr merged. timeout is in seconds.

{ "command": "create_task", "id": 5, "data": { "task_target": [
    { "title": "short sub-task title", "description": "what the sub-task must achieve" }
] } }
    Decomposes the goal into sub-tasks and runs them; task_target is an array of
    sub-tasks, each carrying a title and a description.
`

func quoteAll(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return quoted
}
