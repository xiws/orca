package handler

import (
	"errors"
	"fmt"
	"github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/pkg/command"
	"strings"

	"github.com/zbysir/hunkpatch"
)

// EditHandler 服务 edit 命令。
type EditHandler struct {
	Workspace
}

// Handle 逐个应用 edit 命令的片段。
//
// 整个结果先在内存中计算，因此无法定位的片段会使命令失败
// 而不触及文件。
func (t EditHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*EditOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandEdit), nil
	}
	meta := t.buildMeta(opt)
	publish(t.Publisher, event.NewToolBeforeEvent("edit", opt.Filename, meta, opt.Reasoning, opt.Id))
	summary, err := t.edit(opt)
	okStatus := err == nil
	// After-event 成功时携带摘要，失败时为空。
	publish(t.Publisher, event.NewToolAfterEvent("edit", opt.Filename, "", okStatus, summary, 0, opt.Id))
	return nil, ResultFor(opt, summary, err)
}

// edit 依次应用所有 diff 片段到文件内容，全部成功后写回文件。
func (t EditHandler) edit(opt *EditOption) (string, error) {
	if len(opt.Contents) == 0 {
		return "", ErrEmptyContents
	}
	original, err := readAll(t.Workspace, opt.Filename)
	if err != nil {
		return "", err
	}

	// 依次应用每个 diff 片段，每个片段基于前一个的结果。
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

// buildMeta 返回 edit 操作的上下文描述。
func (t EditHandler) buildMeta(opt *EditOption) string {
	if opt == nil || len(opt.Contents) == 0 {
		return "0 fragments"
	}
	return fmt.Sprintf("%d fragment(s)", len(opt.Contents))
}

// applyFragment 将单个替换应用到 content，返回新内容和单行摘要。
// index 是片段的 1 起始位置，用于指向失败的条目。
func applyFragment(content string, index int, fragment EditFragment) (string, string, error) {
	if fragment.Diff == "" {
		return "", "", fmt.Errorf("%w: fragment %d", ErrFragmentLocator, index)
	}
	return applyDiffFragment(content, index, fragment)
}

// applyDiffFragment 使用 hunkpatch 的模糊匹配算法将统一 diff 应用到 content，
// 容忍不正确的行号和近似的上下文——正是语言模型产生的内容。
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
