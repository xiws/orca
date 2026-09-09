package handler

import (
	"errors"
	"fmt"
	"orca/internal/event"
	"orca/pkg/command"
	"strconv"
	"strings"

	"github.com/zbysir/hunkpatch"
)

// EditHandler serves the edit command.
type EditHandler struct {
	Workspace
}

// Handle applies the fragments of an edit command one after another.
//
// The whole result is computed in memory first, so a fragment that cannot be
// located fails the command without touching the file.
func (t EditHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*EditOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandEdit), nil
	}
	var shell = t.getShell(opt)
	if t.Publisher != nil {
		t.Publisher.Publish(event.NewToolBeforeEvent(shell, opt.Reasoning, opt.Id))
	}
	summary, err := t.edit(opt)
	if t.Publisher != nil {
		t.Publisher.Publish(event.NewToolAfterEvent(shell, summary, opt.Id))
	}
	return nil, ResultFor(opt, summary, err)
}

func (t EditHandler) edit(opt *EditOption) (string, error) {
	if len(opt.Contents) == 0 {
		return "", ErrEmptyContents
	}
	original, err := readAll(t.Workspace, opt.Filename)
	if err != nil {
		return "", err
	}

	updated := original
	summaries := make([]string, 0, len(opt.Contents))
	for i, fragment := range opt.Contents {
		next, summary, err := applyFragment(updated, i+1, fragment)
		if err != nil {
			return "", err
		}
		updated = next
		summaries = append(summaries, summary)
	}

	resolved, err := t.Resolve(opt.Filename)
	if err != nil {
		return "", err
	}
	if updated == original {
		return fmt.Sprintf("%s unchanged", resolved), nil
	}
	if _, err := writeAll(t.Workspace, resolved, updated); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s: %s (%d -> %d lines)",
		resolved, strings.Join(summaries, ", "), len(splitLines(original)), len(splitLines(updated))), nil
}

func (t EditHandler) getShell(opt *EditOption) string {
	if opt == nil {
		return ""
	}

	args := []string{"edit", opt.Filename}

	for _, fragment := range opt.Contents {
		if fragment.Diff != "" {
			oneLine := strings.ReplaceAll(fragment.Diff, "\n", "\\n")
			args = append(args, "--diff", oneLine)
		}

		if fragment.Content != "" {
			args = append(args, "--content", fragment.Content)
		}

		if fragment.Start != 0 {
			args = append(args, "--start", strconv.Itoa(fragment.Start))
		}

		if fragment.End != 0 {
			args = append(args, "--end", strconv.Itoa(fragment.End))
		}
	}

	return strings.Join(args, " ")
}

// applyFragment applies one replacement to content and returns the new content
// plus a one line summary. index is the 1 based position of the fragment, used
// to point failures at the right entry.
func applyFragment(content string, index int, fragment EditFragment) (string, string, error) {
	switch {
	case fragment.Diff != "":
		return applyDiffFragment(content, index, fragment)
	case fragment.Start > 0:
		return applyLineFragment(content, index, fragment)
	default:
		return "", "", fmt.Errorf("%w: fragment %d", ErrFragmentLocator, index)
	}
}

// applyDiffFragment applies a unified diff to content using hunkpatch's fuzzy
// matching algorithm, which tolerates incorrect line numbers and approximate
// surrounding context — exactly what language models produce.
func applyDiffFragment(content string, index int, fragment EditFragment) (string, string, error) {
	opts := hunkpatch.Options{IndentTolerant: true}
	result, err := hunkpatch.ApplyWith(content, fragment.Diff, opts)
	if err != nil {
		var partial *hunkpatch.PartialError
		if errors.As(err, &partial) {
			if partial.Applied == 0 {
				return "", "", fmt.Errorf("fragment %d: diff did not match any content (%d hunks skipped)", index, partial.Total)
			}
			return "", "", fmt.Errorf("fragment %d: only applied %d/%d hunks", index, partial.Applied, partial.Total)
		}
		if errors.Is(err, hunkpatch.ErrNoHunks) {
			return "", "", fmt.Errorf("fragment %d: no hunks found in diff", index)
		}
		return "", "", fmt.Errorf("fragment %d: %w", index, err)
	}
	if result.Applied == 0 {
		// Every hunk left the text unchanged (e.g. old == new, or anchor-only).
		return content, "no hunks applied (content unchanged)", nil
	}
	return result.Text, fmt.Sprintf("applied %d hunk(s)", result.Applied), nil
}

// applyLineFragment replaces the inclusive 1 based line range Start to End with
// Content.
func applyLineFragment(content string, index int, fragment EditFragment) (string, string, error) {
	lines := splitLines(content)
	if fragment.Start > len(lines) {
		return "", "", fmt.Errorf("fragment %d: start line %d is beyond the end of the file (%d lines)", index, fragment.Start, len(lines))
	}
	end := fragment.End
	if end <= 0 {
		end = fragment.Start
	}
	if end > len(lines) {
		return "", "", fmt.Errorf("fragment %d: end line %d is beyond the end of the file (%d lines)", index, end, len(lines))
	}

	updated := make([]string, 0, len(lines))
	updated = append(updated, lines[:fragment.Start-1]...)
	updated = append(updated, splitLines(fragment.Content)...)
	updated = append(updated, lines[end:]...)
	return joinLines(updated, detectNewline(content), strings.HasSuffix(content, "\n")),
		fmt.Sprintf("replaced lines %d-%d with %d line(s)", fragment.Start, end, len(splitLines(fragment.Content))), nil
}

// detectNewline returns the line terminator used by content, so an edit never
// rewrites the line ending style of an existing file.
func detectNewline(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// joinLines glues lines back together, keeping the trailing terminator only
// when the original content had one.
func joinLines(lines []string, newline string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, newline)
	if trailingNewline {
		joined += newline
	}
	return joined
}
