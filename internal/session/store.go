// Package session 提供 agent 会话的持久化，允许对话跨运行保存、加载、列出和删除。
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/utils"
)

const (
	sessionsDir = "sessions"
	sessionExt  = ".json"
)

// SessionMeta 保存会话的摘要信息，用于列表展示。
type SessionMeta struct {
	Id           int64  `json:"id"`
	Title        string `json:"title"`
	ProjectPath  string `json:"project_path"`
	MessageCount int    `json:"message_count"`
	TotalTokens  int    `json:"total_tokens"`
	CreateTime   int64  `json:"create_time"`
	UpdateTime   int64  `json:"update_time"`
}

// sessionFile 是磁盘存储格式，嵌入 SessionMeta 并包含完整消息。
type sessionFile struct {
	Id          int64             `json:"id"`
	Title       string            `json:"title"`
	ProjectPath string            `json:"project_path"`
	Provider    llm.ModelInfo     `json:"provider"`
	TotalUsage  llm.Usage         `json:"total_usage"`
	Messages    []llm.ChatMessage `json:"messages"`
	CreateTime  int64             `json:"create_time"`
	UpdateTime  int64             `json:"update_time"`
	OtterState  *llm.OtterState   `json:"otter_state,omitempty"`
}

// getProjectSessionDir 返回当前项目的会话目录。
func getProjectSessionDir() string {
	projectPath := utils.GetCurrentPath()
	return filepath.Join(projectPath, ".orca", sessionsDir)
}

// getHomeSessionDir 返回用户主目录中的会话目录。
func getHomeSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orca", sessionsDir)
}

// ensureDir 在目录不存在时创建它。
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

// sessionPath 返回给定目录中会话文件的完整路径。
// id 使用十进制转换为文件名，确保不可能进行路径穿越。
func sessionPath(dir string, id int64) string {
	// 使用 FormatInt，它只产生数字，防止路径穿越。
	filename := strconv.FormatInt(id, 10) + sessionExt
	return filepath.Join(dir, filepath.Base(filename))
}

// Save 将会话持久化到项目级会话目录。
func Save(s *agent.Session) error {
	dir := getProjectSessionDir()
	if err := ensureDir(dir); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	file := sessionFile{
		Id:          s.Id,
		Title:       s.Title,
		ProjectPath: s.ProjectPath,
		Provider:    s.Provider,
		TotalUsage:  s.TotalUsage,
		Messages:    s.Messages,
		CreateTime:  s.CreateTime,
		UpdateTime:  s.UpdateTime,
		OtterState:  s.OtterState,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	path := sessionPath(dir, s.Id)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}

	return nil
}

// Load 从项目目录或主目录读取会话。
// 项目会话优先于主目录会话。
func Load(id int64) (*agent.Session, error) {
	// 先尝试项目目录
	if path := sessionPath(getProjectSessionDir(), id); utils.Exists(path) {
		return loadFromFile(path)
	}

	// 尝试主目录
	if homeDir := getHomeSessionDir(); homeDir != "" {
		if path := sessionPath(homeDir, id); utils.Exists(path) {
			return loadFromFile(path)
		}
	}

	return nil, fmt.Errorf("session %d not found", id)
}

// loadFromFile 从文件读取并解析会话。
func loadFromFile(path string) (*agent.Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	var file sessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse session file: %w", err)
	}

	session := &agent.Session{
		Title:       file.Title,
		Id:          file.Id,
		ProjectPath: file.ProjectPath,
		Provider:    file.Provider,
		TotalUsage:  file.TotalUsage,
		Messages:    file.Messages,
		CreateTime:  file.CreateTime,
		UpdateTime:  file.UpdateTime,
		OtterState:  file.OtterState,
	}

	return session, nil
}

// List 返回项目目录和主目录中所有会话的元数据。
// 结果按更新时间排序，最近的在前。
func List() []SessionMeta {
	var metas []SessionMeta
	seen := make(map[int64]bool)

	// 从项目目录收集（较高优先级）
	projectMetas := listFromDir(getProjectSessionDir())
	for _, m := range projectMetas {
		if !seen[m.Id] {
			metas = append(metas, m)
			seen[m.Id] = true
		}
	}

	// 从主目录收集
	homeMetas := listFromDir(getHomeSessionDir())
	for _, m := range homeMetas {
		if !seen[m.Id] {
			metas = append(metas, m)
			seen[m.Id] = true
		}
	}

	// 按更新时间排序，最近的在前
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdateTime > metas[j].UpdateTime
	})

	return metas
}

// listFromDir 从目录读取所有会话文件并返回其元数据。
func listFromDir(dir string) []SessionMeta {
	var metas []SessionMeta

	entries, err := os.ReadDir(dir)
	if err != nil {
		return metas // 目录不存在或无法读取
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionExt) {
			continue
		}

		// 验证文件名是有效的会话 ID
		idStr := strings.TrimSuffix(entry.Name(), sessionExt)
		if _, err := strconv.ParseInt(idStr, 10, 64); err != nil {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var file sessionFile
		if err := json.Unmarshal(data, &file); err != nil {
			continue
		}

		title := file.Title
		if title == "" {
			title = firstUserMessage(file.Messages)
		}

		metas = append(metas, SessionMeta{
			Id:           file.Id,
			Title:        title,
			ProjectPath:  file.ProjectPath,
			MessageCount: len(file.Messages),
			TotalTokens:  file.TotalUsage.TotalTokens,
			CreateTime:   file.CreateTime,
			UpdateTime:   file.UpdateTime,
		})
	}

	return metas
}

// Delete 从项目目录和主目录中删除会话。
func Delete(id int64) error {
	var found bool

	// 尝试项目目录
	if path := sessionPath(getProjectSessionDir(), id); utils.Exists(path) {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("delete session from project: %w", err)
		}
		found = true
	}

	// 尝试主目录
	if homeDir := getHomeSessionDir(); homeDir != "" {
		if path := sessionPath(homeDir, id); utils.Exists(path) {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("delete session from home: %w", err)
			}
			found = true
		}
	}

	if !found {
		return fmt.Errorf("session %d not found", id)
	}

	return nil
}

// DeleteAll 从项目目录和主目录中删除所有会话。
func DeleteAll() error {
	var errs []string

	// 清除项目会话
	if err := clearDir(getProjectSessionDir()); err != nil {
		errs = append(errs, fmt.Sprintf("project: %v", err))
	}

	// 清除主目录会话
	if homeDir := getHomeSessionDir(); homeDir != "" {
		if err := clearDir(homeDir); err != nil {
			errs = append(errs, fmt.Sprintf("home: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors deleting sessions: %s", strings.Join(errs, "; "))
	}

	return nil
}

// clearDir 从目录中删除所有会话文件。
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // 目录不存在，无需清除
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionExt) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			return err
		}
	}

	return nil
}

// firstUserMessage 从消息列表中提取第一条用户消息的前 30 个字符作为 fallback 标题。
func firstUserMessage(msgs []llm.ChatMessage) string {
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.TrimSpace(m.Content) != "" {
			line := strings.SplitN(strings.TrimSpace(m.Content), "\n", 2)[0]
			runes := []rune(strings.TrimSpace(line))
			if len(runes) > 30 {
				return string(runes[:27]) + "..."
			}
			return string(runes)
		}
	}
	return ""
}
