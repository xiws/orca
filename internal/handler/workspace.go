package handler

import (
	"fmt"
	"github.com/xiws/orca/pkg/event"
	"os"
	"path/filepath"
	"strings"
)

// Workspace 将文件命令限定在项目目录范围内。命令只能操作
// 其中的文件；直接或间接通过符号链接解析到其他位置的路径
// 会在打开任何文件之前被拒绝。
type Workspace struct {
	// Root 是相对路径解析的基准目录，也是文件命令可修改的唯一目录树。
	// 空的 Root 回退到进程工作目录。
	Root      string
	Publisher event.EventPublisher
}

// Resolve 将来自命令的文件名转换为绝对路径。
//
// 绝对路径按原样使用，相对路径与 Root 拼接。结果被清理，
// 当 Root 设置时，在解析最近存在的祖先的符号链接后验证
// 保持在 Root 内，因此 ".." 段和符号链接文件都无法指向工作区外。
func (t Workspace) Resolve(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", ErrEmptyFilename
	}

	root, err := t.root()
	if err != nil {
		return "", err
	}

	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)

	canonical, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	// Root 本身可能位于符号链接后面，如 macOS 上的 /tmp，
	// 因此两边在比较前都必须规范化。
	canonicalRoot, err := canonicalPath(root)
	if err != nil {
		return "", err
	}
	if err := within(canonicalRoot, canonical); err != nil {
		return "", err
	}
	return path, nil
}

// root 返回绝对工作区根目录，未配置时回退到当前工作目录。
func (t Workspace) root() (string, error) {
	if t.Root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Clean(cwd), nil
	}
	root, err := filepath.Abs(t.Root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", t.Root, err)
	}
	return filepath.Clean(root), nil
}

// canonicalPath 解析 path 最深存在祖先的符号链接，并重新拼接剩余部分，
// 这让尚未创建的文件也可以被检查。
func canonicalPath(path string) (string, error) {
	existing, missing := splitAtExisting(path)
	resolved := existing
	if existing != "" {
		full, err := filepath.EvalSymlinks(existing)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", existing, err)
		}
		resolved = full
	}
	return filepath.Join(resolved, missing), nil
}

// splitAtExisting 从 path 向上遍历，直到找到存在的条目，
// 返回该条目和其下的尾段。
func splitAtExisting(path string) (existing, missing string) {
	rest := path
	for rest != "" && rest != string(filepath.Separator) {
		if _, err := os.Lstat(rest); err == nil {
			return rest, strings.TrimPrefix(strings.TrimPrefix(path, rest), string(filepath.Separator))
		}
		parent := filepath.Dir(rest)
		if parent == rest {
			break
		}
		rest = parent
	}
	return "", path
}

// within 报告 path 在两者都是绝对路径并清理后是否保持在 root 内。
// 符号链接逃逸由上游比较规范形式处理。
func within(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrOutsideWorkspace, path)
	}
	return nil
}

// publish 在配置了发布者时转发事件。处理器也可能在没有总线的情况下构建，
// nil 发布者应该静默不报而不是在命令中间崩溃。
func publish(publisher event.EventPublisher, ent event.Event) {
	if publisher == nil {
		return
	}
	_ = publisher.Publish(ent)
}
