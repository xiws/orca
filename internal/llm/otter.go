package llm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xiws/otter/pkg/config"
	"github.com/xiws/otter/pkg/provider"
)

// otterConfigPath is where the otter CLI keeps its credentials. The adapter
// reads and refreshes that same file, so one login serves both tools.
const otterConfigPath = "~/.otter/config.json"

// otterRequestTimeout bounds one platform request; otterLoginTimeout bounds
// the platform-side session creation and the account/password login.
const (
	otterRequestTimeout = 5 * time.Minute
	otterLoginTimeout   = 30 * time.Second
)

// otterBackend is the slice of otter's platform provider the requester builds
// on. Declaring it here keeps the adapter replaceable by a fake in tests.
type otterBackend interface {
	Name() string
	SendStream(ctx context.Context, req *provider.SendRequest) (<-chan provider.StreamEvent, error)
}

// otterBackendBuilder resolves the otter platform named by a model id.
type otterBackendBuilder func(platform string) (otterBackend, error)

// otterRemoteSession is the slice of otter's provider.RemoteSessionProvider
// the adapter needs: platforms such as DeepSeek create their conversation
// server side and hand back its id. Declared here (narrower than otter's
// interface, which also carries SessionURL) so a fake can satisfy it.
type otterRemoteSession interface {
	CreateRemoteSession(ctx context.Context) (string, error)
}

// otterRequester adapts an otter platform provider to the Requester interface.
//
// An otter platform is a stateful conversation on the provider's side: the
// platform remembers the history itself and every follow-up must quote the
// previous reply's id(s). The adapter therefore keeps the orca conversation
// and the platform session in step — it tracks how many messages have been
// delivered and sends only the new tail: the system context and the user turn
// on the first call, the tool results afterwards.
type otterRequester struct {
	info    ModelInfo
	build   otterBackendBuilder
	backend otterBackend
	// remote is set for platforms that create their session server side, such
	// as DeepSeek; ChatGPT and Gemini build the conversation with the first
	// message and need none.
	remote otterRemoteSession

	delivered int

	chatSessionID      string
	parentMessageID    int
	parentMessageIDStr string
	remoteMetadata     map[string]string
}

// otterMeta is the platform state one streamed reply reports back. It is
// applied to the requester only once the whole stream succeeded, so a failed
// request leaves the session where it was.
type otterMeta struct {
	chatSessionID      string
	parentMessageID    int
	parentMessageIDStr string
	remoteMetadata     map[string]string
	tokenUsage         int
}

// apply merges one event of the reply into the meta. Missing fields keep their
// previous value, since a platform may report them only once.
func (m *otterMeta) apply(event provider.StreamEvent) {
	if event.ResponseMessageID != 0 {
		m.parentMessageID = event.ResponseMessageID
	}
	if event.ResponseMessageIDStr != "" {
		m.parentMessageIDStr = event.ResponseMessageIDStr
	}
	if event.RemoteConversationID != "" {
		m.chatSessionID = event.RemoteConversationID
	}
	if event.TokenUsage != 0 {
		m.tokenUsage = event.TokenUsage
	}
	for key, value := range event.RemoteMetadata {
		if value == "" {
			continue
		}
		if m.remoteMetadata == nil {
			m.remoteMetadata = make(map[string]string)
		}
		m.remoteMetadata[key] = value
	}
}

// NewOtterRequester builds the Requester for a model served by an otter
// platform. The model id names the platform: deepseek, chatgpt or gemini.
func NewOtterRequester(info ModelInfo) Requester {
	return newOtterRequester(info, buildOtterBackend)
}

// newOtterRequester is the seam tests use to install a fake backend.
func newOtterRequester(info ModelInfo, build otterBackendBuilder) *otterRequester {
	return &otterRequester{info: info, build: build}
}

// Request sends the undelivered tail of the conversation to the platform and
// streams the reply back. On success the state the reply reported is committed
// and the tail counts as delivered; on any failure nothing advances, so a
// caller that retries resends the same tail.
func (o *otterRequester) Request(prompts []ChatMessage, msgs chan<- string) Result {
	var result Result

	prompt := o.pendingPrompt(prompts)
	if prompt == "" {
		result.Error = fmt.Errorf("otter: no new message to send")
		return result
	}

	backend, err := o.ensureBackend()
	if err != nil {
		result.Error = err
		return result
	}
	if err := o.ensureRemoteSession(); err != nil {
		result.Error = err
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), otterRequestTimeout)
	defer cancel()

	events, err := backend.SendStream(ctx, &provider.SendRequest{
		Prompt:             prompt,
		ChatSessionID:      o.chatSessionID,
		ParentMessageID:    o.parentMessageID,
		ParentMessageIDStr: o.parentMessageIDStr,
		RemoteMetadata:     o.remoteMetadata,
		Timeout:            otterRequestTimeout,
	})
	if err != nil {
		result.Error = err
		return result
	}

	var content strings.Builder
	var meta otterMeta
	for event := range events {
		switch event.Type {
		case "text":
			content.WriteString(event.Content)
			send(msgs, event.Content)
		case "error":
			if event.Err != nil {
				result.Error = event.Err
			} else {
				result.Error = fmt.Errorf("otter: %s", event.Content)
			}
			return result
		default:
			// "meta" and "done" carry the ids the next round must quote.
			meta.apply(event)
		}
	}

	o.commit(meta, len(prompts))
	result.Content = content.String()
	result.FinishReason = "stop"
	result.Usage = Usage{TotalTokens: meta.tokenUsage}
	return result
}

// pendingPrompt renders the messages the platform has not seen yet. Assistant
// turns are skipped because the platform session already holds everything the
// model said; resending them would duplicate the conversation.
func (o *otterRequester) pendingPrompt(prompts []ChatMessage) string {
	if o.delivered >= len(prompts) {
		return ""
	}
	var out strings.Builder
	for index := o.delivered; index < len(prompts); index++ {
		message := prompts[index]
		var segment string
		switch message.Role {
		case RoleSystem, RoleUser:
			segment = message.Content
		case RoleTool:
			segment = toolResultSegment(prompts, index)
		default:
			continue
		}
		if segment == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(segment)
	}
	return out.String()
}

// toolResultSegment renders one tool answer, naming the call it answers so the
// model can tell the results of one turn apart.
func toolResultSegment(prompts []ChatMessage, index int) string {
	message := prompts[index]
	name := toolCallName(prompts, index, message.ToolCallID)
	if name == "" {
		return "Tool result:\n" + message.Content
	}
	return fmt.Sprintf("Tool result for %s:\n%s", name, message.Content)
}

// toolCallName finds the name of the call callID answers, looking back at the
// assistant turns of the conversation.
func toolCallName(prompts []ChatMessage, before int, callID string) string {
	if callID == "" {
		return ""
	}
	for index := before - 1; index >= 0; index-- {
		if prompts[index].Role != RoleAssistant {
			continue
		}
		for _, call := range prompts[index].ToolCalls {
			if call.ID == callID {
				return call.Name
			}
		}
	}
	return ""
}

// ensureBackend builds the platform provider once per requester. Building it
// may log in, so it happens on the first Request where a failure has a Result
// to travel in, not at construction.
func (o *otterRequester) ensureBackend() (otterBackend, error) {
	if o.backend != nil {
		return o.backend, nil
	}
	backend, err := o.build(o.info.ModelID)
	if err != nil {
		return nil, err
	}
	o.backend = backend
	if remote, ok := backend.(otterRemoteSession); ok {
		o.remote = remote
	}
	return backend, nil
}

// ensureRemoteSession creates the platform-side session on the first call for
// platforms whose session ids are generated server side.
func (o *otterRequester) ensureRemoteSession() error {
	if o.remote == nil || o.chatSessionID != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), otterLoginTimeout)
	defer cancel()

	chatSessionID, err := o.remote.CreateRemoteSession(ctx)
	if err != nil {
		return fmt.Errorf("otter: create platform session: %w", err)
	}
	o.chatSessionID = chatSessionID
	return nil
}

// commit records what the finished reply reported: the ids the next follow-up
// must quote and the tail length that now counts as delivered.
func (o *otterRequester) commit(meta otterMeta, delivered int) {
	if meta.chatSessionID != "" {
		o.chatSessionID = meta.chatSessionID
	}
	if meta.parentMessageID != 0 {
		o.parentMessageID = meta.parentMessageID
	}
	if meta.parentMessageIDStr != "" {
		o.parentMessageIDStr = meta.parentMessageIDStr
	}
	for key, value := range meta.remoteMetadata {
		if o.remoteMetadata == nil {
			o.remoteMetadata = make(map[string]string)
		}
		o.remoteMetadata[key] = value
	}
	o.delivered = delivered
}

// buildOtterBackend constructs the otter platform provider named by a model
// id. Credentials are initialised exactly the way the otter CLI does, so a
// login performed with either tool serves both.
func buildOtterBackend(platform string) (otterBackend, error) {
	configPath := config.ResolvePath(otterConfigPath)
	appConfig, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("otter: %w", err)
	}

	switch platform {
	case "deepseek":
		ds := provider.NewDeepSeekProvider()
		if err := initOtterDeepSeek(ds, appConfig, configPath); err != nil {
			return nil, err
		}
		return ds, nil
	case "chatgpt":
		cg := provider.NewChatGPTProvider()
		initOtterChatGPT(cg, appConfig)
		return cg, nil
	case "gemini":
		gm := provider.NewGeminiProvider()
		initOtterGemini(gm, appConfig)
		return gm, nil
	default:
		return nil, fmt.Errorf("otter: unsupported platform %q, want deepseek, chatgpt or gemini", platform)
	}
}

// initOtterDeepSeek wires DeepSeek credentials in the order the otter CLI
// uses: an env token first, then the saved token, then an account/password
// login whose fresh token is written back so both tools stay logged in.
func initOtterDeepSeek(ds *provider.DeepSeekProvider, appConfig *config.Config, configPath string) error {
	if token := os.Getenv("OTTER_DEEPSEEK_AUTH_TOKEN"); token != "" {
		ds.SetAuthToken(token)
		return nil
	}
	if appConfig.DeepSeek.AuthToken != "" {
		ds.SetAuthToken(appConfig.DeepSeek.AuthToken)
		return nil
	}

	account := appConfig.DeepSeek.Account
	password := config.GetPassword(&appConfig.DeepSeek, "OTTER_DEEPSEEK_PASSWORD")
	if account == "" || password == "" {
		return fmt.Errorf("otter: deepseek credentials missing, run `otter config set deepseek.account` and `otter config set deepseek.password`, or `otter auth login deepseek`")
	}
	ds.SetAccount(account)
	ds.SetPassword(password)

	ctx, cancel := context.WithTimeout(context.Background(), otterLoginTimeout)
	defer cancel()
	if err := ds.Login(ctx); err != nil {
		return fmt.Errorf("otter: %w", err)
	}
	if token := ds.Token(); token != "" {
		appConfig.DeepSeek.AuthToken = token
		_ = config.Save(appConfig, configPath)
	}
	return nil
}

// initOtterChatGPT wires ChatGPT the way the otter CLI does: the cookie store
// is the primary credential and the provider falls back to the local Chrome
// profile itself; env and config tokens remain as a manual override.
func initOtterChatGPT(cg *provider.ChatGPTProvider, appConfig *config.Config) {
	cg.SetCookiesPath(config.ResolvePath(appConfig.ChatGPT.CookiesPath))
	if appConfig.ChatGPT.Model != "" {
		cg.SetModel(appConfig.ChatGPT.Model)
	}
	switch {
	case os.Getenv("OTTER_CHATGPT_ACCESS_TOKEN") != "":
		cg.SetAccessToken(os.Getenv("OTTER_CHATGPT_ACCESS_TOKEN"))
	case os.Getenv("OTTER_CHATGPT_SESSION_TOKEN") != "":
		cg.SetSessionToken(os.Getenv("OTTER_CHATGPT_SESSION_TOKEN"))
	case appConfig.ChatGPT.AuthToken != "":
		cg.SetAccessToken(appConfig.ChatGPT.AuthToken)
	case appConfig.ChatGPT.SessionToken != "":
		cg.SetSessionToken(appConfig.ChatGPT.SessionToken)
	}
}

// initOtterGemini wires Gemini's cookie store and optional language, matching
// the otter CLI.
func initOtterGemini(gm *provider.GeminiProvider, appConfig *config.Config) {
	gm.SetCookiesPath(config.ResolvePath(appConfig.Gemini.CookiesPath))
	if appConfig.Gemini.Language != "" {
		gm.SetLanguage(appConfig.Gemini.Language)
	}
}
