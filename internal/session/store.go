// Package session provides persistence for agent sessions, allowing conversations
// to be saved, loaded, listed and deleted across runs.
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

// SessionMeta holds summary information about a session for listing purposes.
type SessionMeta struct {
	Id           int64  `json:"id"`
	ProjectPath  string `json:"project_path"`
	MessageCount int    `json:"message_count"`
	TotalTokens  int    `json:"total_tokens"`
	CreateTime   int64  `json:"create_time"`
	UpdateTime   int64  `json:"update_time"`
}

// sessionFile is the on-disk format, embedding SessionMeta with full messages.
type sessionFile struct {
	Id          int64             `json:"id"`
	ProjectPath string            `json:"project_path"`
	Provider    llm.ModelInfo     `json:"provider"`
	TotalUsage  llm.Usage         `json:"total_usage"`
	Messages    []llm.ChatMessage `json:"messages"`
	CreateTime  int64             `json:"create_time"`
	UpdateTime  int64             `json:"update_time"`
	OtterState  *llm.OtterState   `json:"otter_state,omitempty"`
}

// getProjectSessionDir returns the session directory for the current project.
func getProjectSessionDir() string {
	projectPath := utils.GetCurrentPath()
	return filepath.Join(projectPath, ".orca", sessionsDir)
}

// getHomeSessionDir returns the session directory in user home.
func getHomeSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orca", sessionsDir)
}

// ensureDir creates the directory if it does not exist.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

// sessionPath returns the full path for a session file in the given directory.
// The id is converted to a filename using base-10 encoding, ensuring no path
// traversal is possible.
func sessionPath(dir string, id int64) string {
	// Use FormatInt which only produces digits, preventing path traversal
	filename := strconv.FormatInt(id, 10) + sessionExt
	return filepath.Join(dir, filepath.Base(filename))
}

// Save persists a session to the project-level session directory.
func Save(s *agent.Session) error {
	dir := getProjectSessionDir()
	if err := ensureDir(dir); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	file := sessionFile{
		Id:          s.Id,
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

// Load reads a session from either project or home directory.
// Project sessions take priority over home sessions.
func Load(id int64) (*agent.Session, error) {
	// Try project directory first
	if path := sessionPath(getProjectSessionDir(), id); utils.Exists(path) {
		return loadFromFile(path)
	}

	// Try home directory
	if homeDir := getHomeSessionDir(); homeDir != "" {
		if path := sessionPath(homeDir, id); utils.Exists(path) {
			return loadFromFile(path)
		}
	}

	return nil, fmt.Errorf("session %d not found", id)
}

// loadFromFile reads and parses a session from a file.
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

// List returns metadata for all sessions from both project and home directories.
// Results are sorted by update time, most recent first.
func List() []SessionMeta {
	var metas []SessionMeta
	seen := make(map[int64]bool)

	// Collect from project directory (higher priority)
	projectMetas := listFromDir(getProjectSessionDir())
	for _, m := range projectMetas {
		if !seen[m.Id] {
			metas = append(metas, m)
			seen[m.Id] = true
		}
	}

	// Collect from home directory
	homeMetas := listFromDir(getHomeSessionDir())
	for _, m := range homeMetas {
		if !seen[m.Id] {
			metas = append(metas, m)
			seen[m.Id] = true
		}
	}

	// Sort by update time, most recent first
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdateTime > metas[j].UpdateTime
	})

	return metas
}

// listFromDir reads all session files from a directory and returns their metadata.
func listFromDir(dir string) []SessionMeta {
	var metas []SessionMeta

	entries, err := os.ReadDir(dir)
	if err != nil {
		return metas // Directory doesn't exist or can't be read
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionExt) {
			continue
		}

		// Validate filename is a valid session ID
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

		metas = append(metas, SessionMeta{
			Id:           file.Id,
			ProjectPath:  file.ProjectPath,
			MessageCount: len(file.Messages),
			TotalTokens:  file.TotalUsage.TotalTokens,
			CreateTime:   file.CreateTime,
			UpdateTime:   file.UpdateTime,
		})
	}

	return metas
}

// Delete removes a session from both project and home directories.
func Delete(id int64) error {
	var found bool

	// Try project directory
	if path := sessionPath(getProjectSessionDir(), id); utils.Exists(path) {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("delete session from project: %w", err)
		}
		found = true
	}

	// Try home directory
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

// DeleteAll removes all sessions from both project and home directories.
func DeleteAll() error {
	var errs []string

	// Clear project sessions
	if err := clearDir(getProjectSessionDir()); err != nil {
		errs = append(errs, fmt.Sprintf("project: %v", err))
	}

	// Clear home sessions
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

// clearDir removes all session files from a directory.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // Directory doesn't exist, nothing to clear
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
