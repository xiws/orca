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

// otterConfigPath 是 otter CLI 保存凭据的位置。适配器
// 读取并刷新同一个文件，因此一次登录可同时服务两个工具。
const otterConfigPath = "~/.otter/config.json"

// otterRequestTimeout 限制单次平台请求；otterLoginTimeout 限制
// 平台侧会话创建和账号密码登录。
const (
	otterRequestTimeout = 5 * time.Minute
	otterLoginTimeout   = 30 * time.Second
)

// otterBackend 是 requester 构建其上的 otter 平台 provider 切片。
// 在此声明使适配器在测试中可被 fake 替换。
type otterBackend interface {
	Name() string
	SendStream(ctx context.Context, req *provider.SendRequest) (<-chan provider.StreamEvent, error)
}

// otterBackendBuilder 解析模型 id 指定的 otter 平台。
type otterBackendBuilder func(platform string) (otterBackend, error)

// otterRemoteSession 是适配器需要的 otter provider.RemoteSessionProvider 切片：
// DeepSeek 等平台在服务端创建对话并返回其 id。
// 在此声明（比 otter 的接口更窄，后者还携带 SessionURL），
// 以便 fake 可以实现它。
type otterRemoteSession interface {
	CreateRemoteSession(ctx context.Context) (string, error)
}

// otterRequester 将 otter 平台 provider 适配为 Requester 接口。
//
// otter 平台是 provider 侧的有状态对话：平台自己记住历史，
// 每次后续请求必须引用上一次回复的 id。因此适配器保持 orca 对话
// 与平台会话同步——它跟踪已发送多少条消息，
// 只发送新的尾部：首次调用时发送系统上下文和用户轮次，
// 之后发送工具结果。
type otterRequester struct {
	info    ModelInfo
	build   otterBackendBuilder
	backend otterBackend
	// remote 为在服务端创建会话的平台设置，如 DeepSeek；
	// ChatGPT 和 Gemini 用第一条消息构建对话，不需要此字段。
	remote otterRemoteSession

	delivered int

	chatSessionID      string
	parentMessageID    int
	parentMessageIDStr string
	remoteMetadata     map[string]string
}

// otterMeta 是单次流式回复报告回传的平台状态。
// 仅当整个流成功后才应用到 requester，因此失败的请求不会推进会话状态。
type otterMeta struct {
	chatSessionID      string
	parentMessageID    int
	parentMessageIDStr string
	remoteMetadata     map[string]string
	tokenUsage         int
}

// OtterState 捕获 otterRequester 持有的平台侧对话状态。
// 它持久化在 orca 会话中，使恢复的进程能接续同一个远程会话，
// 而不是创建新会话。
type OtterState struct {
	ChatSessionID      string            `json:"chat_session_id,omitempty"`
	ParentMessageID    int               `json:"parent_message_id,omitempty"`
	ParentMessageIDStr string            `json:"parent_message_id_str,omitempty"`
	RemoteMetadata     map[string]string `json:"remote_metadata,omitempty"`
	Delivered          int               `json:"delivered"`
}

// apply 将回复的一个事件合并到 meta 中。缺失字段保留其先前值，
// 因为平台可能只报告一次。
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

// NewOtterRequester 为 otter 平台服务的模型构建 Requester。
// 模型 id 指定平台名称：deepseek、chatgpt 或 gemini。
func NewOtterRequester(info ModelInfo) Requester {
	return newOtterRequester(info, buildOtterBackend)
}

// newOtterRequester 是测试安装 fake 后端的接口。
func newOtterRequester(info ModelInfo, build otterBackendBuilder) *otterRequester {
	return &otterRequester{info: info, build: build}
}

// Request 将对话中未发送的尾部发送到平台并流式回传回复。
// 成功时提交回复报告的状态，尾部计为已发送；
// 任何失败都不会推进状态，因此重试的调用方会重新发送相同的尾部。
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
			// "meta" 和 "done" 携带下一轮必须引用的 id。
			meta.apply(event)
		}
	}

	o.commit(meta, len(prompts))
	result.Content = content.String()
	result.FinishReason = "stop"
	result.Usage = Usage{TotalTokens: meta.tokenUsage}
	return result
}

// 只有当前远端会话生成的助手回复可以省略，新执行必须重放已确认的历史回复。
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
		case RoleAssistant:
			if o.delivered == 0 && message.Content != "" {
				segment = "Previous assistant response:\n" + message.Content
			}
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

// toolResultSegment 渲染一个工具回复，命名其回复的调用，
// 使模型能区分不同轮次的结果。
func toolResultSegment(prompts []ChatMessage, index int) string {
	message := prompts[index]
	name := toolCallName(prompts, index, message.ToolCallID)
	if name == "" {
		return "Tool result:\n" + message.Content
	}
	return fmt.Sprintf("Tool result for %s:\n%s", name, message.Content)
}

// toolCallName 查找 callID 回复的调用名称，
// 回溯对话的助手轮次。
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

// ensureBackend 在每个 requester 中构建一次平台 provider。
// 构建时可能登录，因此在首次 Request 时发生（此时失败有 Result 可承载），
// 而不是在构造时。
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

// ensureRemoteSession 为会话 id 在服务端生成的平台在首次调用时创建平台侧会话。
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

// State 返回当前平台状态的快照，用于持久化。
func (o *otterRequester) State() OtterState {
	return OtterState{
		ChatSessionID:      o.chatSessionID,
		ParentMessageID:    o.parentMessageID,
		ParentMessageIDStr: o.parentMessageIDStr,
		RemoteMetadata:     o.remoteMetadata,
		Delivered:          o.delivered,
	}
}

// RestoreState 从之前保存的状态恢复 requester，
// 使恢复的进程继续同一个平台对话。
func (o *otterRequester) RestoreState(s OtterState) {
	o.chatSessionID = s.ChatSessionID
	o.parentMessageID = s.ParentMessageID
	o.parentMessageIDStr = s.ParentMessageIDStr
	o.remoteMetadata = s.RemoteMetadata
	o.delivered = s.Delivered
}

// commit 记录已完成的回复报告的内容：下一次后续必须引用的 id，
// 以及现在计为已发送的尾部长度。
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

// buildOtterBackend 构建模型 id 指定的 otter 平台 provider。
// 凭据的初始化方式与 otter CLI 完全一致，因此用任一工具执行的登录可同时服务两者。
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

// initOtterDeepSeek 按 otter CLI 的顺序接入 DeepSeek 凭据：
// 先环境变量 token，再保存的 token，最后是账号密码登录，
// 登录后的新 token 写回配置文件，使两个工具保持登录状态。
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

// initOtterChatGPT 按 otter CLI 的方式接入 ChatGPT：
// cookie 存储是主要凭据，provider 回退到本地 Chrome 配置文件本身；
// 环境变量和配置 token 作为手动覆盖。
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

// initOtterGemini 接入 Gemini 的 cookie 存储和可选语言，与 otter CLI 一致。
func initOtterGemini(gm *provider.GeminiProvider, appConfig *config.Config) {
	gm.SetCookiesPath(config.ResolvePath(appConfig.Gemini.CookiesPath))
	if appConfig.Gemini.Language != "" {
		gm.SetLanguage(appConfig.Gemini.Language)
	}
}
