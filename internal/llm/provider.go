package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ModelInfo describes a concrete model together with the connection details of
// the provider that serves it.
type ModelInfo struct {
	Provider      string // provider key in models.json, e.g. "ollama"
	Name          string // provider display name, e.g. "Ollama (Local)"
	API           string // api type, e.g. "openai-completions"
	BaseURL       string
	APIKey        string
	ModelID       string // model id used when calling the api
	ContextWindow int
	SupportsTools bool
	Reasoning     bool
}

// fileModel is a per-model entry as stored in models.json.
type fileModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	SupportsTools bool   `json:"supportsTools"`
	Reasoning     bool   `json:"reasoning"`
}

// fileProvider is a provider entry as stored in models.json.
type fileProvider struct {
	Name    string      `json:"name"`
	API     string      `json:"api"`
	BaseURL string      `json:"baseUrl"`
	APIKey  string      `json:"apiKey"`
	Models  []fileModel `json:"models"`
}

// modelsFile is the root document of models.json.
type modelsFile struct {
	Providers map[string]fileProvider `json:"providers"`
}

// GetProvider resolves providerName and modelId into a ModelInfo, merging the
// model properties with the provider's connection details.
//
// A zero ModelInfo is returned when the provider or the model cannot be found.
func GetProvider(providerName, modelId string) ModelInfo {
	provider, ok := loadProviders()[providerName]
	if !ok {
		return ModelInfo{}
	}
	for _, m := range provider.Models {
		if m.ID != modelId {
			continue
		}
		return ModelInfo{
			Provider:      providerName,
			Name:          provider.Name,
			API:           provider.API,
			BaseURL:       provider.BaseURL,
			APIKey:        provider.APIKey,
			ModelID:       m.ID,
			ContextWindow: m.ContextWindow,
			SupportsTools: m.SupportsTools,
			Reasoning:     m.Reasoning,
		}
	}
	return ModelInfo{}
}

// loadProviders reads models.json from the user home and the project directory,
// merging them so that project-level entries win over home-level ones.
func loadProviders() map[string]fileProvider {
	merged := make(map[string]fileProvider)
	// Lower priority first (~), higher priority last (project) so it overwrites.
	for _, path := range modelsSearchPaths() {
		for name, provider := range readModelsFile(path) {
			merged[name] = provider
		}
	}
	return merged
}

// modelsSearchPaths returns models.json candidates ordered from lowest to
// highest priority: ~/.orca/models.json then {project}/.orca/models.json.
func modelsSearchPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".orca", "models.json"))
	}
	if wd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(wd, ".orca", "models.json"))
	}
	return paths
}

// readModelsFile parses a single models.json file, returning nil when it is
// missing or malformed so that callers can silently fall back to other sources.
func readModelsFile(path string) map[string]fileProvider {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file modelsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil
	}
	return file.Providers
}
