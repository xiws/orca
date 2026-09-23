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

func buildOtterBackend(ctx context.Context, platform string) (otterBackend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch platform {
	case "deepseek", "chatgpt", "gemini":
	default:
		return nil, errors.New("otter: unsupported platform")
	}
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
		// The high-level provider writes extracted/refreshed cookies. Use the same
		// configuration boundary with the lower-level API and in-memory cookies.
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

// Refuse dependency debug switches rather than changing process environment or
// allowing the library to dump authentication tokens to disk/stderr.
func safeOtterDebug() error {
	for _, key := range []string{"OTTER_SENTINEL_DUMP", "OTTER_SSE_DEBUG", "OTTER_TURNSTILE_DEBUG"} {
		if os.Getenv(key) != "" {
			return errors.New("otter: credential/debug dumps must be disabled")
		}
	}
	return nil
}

type deepSeekLogin interface {
	SetAuthToken(string)
	SetAccount(string)
	SetPassword(string)
	Login(context.Context) error
	Token() string
}

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
	// The refreshed token is intentionally not copied to cfg or saved.
	return nil
}

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

type chatGPTBackend struct{ api *api.ChatGPTAPI }

func (b *chatGPTBackend) SendStream(ctx context.Context, req *otter.SendRequest) (<-chan otter.StreamEvent, error) {
	if err := safeOtterDebug(); err != nil {
		return nil, err
	}
	source, err := b.api.Send(ctx, req.Prompt, req.ChatSessionID, req.ParentMessageIDStr)
	if err != nil {
		return nil, err
	}
	out := make(chan otter.StreamEvent)
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

type geminiBackend struct{ api *api.GeminiAPI }

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
