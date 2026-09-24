package llm

import (
	"github.com/xiws/orca/pkg/utils"
)

var (
	// MODEL_FILE 是模型配置文件的文件名，从用户主目录或项目目录中加载。
	MODEL_FILE = "models.json"
)

// ModelInfo 描述一个具体模型及其 provider 的连接详情。
type ModelInfo struct {
	Provider      string // models.json 中的 provider 键，例如 "ollama"
	Name          string // provider 显示名称，例如 "Ollama (Local)"
	API           string // api 类型，例如 "openai-completions" 或 "otter"
	BaseURL       string
	APIKey        string
	ModelID       string // 调用 api 时使用的模型 id
	ContextWindow int
	SupportsTools bool
	Reasoning     bool
	AllowedTools  []string // 模式层限制可用工具列表，nil 表示全部可用
}

// fileModel 是 models.json 中每个模型的存储格式。
type fileModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	SupportsTools bool   `json:"supportsTools"`
	Reasoning     bool   `json:"reasoning"`
}

// fileProvider 是 models.json 中每个 provider 的存储格式。
type fileProvider struct {
	Name    string      `json:"name"`
	API     string      `json:"api"`
	BaseURL string      `json:"baseUrl"`
	APIKey  string      `json:"apiKey"`
	Models  []fileModel `json:"models"`
}

// modelsFile 是 models.json 的根文档。
type modelsFile struct {
	Providers map[string]fileProvider `json:"providers"`
}

// GetProvider 将 providerName 和 modelId 解析为 ModelInfo，
// 将模型属性与 provider 的连接详情合并。
//
// 当 provider 或模型找不到时返回零值 ModelInfo。
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

// loadProviders 从用户主目录和项目目录读取 models.json，
// 合并它们，使项目级条目优先于主目录级条目。
func loadProviders() map[string]fileProvider {
	merged := make(map[string]fileProvider)
	// 低优先级在前（~），高优先级在后（项目），以便覆盖。

	var file modelsFile
	if err := utils.GetModel(MODEL_FILE, &file); err != nil {
		panic(err)
	}

	for name, provider := range file.Providers {
		merged[name] = provider
	}

	return merged
}
