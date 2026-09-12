package handler

import (
	"errors"
	"fmt"
	"github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/pkg/command"
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
	meta := t.buildMeta(opt)
	publish(t.Publisher, event.NewToolBeforeEvent("edit", opt.Filename, meta, opt.Reasoning, opt.Id))
	summary, err := t.edit(opt)
	okStatus := err == nil
	publish(t.Publisher, event.NewToolAfterEvent("edit", opt.Filename, "", okStatus, summary, 0, opt.Id))
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

// buildMeta returns the contextual description for an edit operation.
func (t EditHandler) buildMeta(opt *EditOption) string {
	if opt == nil || len(opt.Contents) == 0 {
		return "0 fragments"
	}
	return fmt.Sprintf("%d fragment(s)", len(opt.Contents))
}

// applyFragment applies one replacement to content and returns the new content
// plus a one line summary. index is the 1 based position of the fragment, used
// to point failures at the right entry.
func applyFragment(content string, index int, fragment EditFragment) (string, string, error) {
	if fragment.Diff == "" {
		return "", "", fmt.Errorf("%w: fragment %d", ErrFragmentLocator, index)
	}
	return applyDiffFragment(content, index, fragment)
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
		return content, "no hunks applied (content unchanged)", nil
	}
	return result.Text, fmt.Sprintf("applied %d hunk(s)", result.Applied), nil
}
