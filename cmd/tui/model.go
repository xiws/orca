package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xiws/orca/internal/agent/core"
	ievent "github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

// ── Agent 状态 ──────────────────────────────────────────────

type agentState int

const (
	stateIdle agentState = iota
	stateThinking
	stateRunningTool
)

// ── Bubble Tea 消息 ─────────────────────────────────────────

type agentResultMsg struct {
	err    error
	result string
}

type streamChunkMsg string
type toolStatusMsg string

// ── TUI 事件处理器（替代 CLI 默认的 stdout 打印） ────────────
// 通过包级变量 programRef 访问 tea.Program，该指针在 main() 中设置。

type tuiToolBeforeHandler struct{}

func (h tuiToolBeforeHandler) Handle(ent event.Event) {
	e, ok := ent.(ievent.ToolBeforeEvent)
	if !ok || programRef == nil {
		return
	}
	// 文件操作显示文件名，bash 显示命令内容
	if e.File() != "" {
		programRef.Send(toolStatusMsg(fmt.Sprintf("🔧 %s %s", e.ToolName(), e.File())))
	} else {
		programRef.Send(toolStatusMsg(fmt.Sprintf("🔧 %s %s", e.ToolName(), e.Meta())))
	}
}

type tuiToolAfterHandler struct{}

func (h tuiToolAfterHandler) Handle(ent event.Event) {
	e, ok := ent.(ievent.ToolAfterEvent)
	if !ok || programRef == nil {
		return
	}
	name := e.ToolName()
	if e.OK() {
		programRef.Send(toolStatusMsg(fmt.Sprintf("✅ %s done", name)))
	} else {
		programRef.Send(toolStatusMsg(fmt.Sprintf("❌ %s failed", name)))
	}
}

// ── 样式 ────────────────────────────────────────────────────

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("211")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Padding(0, 1)

	inputStyle = lipgloss.NewStyle().
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("236")).
			Padding(0, 1)

	chatStyle = lipgloss.NewStyle().
			Padding(0, 1)
)

// ── Model ───────────────────────────────────────────────────

type model struct {
	viewport viewport.Model
	input    textinput.Model
	runtime  *core.Runtime
	session  *core.Session
	msgCh    <-chan string

	chatHistory     []string
	status          string
	state           agentState
	streamingActive bool // 标记是否正在接收流式内容，用于正确分隔聊天条目
	ready           bool
	width           int
	height          int
}

func newModel() model {
	msgCh := make(chan string, 100)

	runtime := core.NewRuntime(
		core.WithSkipDefaultHandlers(),
		core.WithMsgChannel(msgCh),
	)

	// 订阅 TUI 事件处理器
	_ = runtime.Subscribe(ievent.ToolBeforeEvent{}, tuiToolBeforeHandler{})
	_ = runtime.Subscribe(ievent.ToolAfterEvent{}, tuiToolAfterHandler{})

	session := core.NewSession()

	// 系统提示只添加一次，后续轮次复用同一个 session 保留上下文
	ctx := core.PromptContext{
		ProjectPath:   session.ProjectPath,
		ContextLength: session.Provider.ContextWindow,
	}
	session.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(ctx))
	session.AppendToolPrompt()

	return model{
		runtime: runtime,
		session: session,
		msgCh:   msgCh,
		status:  "Ready",
		state:   stateIdle,
	}
}

func (m model) Init() tea.Cmd {
	// 启动流式内容桥接 goroutine：从 runtime msg channel → Bubble Tea 消息
	go func() {
		for chunk := range m.msgCh {
			if programRef != nil {
				programRef.Send(streamChunkMsg(chunk))
			}
		}
	}()

	return textinput.Blink
}

// ── Update ──────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		m.viewport = viewport.New(msg.Width, msg.Height-4)
		m.viewport.SetContent(welcomeText())
		m.viewport.HighPerformanceRendering = false

		m.input = textinput.New()
		m.input.Placeholder = "Enter a task... (Ctrl+C to quit)"
		m.input.CharLimit = 2000
		m.input.Width = m.width - 4
		m.input.Focus()

		m.ready = true
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			value := m.input.Value()
			if strings.TrimSpace(value) == "" || m.state != stateIdle {
				return m, nil
			}
			m.chatHistory = appendChat(m.chatHistory, "> "+value)
			m.input.Reset()
			m.state = stateThinking
			m.status = "🤖 Thinking..."
			if m.ready {
				m.viewport.SetContent(strings.Join(m.chatHistory, "\n"))
				m.viewport.GotoBottom()
			}
			return m, m.runAgent(value)
		}

	case streamChunkMsg:
		content := string(msg)
		if !m.streamingActive {
			// 新一轮流式内容开始：始终创建新条目
			m.streamingActive = true
			m.chatHistory = appendChat(m.chatHistory, content)
		} else if len(m.chatHistory) > 0 {
			// 继续追加到当前流式条目
			m.chatHistory[len(m.chatHistory)-1] += content
		} else {
			m.chatHistory = appendChat(m.chatHistory, content)
		}
		m.status = "🤖 Responding..."
		m.state = stateThinking
		if m.ready {
			m.viewport.SetContent(strings.Join(m.chatHistory, "\n"))
			m.viewport.GotoBottom()
		}
		return m, nil

	case toolStatusMsg:
		m.streamingActive = false // 工具执行中断流式上下文
		m.chatHistory = appendChat(m.chatHistory, "\n"+string(msg))
		m.status = "🔧 Running tool..."
		m.state = stateRunningTool
		if m.ready {
			m.viewport.SetContent(strings.Join(m.chatHistory, "\n"))
			m.viewport.GotoBottom()
		}
		return m, nil

	case agentResultMsg:
		m.state = stateIdle
		m.streamingActive = false
		m.status = "Ready"
		if msg.err != nil {
			m.chatHistory = appendChat(m.chatHistory, "\n❌ Error: "+msg.err.Error())
		} else if msg.result != "" {
			m.chatHistory = appendChat(m.chatHistory, "\n\n"+msg.result)
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.chatHistory, "\n"))
			m.viewport.GotoBottom()
		}
		return m, nil
	}

	var cmds []tea.Cmd

	if m.ready {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)

		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

// ── Agent 执行 ──────────────────────────────────────────────

// runAgent 在后台 goroutine 中执行一轮 Agent 对话。
// 复用 m.session 保留跨轮次的消息历史，使 LLM 能看到之前的对话上下文。
func (m model) runAgent(prompt string) tea.Cmd {
	return func() tea.Msg {
		// 追加用户消息到已有会话（系统提示已在 newModel 中添加）
		m.session.AppendMessage(llm.RoleUser, prompt)

		task := &core.Task{
			Id:          m.session.Id,
			SessionInfo: m.session,
			Input:       prompt,
		}

		err, result := m.runtime.RunTask(task)
		return agentResultMsg{err: err, result: result}
	}
}

// ── View ────────────────────────────────────────────────────

func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}

	chatHeight := m.height - 4
	if chatHeight < 1 {
		chatHeight = 1
	}
	m.viewport.Height = chatHeight

	title := titleStyle.Render("🤖 Orca TUI")
	status := statusStyle.Render(m.status)
	header := lipgloss.JoinHorizontal(lipgloss.Top, title, status)

	chat := chatStyle.Render(m.viewport.View())

	prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Render("> ")
	inputView := inputStyle.Render(prompt + m.input.View())

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		chat,
		inputView,
	)
}

// ── Helpers ─────────────────────────────────────────────────

func appendChat(history []string, msg string) []string {
	history = append(history, msg)
	if len(history) > 1000 {
		history = history[len(history)-1000:]
	}
	return history
}

func welcomeText() string {
	return "Welcome to Orca TUI!\nEnter a task and press Enter to start.\nCtrl+C to quit."
}
