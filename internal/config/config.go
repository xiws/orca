// Package config 加载工作空间配置，支持多层配置合并（全局 ~/.orca/ 和项目 .orca/）。
// 不包含包级别的 init IO 操作。
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/providers"
)

// settings 从 setting.json/settings.json 加载的全局设置。
type settings struct {
	DefaultProvider string  `json:"defaultProvider"`
	DefaultModel    string  `json:"defaultModel"`
	SystemPrompt    *string `json:"systemPrompt"`
}

// fileModel 配置文件中单个模型的声明。
type fileModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	SupportsTools bool   `json:"supportsTools"`
}

// fileProvider 配置文件中的提供方声明，包含 API 类型、地址、密钥和模型列表。
type fileProvider struct {
	Name    string      `json:"name"`
	API     string      `json:"api"`
	BaseURL string      `json:"baseUrl"`
	APIKey  string      `json:"apiKey"`
	Models  []fileModel `json:"models"`
}

// Config 聚合加载后的完整配置。所有数据均为私有，JSON 序列化结果恒为 {}。
// 没有保存接口：遗留凭证只读，${VAR} 引用仅在 Resolve 时展开，不缓存也不持久化。
type Config struct {
	workspace     string                  // 工作空间路径
	settings      settings                // 全局设置
	providers     map[string]fileProvider // 提供方配置
	prompts       map[model.Ref]string    // 按模型索引的系统提示词
	defaultPrompt string                  // 默认系统提示词
}

var _ providers.SecretResolver = (*Config)(nil)

// Load 从全局和项目配置目录加载并合并配置。
func Load(workspace string) (*Config, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("config: explicit workspace required")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("config: resolve workspace: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("config: workspace: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("config: workspace is not a directory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("config: home directory: %w", err)
	}
	cfg := &Config{workspace: root, providers: map[string]fileProvider{}, prompts: map[model.Ref]string{}}
	// 配置目录优先级：全局 ~/.orca/ 然后项目 .orca/。
	dirs := []string{filepath.Join(home, ".orca")}
	local := filepath.Join(root, ".orca")
	if local != dirs[0] {
		dirs = append(dirs, local)
	}
	systemPrompt := "You are a helpful coding assistant.\nProject Path: {{.ProjectPath}}\nModel context length: {{.ContextLength}} tokens."
	otterPrompt := systemPrompt
	for _, dir := range dirs {
		// 加载设置文件（setting.json 和 settings.json）。
		for _, name := range []string{"setting.json", "settings.json"} {
			if _, err := readJSON(filepath.Join(dir, name), &cfg.settings); err != nil {
				return nil, err
			}
		}
		// 加载模型提供方配置。
		var file struct {
			Providers map[string]json.RawMessage `json:"providers"`
		}
		if _, err := readJSON(filepath.Join(dir, "models.json"), &file); err != nil {
			return nil, err
		}
		for name, raw := range file.Providers {
			if strings.TrimSpace(name) == "" || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
				return nil, errors.New("config: invalid provider entry")
			}
			previous := cfg.providers[name]
			// 合并叠加配置：缺失字段继承已有值，模型按 ID 合并。
			overlay := previous
			overlay.Models = nil // 解码时必须复用已有模型切片
			if err := json.Unmarshal(raw, &overlay); err != nil {
				return nil, errors.New("config: invalid provider configuration")
			}
			overlay.Models = append([]fileModel(nil), previous.Models...)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			if modelsRaw, ok := fields["models"]; ok {
				var models []json.RawMessage
				if !strings.HasPrefix(strings.TrimSpace(string(modelsRaw)), "[") || json.Unmarshal(modelsRaw, &models) != nil {
					return nil, errors.New("config: invalid models list")
				}
				seen := map[string]bool{}
				for _, entry := range models {
					var identity struct {
						ID string `json:"id"`
					}
					if json.Unmarshal(entry, &identity) != nil || identity.ID == "" || seen[identity.ID] {
						return nil, errors.New("config: invalid or duplicate model ID")
					}
					seen[identity.ID] = true
					// 按 ID 查找已有模型配置进行合并。
					index := -1
					var target fileModel
					for i, existing := range overlay.Models {
						if existing.ID == identity.ID {
							index = i
							target = existing
							break
						}
					}
					if json.Unmarshal(entry, &target) != nil || target.ContextWindow < 0 {
						return nil, errors.New("config: invalid model configuration")
					}
					if index < 0 {
						overlay.Models = append(overlay.Models, target)
					} else {
						overlay.Models[index] = target
					}
				}
			}
			cfg.providers[name] = overlay
		}
		// 加载系统提示词模板。
		if content, found, err := readPrompt(filepath.Join(dir, "system_prompt.md")); err != nil {
			return nil, err
		} else if found {
			systemPrompt = content
			otterPrompt = content
		}
		if content, found, err := readPrompt(filepath.Join(dir, "otter_prompt.md")); err != nil {
			return nil, err
		} else if found {
			otterPrompt = content
		}
	}
	// settings 中的 systemPrompt 优先级最高。
	if cfg.settings.SystemPrompt != nil {
		systemPrompt = *cfg.settings.SystemPrompt
		otterPrompt = systemPrompt
	}
	// 渲染提示词模板。
	render := func(source string, window int) (string, error) {
		tmpl, err := template.New("system").Option("missingkey=error").Parse(source)
		if err != nil {
			return "", errors.New("config: invalid system prompt template")
		}
		var out strings.Builder
		if err := tmpl.Execute(&out, struct {
			ProjectPath   string
			ContextLength int
		}{root, window}); err != nil {
			return "", errors.New("config: cannot render system prompt template")
		}
		return out.String(), nil
	}
	cfg.defaultPrompt, err = render(systemPrompt, 0)
	if err != nil {
		return nil, err
	}
	if _, err := render(otterPrompt, 0); err != nil {
		return nil, err
	}
	// 为每个提供方的每个模型预渲染系统提示词。
	for providerName, provider := range cfg.providers {
		if provider.API == "" {
			return nil, errors.New("config: provider API missing")
		}
		for _, m := range provider.Models {
			source := systemPrompt
			if provider.API == "otter" {
				source = otterPrompt
			}
			prompt, err := render(source, m.ContextWindow)
			if err != nil {
				return nil, err
			}
			cfg.prompts[model.Ref{Provider: providerName, Model: m.ID}] = prompt
		}
	}
	return cfg, nil
}

// readJSON 读取并校验 JSON 文件，文件不存在时返回 false。
func readJSON(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: read %s: %w", path, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") || model.StrictJSON(data) != nil || json.Unmarshal(data, dst) != nil {
		return false, fmt.Errorf("config: invalid JSON in %s", path)
	}
	return true, nil
}

// readPrompt 读取提示词文件，文件不存在时返回 false。
func readPrompt(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("config: read system prompt %s: %w", path, err)
	}
	return string(data), true, nil
}

// DefaultModel 返回配置的默认模型引用。
func (c *Config) DefaultModel() model.Ref {
	if c == nil {
		return model.Ref{}
	}
	return model.Ref{Provider: c.settings.DefaultProvider, Model: c.settings.DefaultModel}
}

// SystemPrompt 返回指定模型对应的系统提示词，未找到则返回默认提示词。
func (c *Config) SystemPrompt(ref model.Ref) string {
	if c == nil {
		return ""
	}
	if prompt, ok := c.prompts[ref]; ok {
		return prompt
	}
	return c.defaultPrompt
}

// envReference 匹配 ${VAR_NAME} 形式的环境变量引用。
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Resolve 解析指定模型的连接信息，展开 API Key 中的环境变量引用。
func (c *Config) Resolve(ctx context.Context, ref model.Ref) (providers.Connection, error) {
	if err := ctx.Err(); err != nil {
		return providers.Connection{}, err
	}
	if c == nil {
		return providers.Connection{}, errors.New("config: configuration not loaded")
	}
	p, ok := c.providers[ref.Provider]
	if !ok {
		return providers.Connection{}, errors.New("config: unknown provider")
	}
	// 查找指定模型。
	var found *fileModel
	for i := range p.Models {
		if p.Models[i].ID == ref.Model {
			found = &p.Models[i]
			break
		}
	}
	if found == nil {
		return providers.Connection{}, errors.New("config: unknown model for provider")
	}
	// 展开 API Key 中的 ${VAR} 引用。
	var envErr error
	key := envReference.ReplaceAllStringFunc(p.APIKey, func(ref string) string {
		name := ref[2 : len(ref)-1]
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			envErr = fmt.Errorf("config: API key environment variable %s is unset or empty", name)
		}
		return value
	})
	// 检查引用语法，而非展开后的密钥值（可能包含 $）。
	if strings.Contains(envReference.ReplaceAllString(p.APIKey, ""), "${") {
		envErr = errors.New("config: malformed API key environment reference")
	}
	if envErr != nil {
		return providers.Connection{}, envErr
	}
	if err := ctx.Err(); err != nil {
		return providers.Connection{}, err
	}
	return providers.Connection{API: p.API, BaseURL: p.BaseURL, APIKey: key, ContextWindow: found.ContextWindow, SupportsTools: found.SupportsTools}, nil
}
