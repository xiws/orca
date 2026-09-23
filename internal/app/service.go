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

type Workflow interface {
	Drive(context.Context, domain.RunID) (*domain.Run, error)
}

type Options struct {
	Workspace     string
	DefaultModel  model.Ref
	Policy        domain.Policy
	Limits        domain.Limits
	SystemPrompt  func(model.Ref) string
	ValidateModel func(context.Context, model.Ref) error
}

type SubmitRequest struct {
	SessionID    domain.SessionID
	Input        string
	Prompt       string
	SystemPrompt string
	Model        model.Ref
	Mode         string
}

type Service struct {
	store     Store
	workflow  Workflow
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	done      chan struct{}
	mu        sync.Mutex
	active    map[domain.RunID]context.CancelFunc
	subs      map[int]subscription
	nextSub   int
	workerErr error
}

type subscription struct {
	runID domain.RunID
	ch    chan domain.Event
}

func NewService(store Store, runner Workflow, options Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{store: store, workflow: runner, options: options, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), active: map[domain.RunID]context.CancelFunc{}, subs: map[int]subscription{}}
	go s.work()
	return s
}

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

func (s *Service) Submit(ctx context.Context, req SubmitRequest) (*domain.Run, error) {
	if strings.TrimSpace(req.Input) == "" {
		return nil, fmt.Errorf("goal cannot be empty")
	}
	template, err := workflow.Mode(req.Mode)
	if err != nil {
		return nil, err
	}
	if unknown, err := s.store.HasUnresolved(ctx, 0); err != nil {
		return nil, err
	} else if unknown {
		return nil, domain.ErrUnknown
	}
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
			continue
		}
		if err != nil {
			return nil, err
		}
		s.signal()
		return run, nil
	}
	return nil, domain.ErrConflict
}

func (s *Service) ContinueSession(ctx context.Context, id domain.SessionID, req SubmitRequest) (*domain.Run, error) {
	req.SessionID = id
	return s.Submit(ctx, req)
}
func (s *Service) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	return s.store.Run(ctx, id)
}
func (s *Service) Runs(ctx context.Context) ([]domain.Run, error) { return s.store.Runs(ctx) }
func (s *Service) Sessions(ctx context.Context) ([]domain.Session, error) {
	return s.store.Sessions(ctx)
}
func (s *Service) Session(ctx context.Context, id domain.SessionID) (*domain.Session, error) {
	return s.store.Session(ctx, id)
}

func (s *Service) Observe(ctx context.Context, id domain.RunID, after int64) ([]domain.Event, error) {
	return s.store.Events(ctx, id, after, 256)
}

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

func (s *Service) Publish(event domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.subs {
		if sub.runID == 0 || sub.runID == event.RunID {
			select {
			case sub.ch <- event:
			default:
			}
		}
	}
}

func (s *Service) Wait(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := s.store.Run(ctx, id)
		if err != nil {
			return nil, err
		}
		if run.State.Terminal() || run.State == domain.Reconciling || run.State == domain.Interrupted {
			return run, nil
		}
		if run.State == domain.Waiting {
			inputs, err := s.PendingInputs(ctx, id)
			if err != nil {
				return nil, err
			}
			if len(inputs) > 0 {
				return run, nil
			}
		}
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

func (s *Service) RespondToInput(ctx context.Context, id domain.InputRequestID, response string, approved bool) (*domain.Run, error) {
	input, err := s.store.Input(ctx, id)
	if err != nil {
		return nil, err
	}
	run, err := s.store.Run(ctx, input.RunID)
	if err != nil {
		return nil, err
	}
	if input.State == "answered" {
		if input.Response != response || input.Approved != approved {
			return nil, fmt.Errorf("input already answered differently")
		}
		return s.store.Run(ctx, run.RootRunID)
	}
	if input.State != "pending" || (run.State != domain.Waiting && run.State != domain.Running) || (input.ExpiresAt != 0 && input.ExpiresAt <= time.Now().Unix()) {
		return nil, fmt.Errorf("input expired or run not waiting")
	}
	if input.Kind == "input" && strings.TrimSpace(response) == "" {
		return nil, fmt.Errorf("input response cannot be empty")
	}
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
	if unknown, err := s.store.HasUnresolved(ctx, 0); err != nil {
		return nil, err
	} else if unknown {
		return nil, domain.ErrUnknown
	}
	mutation := domain.Mutation{}
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

func (s *Service) RetryTask(ctx context.Context, id domain.TaskID) (*domain.Run, error) {
	task, err := s.store.Task(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return nil, err
	}
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
	run := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: id, TaskVersion: task.Version, SessionID: task.SessionID, Workspace: previous.Workspace, Mode: previous.Mode, TemplateVersion: 1, Model: previous.Model, Policy: previous.Policy.Intersect(s.options.Policy), Limits: s.options.Limits, State: domain.Queued, Nodes: template.Nodes(), Prompt: task.Input, SystemPrompt: previous.SystemPrompt, CreatedAt: time.Now().UnixNano()}
	run.RootRunID = run.ID
	if err := s.store.Apply(ctx, domain.Mutation{Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: run.ID, Kind: "state", Content: "queued"}}}); err != nil {
		return nil, err
	}
	s.signal()
	return run, nil
}

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

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

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
			sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt < runs[j].CreatedAt })
			var next *domain.Run
			for i := range runs {
				r := &runs[i]
				if r.ParentRunID != 0 || r.State.Terminal() || r.State == domain.Reconciling || r.State == domain.Interrupted {
					continue
				}
				if unknown && r.State != domain.Cancelling {
					continue
				}
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

func (s *Service) setWorkerError(err error) {
	if s.ctx.Err() != nil && errors.Is(err, context.Canceled) && !errors.Is(err, domain.ErrCheckpoint) {
		return
	}
	s.mu.Lock()
	s.workerErr = err
	s.mu.Unlock()
}
