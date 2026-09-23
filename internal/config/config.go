// Package config loads explicit workspace configuration without package-init IO.
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

type settings struct {
	DefaultProvider string  `json:"defaultProvider"`
	DefaultModel    string  `json:"defaultModel"`
	SystemPrompt    *string `json:"systemPrompt"`
}
type fileModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	SupportsTools bool   `json:"supportsTools"`
}
type fileProvider struct {
	Name    string      `json:"name"`
	API     string      `json:"api"`
	BaseURL string      `json:"baseUrl"`
	APIKey  string      `json:"apiKey"`
	Models  []fileModel `json:"models"`
}

// All loaded data is private and JSON serialization yields {}. There is no
// save API: legacy literal credentials are read-only, and ${VAR} references
// are expanded only for Resolve, not cached or persisted.
type Config struct {
	workspace     string
	settings      settings
	providers     map[string]fileProvider
	prompts       map[model.Ref]string
	defaultPrompt string
}

var _ providers.SecretResolver = (*Config)(nil)

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
	dirs := []string{filepath.Join(home, ".orca")}
	local := filepath.Join(root, ".orca")
	if local != dirs[0] {
		dirs = append(dirs, local)
	}
	systemPrompt := "You are a helpful coding assistant.\nProject Path: {{.ProjectPath}}\nModel context length: {{.ContextLength}} tokens."
	otterPrompt := systemPrompt
	for _, dir := range dirs {
		for _, name := range []string{"setting.json", "settings.json"} {
			if _, err := readJSON(filepath.Join(dir, name), &cfg.settings); err != nil {
				return nil, err
			}
		}
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
			// Unmarshal an overlay into the previous value so absent fields inherit.
			// Merge model entries by ID as well; explicit false/zero override defaults.
			overlay := previous
			overlay.Models = nil // decoding must not reuse the inherited slice storage
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
	if cfg.settings.SystemPrompt != nil {
		systemPrompt = *cfg.settings.SystemPrompt
		otterPrompt = systemPrompt
	}
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

func (c *Config) DefaultModel() model.Ref {
	if c == nil {
		return model.Ref{}
	}
	return model.Ref{Provider: c.settings.DefaultProvider, Model: c.settings.DefaultModel}
}
func (c *Config) SystemPrompt(ref model.Ref) string {
	if c == nil {
		return ""
	}
	if prompt, ok := c.prompts[ref]; ok {
		return prompt
	}
	return c.defaultPrompt
}

var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

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
	var envErr error
	key := envReference.ReplaceAllStringFunc(p.APIKey, func(ref string) string {
		name := ref[2 : len(ref)-1]
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			envErr = fmt.Errorf("config: API key environment variable %s is unset or empty", name)
		}
		return value
	})
	// Check the reference syntax, not the expanded secret (which may contain $).
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
