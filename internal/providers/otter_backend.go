package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/xiws/otter/pkg/api"
	"github.com/xiws/otter/pkg/chrome"
	otterconfig "github.com/xiws/otter/pkg/config"
	otter "github.com/xiws/otter/pkg/provider"
)

// 本文件实现 otter 平台后端的构建和各平台 provider 的适配层。

// buildOtterBackend 根据平台名称构建对应的 otter 后端。
// 凭据的初始化方式与 otter CLI 完全一致，因此用任一工具执行的登录可同时服务两者。
func buildOtterBackend(ctx context.Context, platform string) (otterBackend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch platform {
	case "deepseek", "chatgpt", "gemini":
	default:
		return nil, errors.New("otter: unsupported platform")
	}
	// 拒绝依赖 debug 开关的配置，防止凭据被转储到磁盘或标准输出
	if err := safeOtterDebug(); err != nil {
		return nil, err
	}
	cfg, err := otterconfig.Load(otterconfig.ResolvePath("~/.otter/config.json"))
	if err != nil {
		return nil, errors.New("otter: cannot read local configuration")
	}
	switch platform {
	case "deepseek":
		ds := otter.NewDeepSeekProvider()
		if err := initDeepSeek(ctx, ds, cfg); err != nil {
			return nil, err
		}
		return ds, nil
	case "chatgpt":
		// 高级 provider 会写入提取/刷新的 cookie，使用底层 API 和内存 cookie
		cg := api.NewChatGPTAPI()
		modelName := cfg.ChatGPT.Model
		if modelName == "" {
			modelName = "auto"
		}
		cg.SetModel(modelName)
		cookies, _ := readOtterCookies(ctx, cfg.ChatGPT.CookiesPath, "chatgpt.com", "openai.com")
		if len(cookies) > 0 {
			cg.SetCookies(cookies)
		}
		switch {
		case os.Getenv("OTTER_CHATGPT_ACCESS_TOKEN") != "":
			cg.SetAccessToken(os.Getenv("OTTER_CHATGPT_ACCESS_TOKEN"))
		case os.Getenv("OTTER_CHATGPT_SESSION_TOKEN") != "":
			cg.SetSessionToken(os.Getenv("OTTER_CHATGPT_SESSION_TOKEN"))
		case cfg.ChatGPT.AuthToken != "":
			cg.SetAccessToken(cfg.ChatGPT.AuthToken)
		case cfg.ChatGPT.SessionToken != "":
			cg.SetSessionToken(cfg.ChatGPT.SessionToken)
		case len(cookies) == 0:
			return nil, errors.New("otter: chatgpt credentials missing")
		}
		if _, err := cg.GetAccessToken(ctx); err != nil {
			return nil, remoteError(ctx, "otter: chatgpt login failed", false)
		}
		return &chatGPTBackend{api: cg}, nil
	case "gemini":
		cookies, err := readOtterCookies(ctx, cfg.Gemini.CookiesPath, "google.com")
		if err != nil {
			return nil, err
		}
		gm := api.NewGeminiAPI()
		gm.SetCookies(cookies)
		if cfg.Gemini.Language != "" {
			gm.SetLanguage(cfg.Gemini.Language)
		}
		if err := gm.Init(ctx); err != nil {
			return nil, remoteError(ctx, "otter: gemini login failed", false)
		}
		return &geminiBackend{api: gm}, nil
	}
	return nil, errors.New("otter: unsupported platform")
}

// safeOtterDebug 拒绝启用凭据/debug 转储的环境变量，
// 防止库将认证 token 输出到磁盘或标准错误。
func safeOtterDebug() error {
	for _, key := range []string{"OTTER_SENTINEL_DUMP", "OTTER_SSE_DEBUG", "OTTER_TURNSTILE_DEBUG"} {
		if os.Getenv(key) != "" {
			return errors.New("otter: credential/debug dumps must be disabled")
		}
	}
	return nil
}

// deepSeekLogin 抽象 DeepSeek provider 的登录凭据设置接口。
type deepSeekLogin interface {
	SetAuthToken(string)
	SetAccount(string)
	SetPassword(string)
	Login(context.Context) error
	Token() string
}

// initDeepSeek 按优先级接入 DeepSeek 凭据：
// 先尝试已保存的 token，再用账号密码登录。
// 刷新后的 token 不写回配置文件（与 otter CLI 行为不同）。
func initDeepSeek(ctx context.Context, ds deepSeekLogin, cfg *otterconfig.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if token := otterconfig.GetAuthToken(&cfg.DeepSeek, "OTTER_DEEPSEEK_AUTH_TOKEN"); token != "" {
		ds.SetAuthToken(token)
		return nil
	}
	password := otterconfig.GetPassword(&cfg.DeepSeek, "OTTER_DEEPSEEK_PASSWORD")
	if cfg.DeepSeek.Account == "" || password == "" {
		return errors.New("otter: deepseek credentials missing")
	}
	ds.SetAccount(cfg.DeepSeek.Account)
	ds.SetPassword(password)
	if err := ds.Login(ctx); err != nil {
		return remoteError(ctx, "otter: deepseek login failed", false)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ds.SetAuthToken(ds.Token())
	// 刷新后的 token 有意不写回配置文件
	return nil
}

// readOtterCookies 从指定路径或本地 Chrome 配置中读取 cookie。
// 优先使用指定路径的 cookie 文件，找不到时回退到浏览器提取。
func readOtterCookies(ctx context.Context, path string, domains ...string) ([]chrome.Cookie, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path != "" {
		if cookies, err := chrome.LoadCookies(otterconfig.ResolvePath(path)); err == nil && len(cookies) > 0 {
			return cookies, nil
		}
	}
	cookies, err := chrome.ExtractCookies(domains...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(cookies) == 0 {
		return nil, errors.New("otter: local cookies unavailable")
	}
	return cookies, nil
}

// chatGPTBackend 将 ChatGPT 底层 API 适配为 otterBackend 接口。
type chatGPTBackend struct{ api *api.ChatGPTAPI }

// SendStream 通过 ChatGPT API 发送消息并返回流式事件通道。
func (b *chatGPTBackend) SendStream(ctx context.Context, req *otter.SendRequest) (<-chan otter.StreamEvent, error) {
	if err := safeOtterDebug(); err != nil {
		return nil, err
	}
	source, err := b.api.Send(ctx, req.Prompt, req.ChatSessionID, req.ParentMessageIDStr)
	if err != nil {
		return nil, err
	}
	out := make(chan otter.StreamEvent)
	// 在后台 goroutine 中将 API 响应块转换为统一的 StreamEvent
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-source:
				if !ok {
					return
				}
				event := otter.StreamEvent{Type: chunk.Type, Content: chunk.Content, Err: chunk.Err, RemoteConversationID: chunk.ConversationID, ResponseMessageIDStr: chunk.MessageID}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// geminiBackend 将 Gemini 底层 API 适配为 otterBackend 接口。
type geminiBackend struct{ api *api.GeminiAPI }

// SendStream 通过 Gemini API 发送消息并返回流式事件通道。
func (b *geminiBackend) SendStream(ctx context.Context, req *otter.SendRequest) (<-chan otter.StreamEvent, error) {
	var metadata []any
	if raw := req.RemoteMetadata["gemini_metadata"]; raw != "" {
		if !validGeminiMetadata(raw) || json.Unmarshal([]byte(raw), &metadata) != nil {
			return nil, errors.New("otter: invalid gemini metadata")
		}
	}
	source, err := b.api.StreamGenerate(ctx, req.Prompt, metadata)
	if err != nil {
		return nil, err
	}
	out := make(chan otter.StreamEvent)
	// 在后台 goroutine 中将 Gemini 响应块转换为统一的 StreamEvent，
	// 并将 Gemini 特有的 metadata 序列化为 JSON 字符串。
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-source:
				if !ok {
					return
				}
				event := otter.StreamEvent{Type: chunk.Type, Content: chunk.Content, Err: chunk.Err, RemoteConversationID: chunk.ConversationID, ResponseMessageIDStr: chunk.ResponseID}
				if len(chunk.Metadata) != 0 {
					raw, _ := json.Marshal(chunk.Metadata)
					event.RemoteMetadata = map[string]string{"gemini_metadata": string(raw)}
				}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
