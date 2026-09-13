package llm

// models.json 的 "api" 字段接受的 API 类型值。
const (
	// APIOtter 通过 otter 平台 provider（github.com/xiws/otter）
	// 访问 ChatGPT、Gemini 和 DeepSeek，而非 OpenAI 兼容的 HTTP 端点。
	// 以此方式提供的模型声明 supportsTools=false：Web 平台没有原生函数调用，
	// 因此命令通过提示文本传递。
	APIOtter = "otter"
)

// NewRequester 根据 api 类型为解析后的模型构建客户端。
// 未知的 api 值回退到 OpenAI 兼容客户端。
func NewRequester(info ModelInfo) Requester {
	switch info.API {
	case APIOtter:
		return NewOtterRequester(info)
	default:
		return NewOpenAIRequester(info)
	}
}
