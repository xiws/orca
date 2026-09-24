// Package app 提供核心应用服务层，协调会话管理、任务提交、工作流驱动和事件发布。
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/workflow"
)

// Store 定义状态存储接口，支持 mutation 原子提交和各种实体查询。
type Store interface {
	Apply(context.Context, domain.Mutation) error
	Session(context.Context, domain.SessionID) (*domain.Session, error)
	Sessions(context.Context) ([]domain.Session, error)
	Task(context.Context, domain.TaskID, int) (*domain.Task, error)
	Run(context.Context, domain.RunID) (*domain.Run, error)
	Runs(context.Context) ([]domain.Run, error)
	Input(context.Context, domain.InputRequestID) (*domain.InputRequest, error)
	Invocation(context.Context, domain.InvocationID) (*domain.Invocation, error)
	Invocations(context.Context, domain.RunID) ([]domain.Invocation, error)
	Events(context.Context, domain.RunID, int64, int) ([]domain.Event, error)
	Tools(context.Context, domain.RunID) ([]domain.ToolExecution, error)
	Inputs(context.Context, domain.RunID) ([]domain.InputRequest, error)
	HasUnresolved(context.Context, domain.RunID) (bool, error)
	DeleteSessions(context.Context, []domain.SessionID) error
}

// Workflow 定义工作流驱动接口。
type Workflow interface {
	Drive(context.Context, domain.RunID) (*domain.Run, error)
}

// Options 定义服务配置选项，包括工作空间、默认模型、策略限制等。
type Options struct {
	Workspace     string                                 // 工作空间路径
	DefaultModel  model.Ref                              // 默认模型引用
	Policy        domain.Policy                          // 全局工具策略
	Limits        domain.Limits                          // 执行限制
	SystemPrompt  func(model.Ref) string                 // 系统提示生成函数
	ValidateModel func(context.Context, model.Ref) error // 模型校验函数
}

// SubmitRequest 定义任务提交请求参数。
type SubmitRequest struct {
	SessionID    domain.SessionID // 会话 ID（0 表示新建会话）
	Input        string           // 用户输入
	Prompt       string           // 实际提示词
	SystemPrompt string           // 自定义系统提示
	Model        model.Ref        // 模型引用
	Mode         string           // 工作流模式
}

// Service 是核心应用服务，管理运行、会话和事件。
type Service struct {
	store     Store
	workflow  Workflow
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{} // 唤醒后台 worker 的信号通道
	done      chan struct{} // worker 退出信号
	mu        sync.Mutex
	active    map[domain.RunID]context.CancelFunc // 当前活跃的根运行
	subs      map[int]subscription                // 事件订阅者
	nextSub   int
	workerErr error // worker 最后错误
}

// subscription 表示一个事件订阅。
type subscription struct {
	runID domain.RunID
	ch    chan domain.Event
}

// NewService 创建并启动应用服务，启动后台 worker 协程。
func NewService(store Store, runner Workflow, options Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{store: store, workflow: runner, options: options, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), active: map[domain.RunID]context.CancelFunc{}, subs: map[int]subscription{}}
	go s.work()
	return s
}

// Close 关闭服务，等待 worker 退出并关闭所有订阅通道。
func (s *Service) Close() error {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sub := range s.subs {
		close(sub.ch)
		delete(s.subs, id)
	}
	return s.workerErr
}

// Submit 提交新任务，创建运行并入队执行。支持重试以处理并发冲突。
func (s *Service) Submit(ctx context.Context, req SubmitRequest) (*domain.Run, error) {
	if strings.TrimSpace(req.Input) == "" {
		return nil, fmt.Errorf("goal cannot be empty")
	}
	template, err := workflow.Mode(req.Mode)
	if err != nil {
		return nil, err
	}
	// 检查是否存在结果未知的运行
	if unknown, err := s.store.HasUnresolved(ctx, 0); err != nil {
		return nil, err
	} else if unknown {
		return nil, domain.ErrUnknown
	}
	// 最多重试 8 次以处理 CAS 冲突
	for attempt := 0; attempt < 8; attempt++ {
		var session *domain.Session
		if req.SessionID != 0 {
			session, err = s.store.Session(ctx, req.SessionID)
		} else {
			session = domain.NewSession(s.options.Workspace)
		}
		if err != nil {
			return nil, err
		}
		if session.ProjectPath != s.options.Workspace {
			return nil, fmt.Errorf("session belongs to workspace %s", session.ProjectPath)
		}
		// 解析模型引用：未指定时使用默认模型或会话中最新的模型
		ref := req.Model
		if (ref.Provider == "") != (ref.Model == "") {
			return nil, fmt.Errorf("provider and model must be supplied together")
		}
		if ref.Provider == "" {
			ref = s.options.DefaultModel
			runs, err := s.store.Runs(ctx)
			if err != nil {
				return nil, err
			}
			var latest int64
			for _, run := range runs {
				if run.SessionID == session.ID && run.ParentRunID == 0 && run.CreatedAt > latest {
					ref = run.Model
					latest = run.CreatedAt
				}
			}
		}
		if s.options.ValidateModel != nil {
			if err := s.options.ValidateModel(ctx, ref); err != nil {
				return nil, err
			}
		}
		// 创建任务和运行
		title := []rune(req.Input)
		if len(title) > 80 {
			title = title[:80]
		}
		task := domain.NewTask(req.Input, string(title), session.ID)
		prompt := req.Prompt
		if prompt == "" {
			prompt = req.Input
		}
		system := req.SystemPrompt
		if system == "" && s.options.SystemPrompt != nil {
			system = s.options.SystemPrompt(ref)
		}
		run := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: task.ID, TaskVersion: task.Version, SessionID: session.ID, Workspace: session.ProjectPath, Mode: template.Name, TemplateVersion: template.Version, Model: ref, Policy: s.options.Policy.Intersect(template.Policy), Limits: s.options.Limits, State: domain.Queued, Nodes: template.Nodes(), Prompt: prompt, SystemPrompt: system, CreatedAt: time.Now().UnixNano()}
		run.RootRunID = run.ID
		session.AppendUser(task.ID, prompt)
		if session.Title == "" {
			session.Title = string(title)
		}
		err = s.store.Apply(ctx, domain.Mutation{Sessions: []*domain.Session{session}, Tasks: []*domain.Task{task}, Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: run.ID, Kind: "state", Content: "queued"}}})
		if errors.Is(err, domain.ErrConflict) && req.SessionID != 0 {
			continue // CAS 冲突时重试
		}
		if err != nil {
			return nil, err
		}
		s.signal()
		return run, nil
	}
	return nil, domain.ErrConflict
}

// ContinueSession 在已有会话中继续提交新任务。
func (s *Service) ContinueSession(ctx context.Context, id domain.SessionID, req SubmitRequest) (*domain.Run, error) {
	req.SessionID = id
	return s.Submit(ctx, req)
}

// Run 查询单个运行。
func (s *Service) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	return s.store.Run(ctx, id)
}

// Runs 查询所有运行。
func (s *Service) Runs(ctx context.Context) ([]domain.Run, error) { return s.store.Runs(ctx) }

// Sessions 查询所有会话。
func (s *Service) Sessions(ctx context.Context) ([]domain.Session, error) {
	return s.store.Sessions(ctx)
}

// Session 查询单个会话。
func (s *Service) Session(ctx context.Context, id domain.SessionID) (*domain.Session, error) {
	return s.store.Session(ctx, id)
}

// Observe 查询运行的事件流，最多返回 256 条。
func (s *Service) Observe(ctx context.Context, id domain.RunID, after int64) ([]domain.Event, error) {
	return s.store.Events(ctx, id, after, 256)
}

// Subscribe 订阅指定运行的实时事件，返回事件通道和取消函数。runID 为 0 时订阅所有运行。
func (s *Service) Subscribe(id domain.RunID) (<-chan domain.Event, func()) {
	s.mu.Lock()
	key := s.nextSub
	s.nextSub++
	ch := make(chan domain.Event, 64)
	s.subs[key] = subscription{runID: id, ch: ch}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if sub, ok := s.subs[key]; ok {
			delete(s.subs, key)
			close(sub.ch)
		}
	}
}

// Publish 向所有匹配的订阅者发布事件。
func (s *Service) Publish(event domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.subs {
		// runID 为 0 表示订阅所有运行
		if sub.runID == 0 || sub.runID == event.RunID {
			select {
			case sub.ch <- event:
			default: // 非阻塞发送，通道满时丢弃
			}
		}
	}
}

// Wait 等待运行进入终态、中断或等待用户输入。
func (s *Service) Wait(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := s.store.Run(ctx, id)
		if err != nil {
			return nil, err
		}
		// 终态、需协调或已中断时直接返回
		if run.State.Terminal() || run.State == domain.Reconciling || run.State == domain.Interrupted {
			return run, nil
		}
		// 等待状态且有待处理的输入时返回
		if run.State == domain.Waiting {
			inputs, err := s.PendingInputs(ctx, id)
			if err != nil {
				return nil, err
			}
			if len(inputs) > 0 {
				return run, nil
			}
		}
		// 检查 worker 是否出错
		s.mu.Lock()
		workerErr := s.workerErr
		s.mu.Unlock()
		if workerErr != nil {
			return nil, workerErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// PendingInputs 查询运行树中所有待处理的用户输入请求。
func (s *Service) PendingInputs(ctx context.Context, id domain.RunID) ([]domain.InputRequest, error) {
	run, err := s.store.Run(ctx, id)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.InputRequest
	// 遍历运行树中所有未终止的运行，收集 pending 状态的输入
	for _, r := range runs {
		if r.RootRunID != run.RootRunID || r.State.Terminal() {
			continue
		}
		inputs, err := s.store.Inputs(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, input := range inputs {
			if input.State == "pending" {
				result = append(result, input)
			}
		}
	}
	return result, nil
}

// RespondToInput 响应用户输入请求，恢复运行为 Running 状态。
func (s *Service) RespondToInput(ctx context.Context, id domain.InputRequestID, response string, approved bool) (*domain.Run, error) {
	input, err := s.store.Input(ctx, id)
	if err != nil {
		return nil, err
	}
	run, err := s.store.Run(ctx, input.RunID)
	if err != nil {
		return nil, err
	}
	// 已回答的输入：校验响应一致性
	if input.State == "answered" {
		if input.Response != response || input.Approved != approved {
			return nil, fmt.Errorf("input already answered differently")
		}
		return s.store.Run(ctx, run.RootRunID)
	}
	// 校验输入是否仍有效
	if input.State != "pending" || (run.State != domain.Waiting && run.State != domain.Running) || (input.ExpiresAt != 0 && input.ExpiresAt <= time.Now().Unix()) {
		return nil, fmt.Errorf("input expired or run not waiting")
	}
	if input.Kind == "input" && strings.TrimSpace(response) == "" {
		return nil, fmt.Errorf("input response cannot be empty")
	}
	// 更新输入状态并恢复运行
	input.State = "answered"
	input.Response = response
	input.Approved = approved
	run.Policy = run.Policy.Intersect(s.options.Policy)
	run.State = domain.Running
	run.WaitingID = 0
	if err := s.store.Apply(ctx, domain.Mutation{Inputs: []*domain.InputRequest{input}, Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: run.ID, Kind: "input", Content: fmt.Sprintf("answered %d", id)}}}); err != nil {
		return nil, err
	}
	s.signal()
	return s.store.Run(ctx, run.RootRunID)
}

// ResumeRun 恢复被中断的运行，重新应用全局策略。
func (s *Service) ResumeRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	run, err := s.store.Run(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.State != domain.Interrupted && run.State != domain.Queued {
		return nil, fmt.Errorf("only queued or interrupted runs can resume, got %s", run.State)
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return nil, err
	}
	// 确认不存在结果未知的运行
	if unknown, err := s.store.HasUnresolved(ctx, 0); err != nil {
		return nil, err
	} else if unknown {
		return nil, domain.ErrUnknown
	}
	mutation := domain.Mutation{}
	// 将运行树中的中断运行恢复为运行中，并重新应用策略
	for i := range runs {
		r := &runs[i]
		if r.RootRunID != run.RootRunID || r.State.Terminal() {
			continue
		}
		if r.State == domain.Interrupted {
			r.State = domain.Running
		}
		r.Policy = r.Policy.Intersect(s.options.Policy)
		mutation.Runs = append(mutation.Runs, r)
	}
	if err := s.store.Apply(ctx, mutation); err != nil {
		return nil, err
	}
	s.signal()
	return s.store.Run(ctx, run.RootRunID)
}

// RetryTask 重试已完成的任务，创建新的运行。
func (s *Service) RetryTask(ctx context.Context, id domain.TaskID) (*domain.Run, error) {
	task, err := s.store.Task(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return nil, err
	}
	// 找到该任务最新的已完成运行
	var previous *domain.Run
	for i := range runs {
		r := &runs[i]
		if r.TaskID == id {
			if !r.State.Terminal() {
				return nil, fmt.Errorf("task still has active or unresolved execution")
			}
			if previous == nil || r.CreatedAt > previous.CreatedAt {
				previous = r
			}
		}
	}
	if previous == nil {
		return nil, domain.ErrNotFound
	}
	if unknown, err := s.store.HasUnresolved(ctx, 0); err != nil {
		return nil, err
	} else if unknown {
		return nil, domain.ErrUnknown
	}
	template, err := workflow.Mode(previous.Mode)
	if err != nil {
		return nil, err
	}
	// 使用之前运行的配置创建新运行
	run := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: id, TaskVersion: task.Version, SessionID: task.SessionID, Workspace: previous.Workspace, Mode: previous.Mode, TemplateVersion: 1, Model: previous.Model, Policy: previous.Policy.Intersect(s.options.Policy), Limits: s.options.Limits, State: domain.Queued, Nodes: template.Nodes(), Prompt: task.Input, SystemPrompt: previous.SystemPrompt, CreatedAt: time.Now().UnixNano()}
	run.RootRunID = run.ID
	if err := s.store.Apply(ctx, domain.Mutation{Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: run.ID, Kind: "state", Content: "queued"}}}); err != nil {
		return nil, err
	}
	s.signal()
	return run, nil
}

// Cancel 取消运行树中的所有运行。
func (s *Service) Cancel(ctx context.Context, id domain.RunID) error {
	run, err := s.store.Run(ctx, id)
	if err != nil {
		return err
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return err
	}
	mutation := domain.Mutation{}
	// 将运行树中所有未终止的运行标记为 Cancelling
	for i := range runs {
		r := &runs[i]
		if r.RootRunID != run.RootRunID || r.State.Terminal() {
			continue
		}
		if r.State == domain.Reconciling {
			return domain.ErrUnknown
		}
		r.State = domain.Cancelling
		mutation.Runs = append(mutation.Runs, r)
	}
	if err := s.store.Apply(ctx, mutation); err != nil {
		return err
	}
	// 如果运行正在活跃，通过 cancel 函数中断；否则驱动一次以完成状态转换
	s.mu.Lock()
	cancel := s.active[run.RootRunID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	} else if _, err := s.workflow.Drive(ctx, run.RootRunID); err != nil && !errors.Is(err, domain.ErrConflict) {
		return err
	}
	s.signal()
	return nil
}

// signal 非阻塞地唤醒后台 worker。
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// work 是后台 worker 协程，持续检查并驱动待执行的运行。
func (s *Service) work() {
	defer close(s.done)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		for s.ctx.Err() == nil {
			runs, err := s.store.Runs(s.ctx)
			if err != nil {
				s.setWorkerError(err)
				break
			}
			unknown, err := s.store.HasUnresolved(s.ctx, 0)
			if err != nil {
				s.setWorkerError(err)
				break
			}
			// 按创建时间排序，确保先入先出
			sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt < runs[j].CreatedAt })
			var next *domain.Run
			// 选择下一个可执行的根运行
			for i := range runs {
				r := &runs[i]
				// 跳过子运行、已终止、需协调或已中断的运行
				if r.ParentRunID != 0 || r.State.Terminal() || r.State == domain.Reconciling || r.State == domain.Interrupted {
					continue
				}
				// 存在未知结果时只允许取消操作
				if unknown && r.State != domain.Cancelling {
					continue
				}
				// 检查同一会话中是否有更早的运行阻塞
				blocked := false
				if r.State != domain.Cancelling {
					for j := 0; j < i; j++ {
						if runs[j].ParentRunID == 0 && runs[j].SessionID == r.SessionID && !runs[j].State.Terminal() {
							blocked = true
							break
						}
					}
				}
				if blocked {
					continue
				}
				// 等待状态的运行只在子运行就绪时才处理
				if r.State == domain.Waiting {
					ready := false
					for _, c := range runs {
						if c.RootRunID == r.ID && (c.State == domain.Queued || c.State == domain.Running || c.State == domain.Cancelling) {
							ready = true
						}
					}
					if !ready {
						continue
					}
				}
				next = r
				break
			}
			if next == nil {
				break
			}
			// 注册活跃运行并驱动工作流
			ctx, cancel := context.WithCancel(s.ctx)
			s.mu.Lock()
			s.active[next.ID] = cancel
			s.mu.Unlock()
			_, err = s.workflow.Drive(ctx, next.ID)
			cancel()
			s.mu.Lock()
			delete(s.active, next.ID)
			s.mu.Unlock()
			if err != nil {
				s.setWorkerError(err)
				break
			}
		}
	}
}

// setWorkerError 记录 worker 错误，忽略上下文取消导致的非检查点错误。
func (s *Service) setWorkerError(err error) {
	if s.ctx.Err() != nil && errors.Is(err, context.Canceled) && !errors.Is(err, domain.ErrCheckpoint) {
		return
	}
	s.mu.Lock()
	s.workerErr = err
	s.mu.Unlock()
}
