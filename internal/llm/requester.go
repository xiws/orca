package llm

// API type values accepted by the "api" field of models.json.
const (
	// APIOtter reaches ChatGPT, Gemini and DeepSeek through the otter platform
	// providers (github.com/xiws/otter) instead of an OpenAI-compatible HTTP
	// endpoint. Models served this way declare supportsTools=false: web
	// platforms have no native function calling, so the commands travel in
	// the prompt text instead.
	APIOtter = "otter"
)

// NewRequester builds the client for a resolved model based on its api type.
// Unknown api values fall back to the OpenAI-compatible client.
func NewRequester(info ModelInfo) Requester {
	switch info.API {
	case APIOtter:
		return NewOtterRequester(info)
	default:
		return NewOpenAIRequester(info)
	}
}
