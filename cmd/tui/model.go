package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
)

// service 定义 TUI 使用的应用层服务接口。
type service interface {
	Submit(context.Context, app.SubmitRequest) (*domain.Run, error)
	ContinueSession(context.Context, domain.SessionID, app.SubmitRequest) (*domain.Run, error)
	Run(context.Context, domain.RunID) (*domain.Run, error)
	Runs(context.Context) ([]domain.Run, error)
	Session(context.Context, domain.SessionID) (*domain.Session, error)
	Wait(context.Context, domain.RunID) (*domain.Run, error)
	Observe(context.Context, domain.RunID, int64) ([]domain.Event, error)
	Subscribe(domain.RunID) (<-chan domain.Event, func())
	PendingInputs(context.Context, domain.RunID) ([]domain.InputRequest, error)
	RespondToInput(context.Context, domain.InputRequestID, string, bool) (*domain.Run, error)
	ResumeRun(context.Context, domain.RunID) (*domain.Run, error)
	RetryTask(context.Context, domain.TaskID) (*domain.Run, error)
	Cancel(context.Context, domain.RunID) error
}

var _ service = (*app.Service)(nil)

// operationMsg 是提交/会话操作完成后的消息。
type operationMsg struct {
	generation int
	run        *domain.Run
	session    *domain.Session
	err        error
}

// snapshotMsg 携带一次完整快照（运行状态、事件、输入等）。
type snapshotMsg struct {
	generation     int
	run            *domain.Run
	events         []domain.Event
	inputs         []domain.InputRequest
	cursors        map[domain.RunID]int64
	ids            map[domain.RunID]bool
	childrenActive bool
	err            error
}

// pollMsg 触发一次定时轮询快照。
type pollMsg struct{ generation int }

// deltaMsg 携带一个流式增量事件，或表示订阅通道已关闭。
type deltaMsg struct {
	generation int
	event      domain.Event
	closed     bool
}

// cancelMsg 携带取消操作的结果以及取消期间的额外事件。
type cancelMsg struct {
	generation int
	run        *domain.Run
	err        error
	events     []domain.Event
}

// runsMsg 携带 /runs 命令返回的运行列表。
type runsMsg struct {
	runs []domain.Run
	err  error
}

// exitRequestedMsg 表示信号或 Ctrl+C 请求退出。
type exitRequestedMsg struct{}

// model 是 Bubble Tea 的模型，保存 TUI 的全部状态与依赖。
type model struct {
	operations     *operationGroup
	service        service
	ctx            context.Context
	viewport       viewport.Model
	input          textinput.Model
	chatHistory    []string
	status         string
	modeName       string
	sessionID      domain.SessionID
	runID          domain.RunID
	state          domain.RunState
	busy           bool
	quitting       bool
	cancelPending  bool
	generation     int
	watchCtx       context.Context
	stopWatch      context.CancelFunc
	unsubscribe    func()
	deltas         <-chan domain.Event
	cursors        map[domain.RunID]int64
	runIDs         map[domain.RunID]bool
	shownInputs    map[domain.InputRequestID]bool
	previews       map[streamKey]string
	confirmed      map[invocationKey]int
	lastMessages   map[domain.RunID][32]byte
	previewOrder   []streamKey
	waitingHuman   bool
	childrenActive bool
	ready          bool
	width          int
	height         int
}

// newModel 构造 model：所有执行依赖都通过注入，构造过程不读取 HOME、
// 不读配置、不打开存储、不注册处理器，也不启动执行 goroutine。
func newModel(s service) model {
	input := textinput.New()
	input.Placeholder = "Task or /help; Ctrl+C cancels active work and exits"
	input.CharLimit = 16000
	input.Width = 76
	input.Focus()
	vp := viewport.New(80, 20)
	vp.SetContent(welcomeText())
	return model{operations: &operationGroup{}, service: s, ctx: context.Background(), viewport: vp, input: input,
		status: "idle", modeName: "code", width: 80, height: 24,
		chatHistory: []string{welcomeText()}, cursors: map[domain.RunID]int64{},
		runIDs: map[domain.RunID]bool{}, shownInputs: map[domain.InputRequestID]bool{}}
}

// Init 实现 tea.Model 接口，返回输入框光标闪烁命令。
func (m model) Init() tea.Cmd { return textinput.Blink }

// executing 判断当前是否有正在执行的运行（含排队/运行/取消中，或等待子任务）。
func (m model) executing() bool {
	return m.runID != 0 && (m.state == domain.Queued || m.state == domain.Running || m.state == domain.Cancelling || (m.state == domain.Waiting && (!m.waitingHuman || m.childrenActive)))
}

// append 将文本追加到聊天历史，并对历史记录做长度截断。
func (m *model) append(text string) {
	m.chatHistory = append(m.chatHistory, safeText(text))
	if len(m.chatHistory) > 1000 {
		m.chatHistory = append([]string(nil), m.chatHistory[len(m.chatHistory)-1000:]...)
	}
	m.refresh()
}

// refresh 将聊天历史和流式预览合并后写入 viewport，并滚动到底部。
func (m *model) refresh() {
	var content strings.Builder
	content.WriteString(strings.Join(m.chatHistory, "\n"))
	for _, key := range m.previewOrder {
		if text, ok := m.previews[key]; ok {
			fmt.Fprintf(&content, "\n[stream run=%d invocation=%d assistant=%d; provisional]\n%s", key.run, key.invocation, key.sequence, text)
		}
	}
	m.viewport.SetContent(ansi.Hardwrap(content.String(), max(1, m.width), true))
	m.viewport.GotoBottom()
}

// stopObserving 停止对运行事件流的观察，取消订阅并清空 delta 通道。
func (m *model) stopObserving() {
	if m.stopWatch != nil {
		m.stopWatch()
		m.stopWatch = nil
	}
	if m.unsubscribe != nil {
		m.unsubscribe()
		m.unsubscribe = nil
	}
	m.deltas = nil
}

// Update 实现 tea.Model 接口，根据消息类型分发处理逻辑。
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// 复用两个控件：resize 时不能替换历史记录或输入草稿。
		m.width, m.height = clamp(msg.Width, 1, 500), clamp(msg.Height, 1, 200)
		m.viewport.Width, m.viewport.Height = m.width, max(1, m.height-3)
		m.input.Width = max(1, m.width-2)
		m.ready = true
		m.refresh()
		return m, nil
	case exitRequestedMsg:
		return m.requestExit()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m.requestExit()
		}
		if msg.Type == tea.KeyEnter {
			value := strings.TrimSpace(m.input.Value())
			if value == "" || m.quitting {
				return m, nil
			}
			return m.dispatch(value)
		}
	case operationMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.busy = false
		if msg.err == nil && msg.session == nil && msg.run == nil {
			msg.err = fmt.Errorf("service returned no run")
		}
		if msg.err != nil {
			m.append("Error: " + msg.err.Error())
			if m.quitting {
				return m, tea.Quit
			}
			m.status = "error"
			if m.runID != 0 && !m.state.Terminal() {
				m.watchCtx, m.stopWatch = context.WithCancel(m.ctx)
				m.deltas, m.unsubscribe = m.service.Subscribe(0)
				return m, tea.Batch(m.snapshot(), waitDelta(m.watchCtx, m.deltas, m.generation))
			}
			return m, nil
		}
		if msg.session != nil {
			m.sessionID, m.runID, m.state = msg.session.ID, 0, ""
			m.status = "idle"
			m.append(fmt.Sprintf("Session %d: %s", msg.session.ID, msg.session.Title))
			for _, item := range msg.session.Messages {
				m.append(fmt.Sprintf("[session=%d task=%d %s] %s", msg.session.ID, item.TaskID, item.Role, item.Content))
			}
			if m.quitting {
				return m, tea.Quit
			}
			return m, nil
		}
		m.runID, m.sessionID, m.state = msg.run.ID, msg.run.SessionID, msg.run.State
		m.status = string(m.state)
		m.append(fmt.Sprintf("--- session=%d task=%d run=%d state=%s ---", msg.run.SessionID, msg.run.TaskID, msg.run.ID, msg.run.State))
		if m.quitting {
			return m.beginCancel()
		}
		m.watchCtx, m.stopWatch = context.WithCancel(m.ctx)
		m.deltas, m.unsubscribe = m.service.Subscribe(0)
		// 在 resume/respond/watch 之间保留每个 run 的游标，避免已经渲染过的
		// 持久化消息在重新 attach 时再次追加。
		m.runIDs = map[domain.RunID]bool{m.runID: true}
		m.shownInputs = map[domain.InputRequestID]bool{}
		m.previews = map[streamKey]string{}
		m.previewOrder, m.waitingHuman = nil, false
		return m, tea.Batch(m.snapshot(), waitDelta(m.watchCtx, m.deltas, m.generation))
	case pollMsg:
		if msg.generation != m.generation || m.stopWatch == nil {
			return m, nil
		}
		if m.deltas == nil {
			m.deltas, m.unsubscribe = m.service.Subscribe(0)
			return m, tea.Batch(m.snapshot(), waitDelta(m.watchCtx, m.deltas, m.generation))
		}
		return m, m.snapshot()
	case snapshotMsg:
		if msg.generation != m.generation || m.stopWatch == nil {
			return m, nil
		}
		if msg.err != nil {
			// 丢失观察者并不拥有或取消运行。继续轮询持久化游标；
			// 后续成功读取会补全缺失事件。
			m.status = "observation error: " + msg.err.Error()
			return m, nextPoll(m.generation)
		}
		for _, event := range msg.events {
			if event.Sequence <= m.cursors[event.RunID] {
				continue
			}
			m.appendEvent(event)
		}
		m.cursors, m.runIDs = msg.cursors, msg.ids
		for _, input := range msg.inputs {
			if m.shownInputs[input.ID] {
				continue
			}
			m.shownInputs[input.ID] = true
			data, _ := json.MarshalIndent(input, "", "  ")
			m.append(string(data))
			if input.Kind == "approval" {
				m.append(fmt.Sprintf("/approve %d  or  /reject %d", input.ID, input.ID))
			} else {
				m.append(fmt.Sprintf("/respond %d TEXT", input.ID))
			}
		}
		if msg.run == nil {
			m.status = "observation error: missing run"
			return m, nextPoll(m.generation)
		}
		m.state, m.status = msg.run.State, string(msg.run.State)
		m.waitingHuman = msg.run.State == domain.Waiting && len(msg.inputs) > 0
		m.childrenActive = msg.childrenActive
		if m.waitingHuman {
			m.status = fmt.Sprintf("waiting for input/approval ID=%d (saved, not failed)", msg.inputs[0].ID)
		} else if m.state == domain.Waiting {
			m.status = "waiting for child tasks; observing"
		}
		if m.state.Terminal() || m.state == domain.Interrupted || m.state == domain.Reconciling {
			m.previews, m.previewOrder = nil, nil
			m.append(fmt.Sprintf("--- run=%d state=%s waiting=%d error=%q ---", msg.run.ID, msg.run.State, msg.run.WaitingID, msg.run.Error))
			m.stopObserving()
			return m, nil
		}
		return m, nextPoll(m.generation)
	case deltaMsg:
		if msg.generation != m.generation || m.stopWatch == nil {
			return m, nil
		}
		if msg.closed {
			if m.unsubscribe != nil {
				m.unsubscribe()
				m.unsubscribe = nil
			}
			m.deltas = nil
			return m, nil // The existing poll chain will reconnect with the same cursors.
		}
		if msg.event.Kind == "delta" && m.runIDs[msg.event.RunID] {
			m.addDelta(msg.event)
		}
		return m, waitDelta(m.watchCtx, m.deltas, m.generation)
	case cancelMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.cancelPending = false
		if msg.err != nil {
			m.quitting = false
			m.append("Cancellation error: " + msg.err.Error())
			if msg.run != nil {
				m.state = msg.run.State
				m.status = string(m.state)
			}
			return m, nil
		}
		if msg.run == nil {
			m.quitting = false
			m.append("Cancellation error: service returned no run")
			return m, nil
		}
		for _, event := range msg.events {
			if event.Sequence > m.cursors[event.RunID] {
				m.appendEvent(event)
				m.cursors[event.RunID] = event.Sequence
			}
		}
		m.previews, m.previewOrder = nil, nil
		m.state, m.status = msg.run.State, string(msg.run.State)
		m.append(fmt.Sprintf("--- run=%d state=%s; execution stopped ---", msg.run.ID, msg.run.State))
		m.stopObserving()
		return m, tea.Quit
	case runsMsg:
		if msg.err != nil {
			m.append("Error: " + msg.err.Error())
			return m, nil
		}
		for _, r := range msg.runs {
			m.append(fmt.Sprintf("session=%d task=%d run=%d root=%d state=%s waiting=%d", r.SessionID, r.TaskID, r.ID, r.RootRunID, r.State, r.WaitingID))
		}
		if len(msg.runs) == 0 {
			m.append("No runs.")
		}
		return m, nil
	}
	var cmd, vpCmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	return m, tea.Batch(cmd, vpCmd)
}

// requestExit 处理退出请求：若有活跃运行则先发起取消，否则直接退出。
func (m model) requestExit() (tea.Model, tea.Cmd) {
	if m.cancelPending {
		return m, nil
	}
	m.quitting = true
	if m.busy {
		m.status = "waiting for submission before cancel"
		return m, nil
	}
	if m.executing() {
		return m.beginCancel()
	}
	m.stopObserving()
	return m, tea.Quit
}

// beginCancel 发起对当前运行的取消，并通过 operationGroup 跟踪取消命令。
func (m model) beginCancel() (tea.Model, tea.Cmd) {
	m.cancelPending = true
	m.status = "cancelling; waiting for execution to stop"
	s, id, generation := m.service, m.runID, m.generation
	ctx := context.WithoutCancel(m.ctx)
	finalModel := m
	finalModel.watchCtx = ctx
	finalSnapshot := finalModel.snapshot()
	return m, m.operations.track(func() tea.Msg {
		if err := s.Cancel(ctx, id); err != nil {
			r, _ := s.Run(ctx, id)
			return cancelMsg{generation: generation, run: r, err: err}
		}
		r, err := s.Wait(ctx, id)
		if err != nil {
			return cancelMsg{generation: generation, run: r, err: err}
		}
		snapshot := finalSnapshot().(snapshotMsg)
		return cancelMsg{generation: generation, run: r, err: snapshot.err, events: snapshot.events}
	})
}

// dispatch 解析用户输入的命令或普通提交，构造对应的 operation 闭包。
func (m model) dispatch(value string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return m, nil
	}
	command := fields[0]
	if command == "/help" {
		if len(fields) != 1 {
			m.append("Usage: /help")
			return m, nil
		}
		m.input.Reset()
		m.append(welcomeText())
		return m, nil
	}
	if command == "/runs" {
		if len(fields) != 1 {
			m.append("Usage: /runs")
			return m, nil
		}
		m.input.Reset()
		s, ctx := m.service, m.ctx
		return m, func() tea.Msg { runs, err := s.Runs(ctx); return runsMsg{runs, err} }
	}
	responding := m.waitingHuman && (command == "/respond" || command == "/approve" || command == "/reject")
	if m.busy || (m.executing() && !responding) {
		m.append("A run is active. Keep the draft, or Ctrl+C to cancel and exit.")
		return m, nil
	}
	if command == "/mode" {
		if len(fields) != 2 || !validMode(fields[1]) {
			m.append("Unknown mode; use ask/code/plan/agent/review/test/terminal/deliberate")
			return m, nil
		}
		m.modeName = fields[1]
		m.input.Reset()
		m.append("Mode: " + m.modeName)
		return m, nil
	}
	s, ctx := m.service, m.ctx
	var operation func() operationMsg
	if strings.HasPrefix(command, "/") {
		switch command {
		case "/session", "/watch", "/resume", "/retry", "/approve", "/reject", "/respond":
			if len(fields) < 2 || (command != "/respond" && len(fields) != 2) || (command == "/respond" && len(fields) < 3) {
				m.append("Usage: " + command + " ID" + map[bool]string{true: " TEXT"}[command == "/respond"])
				return m, nil
			}
			id, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || id <= 0 || strings.IndexFunc(fields[1], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				m.append("Invalid positive ID: " + fields[1])
				return m, nil
			}
			// 保留完整的响应文本，而不是对其空格重新分词。
			response := ""
			if command == "/respond" {
				rest := strings.TrimSpace(strings.TrimPrefix(value, command))
				response = strings.TrimSpace(strings.TrimPrefix(rest, fields[1]))
			}
			operation = func() operationMsg {
				var result operationMsg
				switch command {
				case "/session":
					result.session, result.err = s.Session(ctx, domain.SessionID(id))
				case "/watch":
					result.run, result.err = s.Run(ctx, domain.RunID(id))
				case "/resume":
					result.run, result.err = s.ResumeRun(ctx, domain.RunID(id))
				case "/retry":
					result.run, result.err = s.RetryTask(ctx, domain.TaskID(id))
				case "/approve", "/reject":
					result.run, result.err = s.RespondToInput(ctx, domain.InputRequestID(id), "", command == "/approve")
				case "/respond":
					result.run, result.err = s.RespondToInput(ctx, domain.InputRequestID(id), response, false)
				}
				return result
			}
		default:
			m.append("Unknown command: " + command)
			return m, nil
		}
	} else {
		// 新请求不包含之前的 invocation 游标、工具追踪、预算或临时上下文，
		// 下一次上下文完全由 Service 构造。
		req := app.SubmitRequest{SessionID: m.sessionID, Input: value, Mode: m.modeName}
		operation = func() operationMsg {
			var r *domain.Run
			var err error
			if req.SessionID == 0 {
				r, err = s.Submit(ctx, req)
			} else {
				r, err = s.ContinueSession(ctx, req.SessionID, req)
			}
			return operationMsg{run: r, err: err}
		}
	}
	m.stopObserving()
	m.generation++
	generation := m.generation
	m.busy = true
	m.status = "submitting"
	m.input.Reset()
	m.append("> " + value)
	return m, m.operations.track(func() tea.Msg { result := operation(); result.generation = generation; return result })
}

// snapshot 构造一个命令，获取当前运行、所有相关运行的事件和待处理输入。
func (m model) snapshot() tea.Cmd {
	s, ctx, id, generation := m.service, m.watchCtx, m.runID, m.generation
	cursors := make(map[domain.RunID]int64, len(m.cursors))
	for id, after := range m.cursors {
		cursors[id] = after
	}
	return func() tea.Msg {
		result := snapshotMsg{generation: generation, cursors: cursors}
		result.run, result.err = s.Run(ctx, id)
		if result.err != nil {
			return result
		}
		runs, err := s.Runs(ctx)
		if err != nil {
			result.err = err
			return result
		}
		root := id
		if result.run != nil && result.run.RootRunID != 0 {
			root = result.run.RootRunID
		}
		result.ids = map[domain.RunID]bool{root: true, id: true}
		for _, r := range runs {
			if r.RootRunID == root {
				result.ids[r.ID] = true
				if r.ID != id && (r.State == domain.Queued || r.State == domain.Running || r.State == domain.Cancelling) {
					result.childrenActive = true
				}
			}
		}
		for runID := range result.ids {
			after := cursors[runID]
			for {
				events, err := s.Observe(ctx, runID, after)
				if err != nil {
					result.err = err
					return result
				}
				if len(events) == 0 {
					break
				}
				sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
				previous := after
				for _, event := range events {
					if event.RunID == runID && event.Sequence > after {
						result.events = append(result.events, event)
						after = event.Sequence
					}
				}
				if previous == after {
					result.err = fmt.Errorf("observe run %d returned no advancing sequence", runID)
					return result
				}
			}
			cursors[runID] = after
		}
		sort.SliceStable(result.events, func(i, j int) bool { return result.events[i].Sequence < result.events[j].Sequence })
		result.inputs, result.err = s.PendingInputs(ctx, id)
		return result
	}
}

// waitDelta 等待下一个流式增量事件或通道关闭。
func waitDelta(ctx context.Context, ch <-chan domain.Event, generation int) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return deltaMsg{generation: generation, closed: true}
		case event, ok := <-ch:
			return deltaMsg{generation: generation, event: event, closed: !ok}
		}
	}
}

// nextPoll 在 100ms 后发送一个 pollMsg 触发下一次快照轮询。
func nextPoll(generation int) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return pollMsg{generation} })
}

// validMode 判断模式名称是否合法。
func validMode(name string) bool {
	switch name {
	case "ask", "code", "plan", "agent", "review", "test", "terminal", "deliberate":
		return true
	}
	return false
}

// clamp 将 n 限制在 [low, high] 区间内。
func clamp(n, low, high int) int { return min(high, max(low, n)) }

// safeText 清理字符串：替换非法 UTF-8、制表符、控制字符和格式字符，
// 以防止终端渲染异常。
func safeText(text string) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.ReplaceAll(text, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && r != '\n') || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return '�'
		}
		return r
	}, text)
}

// View 实现 tea.Model 接口，渲染头部、视口和输入框，并对内容做安全截断。
func (m model) View() string {
	if !m.ready {
		return "Orca TUI — waiting for terminal size"
	}
	header := fmt.Sprintf("Orca | session=%d run=%d | %s | mode=%s", m.sessionID, m.runID, strings.ReplaceAll(safeText(m.status), "\n", " "), m.modeName)
	// 仅清理显示副本：即便是恶意粘贴也不能到达终端，
	// 且 resize 不能替换真正的草稿或控件状态。
	input := m.input
	if clean := strings.ReplaceAll(safeText(input.Value()), "\n", " "); clean != input.Value() {
		input.SetValue(clean)
	}
	lines := strings.Split(header+"\n"+m.viewport.View()+"\n"+input.View(), "\n")
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}

// welcomeText 返回 TUI 欢迎信息，包含使用说明和可用命令列表。
func welcomeText() string {
	return "Orca TUI\nEnter a task; later tasks continue the same session with fresh task/run identities.\n/mode NAME  /session ID  /watch RUN_ID  /resume ID  /retry TASK_ID  /runs\n/approve INPUT_ID  /reject INPUT_ID  /respond INPUT_ID TEXT\nWaiting/reconciling runs are saved, not failed. Ctrl+C cancels active work before exit.\nStreaming text is provisional; complete durable messages replace it."
}
