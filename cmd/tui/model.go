package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/modes"
	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
	ievent "github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
	"github.com/xiws/orca/internal/tool"
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
	record   *session.Record
	provider llm.ModelInfo
	executor app.Executor
	msgCh    <-chan string
	events   chan tea.Msg
	done     chan struct{}

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
	projectPath := utils.GetCurrentPath()
	runtime := core.NewRuntime(
		core.WithSkipDefaultHandlers(),
		core.WithMsgChannel(msgCh),
		core.WithWorkspace(projectPath),
	)
	_ = runtime.Subscribe(ievent.ToolBeforeEvent{}, tuiToolBeforeHandler{})
	_ = runtime.Subscribe(ievent.ToolAfterEvent{}, tuiToolAfterHandler{})
	return model{
		runtime:  runtime,
		record:   &session.Record{Session: domain.NewSession(projectPath)},
		provider: llm.GetProvider(tool.Get(tool.KeyDefaultProvider), tool.Get(tool.KeyDefaultModel)),
		executor: modes.For("code", runtime),
		msgCh:    msgCh,
		events:   make(chan tea.Msg, 100),
		done:     make(chan struct{}),
		status:   "Ready",
		state:    stateIdle,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.events:
			return msg
		case <-m.done:
			return nil
		}
	}
}

// ── Update ──────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.viewport = viewport.New(msg.Width, max(1, msg.Height-4))
			m.viewport.SetContent(welcomeText())
			m.input = textinput.New()
			m.input.Placeholder = "Enter a task... (Ctrl+C to quit)"
			m.input.CharLimit = 2000
			m.input.Focus()
		}
		m.viewport.Width = max(1, msg.Width)
		m.viewport.Height = max(1, msg.Height-4)
		m.input.Width = max(1, msg.Width-4)
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
			return m, tea.Batch(m.runAgent(value), m.waitForEvent())
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
		return m, m.waitForEvent()

	case toolStatusMsg:
		if m.state == stateIdle {
			return m, nil
		}
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
		alreadyDisplayed := m.streamingActive && len(m.chatHistory) > 0 && strings.HasSuffix(m.chatHistory[len(m.chatHistory)-1], msg.result)
		m.state = stateIdle
		m.streamingActive = false
		m.status = "Ready"
		if msg.err != nil {
			m.chatHistory = appendChat(m.chatHistory, "\n❌ Error: "+msg.err.Error())
		} else if msg.result != "" && !alreadyDisplayed {
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

func (m model) runAgent(prompt string) tea.Cmd {
	return func() tea.Msg {
		finished := make(chan agentResultMsg, 1)
		go func() {
			turn := app.Prepare(m.record, app.Request{Input: prompt, Provider: m.provider})
			result, err := app.Execute(m.record, turn, m.executor)
			finished <- agentResultMsg{err: err, result: result}
		}()
		emit := func(msg tea.Msg) {
			select {
			case m.events <- msg:
			case <-m.done:
			}
		}
		chunks := m.msgCh
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				emit(streamChunkMsg(chunk))
			case result := <-finished:
				// Request 返回前已发送全部分块；完成事件必须排在它们之后。
				for {
					select {
					case chunk, ok := <-chunks:
						if !ok {
							chunks = nil
							continue
						}
						emit(streamChunkMsg(chunk))
					default:
						emit(result)
						return nil
					}
				}
			}
		}
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
