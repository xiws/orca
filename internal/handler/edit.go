package handler

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"orca/pkg/command"
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
	summary, err := t.edit(opt)
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

// applyFragment applies one replacement to content and returns the new content
// plus a one line summary. index is the 1 based position of the fragment, used
// to point failures at the right entry.
func applyFragment(content string, index int, fragment EditFragment) (string, string, error) {
	switch {
	case fragment.OldString != "":
		return applyStringFragment(content, index, fragment)
	case fragment.Start > 0:
		return applyLineFragment(content, index, fragment)
	default:
		return "", "", fmt.Errorf("%w: fragment %d", ErrFragmentLocator, index)
	}
}

// applyStringFragment replaces a uniquely matching old_string.
func applyStringFragment(content string, index int, fragment EditFragment) (string, string, error) {
	matches := strings.Count(content, fragment.OldString)
	if matches == 0 {
		return "", "", fmt.Errorf("fragment %d: old_string %s not found", index, quote(fragment.OldString))
	}
	if matches > 1 && !fragment.ReplaceAll {
		return "", "", fmt.Errorf("fragment %d: old_string %s matches %d times, extend it or set replace_all", index, quote(fragment.OldString), matches)
	}
	if fragment.Start > 0 {
		if err := verifyLineRange(content, fragment); err != nil {
			return "", "", fmt.Errorf("fragment %d: %w", index, err)
		}
	}

	times := 1
	if fragment.ReplaceAll {
		times = -1
	}
	updated := strings.Replace(content, fragment.OldString, fragment.NewString, times)
	return updated, fmt.Sprintf("replaced %d occurrence(s) of %s", matches, quote(fragment.OldString)), nil
}

// verifyLineRange checks that the lines a caller expected are the lines where
// old_string actually matched.
func verifyLineRange(content string, fragment EditFragment) error {
	start, end, err := matchLineRange(content, fragment.OldString)
	if err != nil {
		return err
	}
	if fragment.End <= 0 {
		fragment.End = fragment.Start
	}
	if start != fragment.Start || end != fragment.End {
		return fmt.Errorf("old_string matches lines %d-%d, not %d-%d", start, end, fragment.Start, fragment.End)
	}
	return nil
}

// matchLineRange returns the 1 based inclusive line range covered by the first
// occurrence of old in content.
func matchLineRange(content, old string) (int, int, error) {
	at := strings.Index(content, old)
	if at < 0 {
		return 0, 0, fmt.Errorf("old_string %s not found", quote(old))
	}
	start := 1 + strings.Count(content[:at], "\n")
	return start, start + strings.Count(old, "\n"), nil
}

// applyLineFragment replaces the inclusive 1 based line range Start to End with
// Content, which is only meant for callers that already know the line numbers.
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

// quote makes a fragment readable inside an error message.
func quote(s string) string {
	oneLine := strings.ReplaceAll(s, "\n", `\n`)
	if utf8.RuneCountInString(oneLine) > 40 {
		oneLine = string([]rune(oneLine)[:40]) + "..."
	}
	return fmt.Sprintf("%q", oneLine)
}
