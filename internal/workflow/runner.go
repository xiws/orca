// 工作流执行引擎：驱动多节点工作流的生命周期，包括调度、并发执行、委托和崩溃恢复
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

// ExecutionStore 定义工作流运行所需的持久化存储接口
type ExecutionStore interface {
	Apply(context.Context, domain.Mutation) error
	Session(context.Context, domain.SessionID) (*domain.Session, error)
	Task(context.Context, domain.TaskID, int) (*domain.Task, error)
	Run(context.Context, domain.RunID) (*domain.Run, error)
	Runs(context.Context) ([]domain.Run, error)
	Invocation(context.Context, domain.InvocationID) (*domain.Invocation, error)
	Input(context.Context, domain.InputRequestID) (*domain.InputRequest, error)
	InputByCall(context.Context, string) (*domain.InputRequest, error)
	Tool(context.Context, string) (*domain.ToolExecution, error)
	Delegation(context.Context, string) (*domain.Delegation, error)
}

// AgentRunner 定义智能体执行接口，驱动单次模型调用并返回结果
type AgentRunner interface {
	Advance(context.Context, domain.InvocationID, string) agent.Outcome
}

// Runner 工作流执行器，管理并发调度和状态推进
type Runner struct {
	store  ExecutionStore // 持久化存储
	agent  AgentRunner    // 智能体执行器
	slots  chan struct{}  // 并发控制信号量
	active sync.Map       // 当前活跃的根运行ID集合
}

// NewRunner 创建执行器，limit 控制并发调用的上限
func NewRunner(store ExecutionStore, runner AgentRunner, concurrency ...int) *Runner {
	limit := 4
	if len(concurrency) > 0 && concurrency[0] > 0 {
		limit = concurrency[0]
	}
	return &Runner{store: store, agent: runner, slots: make(chan struct{}, limit)}
}

// work 表示一次待执行的节点调度单元
type work struct {
	runID        domain.RunID
	nodeID       string
	invocationID domain.InvocationID
	resume       string // 恢复时传递给智能体的续执行内容
}

// commit 将变更写入存储，统一包装为 ErrCheckpoint 错误
func (w *Runner) commit(ctx context.Context, mutation domain.Mutation) error {
	if err := w.store.Apply(ctx, mutation); err != nil {
		return errors.Join(domain.ErrCheckpoint, err)
	}
	return nil
}

// needsInterrupt 判断错误是否需要触发中断恢复流程
func needsInterrupt(err error) bool {
	return errors.Is(err, domain.ErrUnknown) || errors.Is(err, model.ErrUnknown) || errors.Is(err, domain.ErrCheckpoint) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Drive 驱动根运行直到完成或中断，是工作流的主循环入口
func (w *Runner) Drive(ctx context.Context, rootID domain.RunID) (*domain.Run, error) {
	if _, busy := w.active.LoadOrStore(rootID, struct{}{}); busy {
		return nil, domain.ErrConflict // 同一根运行不允许并发驱动
	}
	defer w.active.Delete(rootID)
	stop := func(err error) (*domain.Run, error) {
		if needsInterrupt(err) || ctx.Err() != nil {
			return w.interrupt(context.WithoutCancel(ctx), rootID, err)
		}
		return nil, err
	}
	for {
		root, err := w.store.Run(context.WithoutCancel(ctx), rootID)
		if err != nil {
			return nil, err
		}
		if root.ParentRunID != 0 {
			return nil, fmt.Errorf("Drive requires a root run")
		}
		if root.State.Terminal() {
			return root, nil
		}
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		runs, err := w.store.Runs(ctx)
		if err != nil {
			return stop(err)
		}
		// 恢复检查：先扫描是否有子孙运行处于阻塞状态，再调度新任务
		var blocked error
		for _, run := range runs {
			if run.RootRunID != rootID {
				continue
			}
			switch run.State {
			case domain.Reconciling:
				blocked = errors.Join(blocked, domain.ErrUnknown)
			case domain.Interrupted:
				blocked = errors.Join(blocked, domain.ErrCheckpoint)
			case domain.Cancelling, domain.Cancelled:
				blocked = errors.Join(blocked, context.Canceled)
			}
		}
		if blocked != nil {
			return stop(blocked)
		}
		// 按创建时间排序，保证确定性调度
		sort.Slice(runs, func(i, j int) bool {
			if runs[i].CreatedAt == runs[j].CreatedAt {
				return runs[i].ID < runs[j].ID
			}
			return runs[i].CreatedAt < runs[j].CreatedAt
		})
		var batch []work
		progress := false
		// 遍历所有运行，推进状态机并收集待执行的节点
		for _, snapshot := range runs {
			if snapshot.RootRunID != rootID || snapshot.State.Terminal() {
				continue
			}
			jobs, changed, err := w.advance(ctx, snapshot.ID)
			if err != nil {
				return stop(err)
			}
			batch = append(batch, jobs...)
			progress = progress || changed
		}
		if len(batch) > 0 {
			if err := w.execute(ctx, rootID, batch); err != nil {
				return stop(err)
			}
			progress = true
		}
		if !progress {
			return w.store.Run(ctx, rootID)
		}
	}
}

// advance 推进单个运行的状态机，仅做持久化检查点，不调用智能体
func (w *Runner) advance(ctx context.Context, id domain.RunID) ([]work, bool, error) {
	run, err := w.store.Run(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if run.State.Terminal() {
		return nil, false, nil
	}
	if run.State == domain.Cancelling {
		return nil, false, context.Canceled
	}
	if run.State == domain.Reconciling {
		return nil, false, domain.ErrUnknown
	}
	if run.TemplateVersion != 1 {
		return nil, false, fmt.Errorf("unsupported workflow version %d", run.TemplateVersion)
	}
	changed := false
	// 冻结输入：与状态变更独立，确保输入在运行期间不可变
	if !run.InputFrozen {
		if err := w.freezeInput(ctx, run); err != nil {
			return nil, false, err
		}
		if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{run}}); err != nil {
			return nil, false, err
		}
		changed = true
	}
	var ready []int
	var resumes []string
	var waiting domain.InputRequestID
	for _, index := range readyNodes(run) {
		node := run.Nodes[index]
		resume := ""
		if node.InvocationID != 0 {
			inv, err := w.store.Invocation(ctx, node.InvocationID)
			if err != nil {
				return nil, false, err
			}
			if inv.Phase == "waiting" {
				input, err := w.invocationInput(ctx, inv)
				if err != nil {
					return nil, false, err
				}
				if input.State == "pending" {
					if waiting == 0 {
						waiting = input.ID
					}
					continue
				}
			}
			if inv.Phase == "delegated" {
				var err error
				resume, err = w.delegationResult(ctx, inv.ID)
				if err != nil {
					return nil, false, err
				}
				// 缺少 link 表示委派结果尚未提交，需要继续处理。
				key, err := pendingKey(inv)
				if err != nil {
					return nil, false, err
				}
				if _, err := w.store.Delegation(ctx, key); err == nil && resume == "" {
					continue
				} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
					return nil, false, err
				}
			}
		}
		ready = append(ready, index)
		resumes = append(resumes, resume)
	}
	if len(ready) == 0 && len(readyNodes(run)) > 0 {
		if run.State != domain.Waiting || run.WaitingID != waiting {
			if run.State != domain.Running && run.State != domain.Waiting {
				if err := run.Transition(domain.Running); err != nil {
					return nil, false, err
				}
				if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{run}}); err != nil {
					return nil, false, err
				}
			}
			run.State, run.WaitingID = domain.Waiting, waiting
			if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{run}}); err != nil {
				return nil, false, err
			}
			changed = true
		}
		return nil, changed, nil
	}
	if run.State != domain.Running {
		if err := run.Transition(domain.Running); err != nil {
			return nil, false, err
		}
		run.WaitingID = 0
		if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: id, Kind: "state", Content: "running"}}}); err != nil {
			return nil, false, err
		}
		changed = true
	}
	if len(ready) == 0 {
		return nil, true, w.finish(ctx, run)
	}
	jobs := make([]work, 0, len(ready))
	for i, index := range ready {
		node := &run.Nodes[index]
		if node.InvocationID == 0 {
			role, err := agent.Role(node.Role)
			if err != nil {
				return nil, false, err
			}
			inv := &domain.Invocation{ID: domain.InvocationID(domain.NewID()), RunID: id, NodeID: node.ID, Attempt: node.Attempt, Role: role.Name, RoleVersion: role.Version, Model: run.Model, Policy: run.Policy.Intersect(role.Capabilities), Phase: "model", Thread: domain.Thread{ContextVersion: 1}}
			inv.Thread.Messages = []model.Message{{Role: "system", Content: run.SystemPrompt + "\n\n" + role.Prompt}}
			inv.Thread.Messages = append(inv.Thread.Messages, run.Input...)
			for _, prior := range run.Nodes {
				if prior.State != "completed" {
					continue
				}
				if strings.HasPrefix(role.Name, "critic-") && strings.HasPrefix(prior.Role, "critic-") {
					continue
				}
				inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "user", Content: "Evidence from " + prior.Role + ":\n" + prior.Result})
				if prior.InvocationID != 0 {
					previous, err := w.store.Invocation(ctx, prior.InvocationID)
					if err != nil {
						return nil, false, err
					}
					for _, message := range previous.Thread.Messages {
						if message.Role == "tool" {
							inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "user", Content: "Recorded tool evidence (call ID " + message.ToolCallID + "):\n" + message.Content})
						}
					}
				}
			}
			if role.Name == "verifier" {
				evidence, _, err := w.validationEvidence(ctx, run)
				if err != nil {
					return nil, false, err
				}
				data, err := json.Marshal(evidence)
				if err != nil {
					return nil, false, err
				}
				inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "user", Content: "Verification evidence contract: evidence must be a string list. Each entry must start with an exact key (preferred), or an unambiguous call_id, from the latest validator ledger below; optional explanation follows whitespace. Never invent IDs or cite older validation. Passed requires at least one bash execution, ALL bash calls completed with ok:true and explicit exit_code:0, and a citation to a successful bash record. Missing or failed checks cannot pass.\n" + string(data)})
			}
			node.InvocationID, node.State = inv.ID, "running"
			if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{run}, Invocations: []*domain.Invocation{inv}}); err != nil {
				return nil, false, err
			}
		}
		jobs = append(jobs, work{id, node.ID, node.InvocationID, resumes[i]})
	}
	return jobs, true, nil
}

func (w *Runner) execute(ctx context.Context, rootID domain.RunID, batch []work) error {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make([]agent.Outcome, len(batch))
	var wg sync.WaitGroup
	for i, job := range batch {
		wg.Add(1)
		go func(i int, job work) {
			defer wg.Done()
			select {
			case w.slots <- struct{}{}:
				defer func() { <-w.slots }()
			case <-batchCtx.Done():
				outcomes[i] = agent.Outcome{Kind: "failed", Err: batchCtx.Err()}
				return
			}
			if err := batchCtx.Err(); err != nil {
				outcomes[i] = agent.Outcome{Kind: "failed", Err: err}
				return
			}
			outcomes[i] = w.agent.Advance(batchCtx, job.invocationID, job.resume)
			if needsInterrupt(outcomes[i].Err) {
				cancel() // 立即停止同批次的其他任务，在恢复写入前等待它们退出。
			}
		}(i, job)
	}
	wg.Wait()
	// 批次中存在不确定结果时，不提交其他成功状态。
	var interrupted error
	for _, out := range outcomes {
		if needsInterrupt(out.Err) {
			interrupted = errors.Join(interrupted, out.Err)
		}
	}
	if ctx.Err() != nil {
		interrupted = errors.Join(interrupted, ctx.Err())
	}
	if interrupted != nil {
		return interrupted
	}
	for i, out := range outcomes {
		root, err := w.store.Run(ctx, rootID)
		if err != nil {
			return err
		}
		if root.State.Terminal() || root.State == domain.Cancelling {
			return context.Canceled
		}
		if root.State == domain.Reconciling {
			return domain.ErrUnknown
		}
		if root.State == domain.Interrupted {
			return domain.ErrCheckpoint
		}
		job := batch[i]
		current, err := w.store.Run(ctx, job.runID)
		if err != nil {
			return err
		}
		if current.State.Terminal() || current.State == domain.Cancelling {
			return context.Canceled
		}
		if current.State == domain.Reconciling {
			return domain.ErrUnknown
		}
		if current.State == domain.Interrupted {
			return domain.ErrCheckpoint
		}
		index := -1
		for j, node := range current.Nodes {
			if node.ID == job.nodeID && node.InvocationID == job.invocationID {
				index = j
				break
			}
		}
		if index < 0 {
			return errors.Join(domain.ErrCheckpoint, domain.ErrConflict)
		}
		node := &current.Nodes[index]
		switch out.Kind {
		case "completed":
			node.State, node.Result = "completed", out.Result
			artifact := &domain.Artifact{ID: domain.NewID(), RunID: current.ID, InvocationID: node.InvocationID, Kind: node.Role, Content: out.Result}
			if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{current}, Artifacts: []*domain.Artifact{artifact}}); err != nil {
				return err
			}
		case "input":
			if out.Input == nil || out.Input.RunID != current.ID || out.Input.InvocationID != node.InvocationID {
				return fmt.Errorf("invalid input outcome")
			}
			node.State = "waiting"
			current.State = domain.Waiting
			if current.WaitingID == 0 {
				current.WaitingID = out.Input.ID
			}
			if err := w.commit(ctx, domain.Mutation{Runs: []*domain.Run{current}, Events: []domain.Event{{RunID: current.ID, InvocationID: node.InvocationID, Kind: "waiting", Content: fmt.Sprintf("%d: %s", out.Input.ID, out.Input.Prompt)}}}); err != nil {
				return err
			}
		case "delegated":
			if err := w.delegate(ctx, current, index, out); err != nil {
				return err
			}
		case "failed":
			node.State = "failed"
			if out.Err == nil {
				out.Err = fmt.Errorf("agent failed without a reason")
			}
			if err := w.failRun(ctx, current, out.Err); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown agent outcome %q", out.Kind)
		}
	}
	return nil
}

func readyNodes(run *domain.Run) []int {
	for i, n := range run.Nodes {
		if n.State == "completed" {
			continue
		}
		if run.Mode == "deliberate" && strings.HasPrefix(n.Role, "critic-") {
			var result []int
			for j := i; j < len(run.Nodes) && strings.HasPrefix(run.Nodes[j].Role, "critic-"); j++ {
				if run.Nodes[j].State != "completed" {
					result = append(result, j)
				}
			}
			return result
		}
		return []int{i}
	}
	return nil
}

func (w *Runner) freezeInput(ctx context.Context, run *domain.Run) error {
	if run.ParentRunID != 0 || run.SessionID == 0 {
		run.Input = []model.Message{{Role: "user", Content: run.Prompt}}
		run.InputFrozen = true
		return nil
	}
	session, err := w.store.Session(ctx, run.SessionID)
	if err != nil {
		return err
	}
	previous, err := w.store.Runs(ctx)
	if err != nil {
		return err
	}
	accepted := map[domain.TaskID]bool{0: true}
	for _, prior := range previous {
		if prior.SessionID == run.SessionID && prior.ParentRunID == 0 && prior.CreatedAt < run.CreatedAt && prior.TaskID != run.TaskID {
			accepted[prior.TaskID] = true
		}
	}
	var history []model.Message
	for _, message := range session.Messages {
		if accepted[message.TaskID] {
			history = append(history, model.Message{Role: string(message.Role), Content: message.Content})
		}
	}
	for len(history) > 40 || model.Estimate(history) > 12000 {
		if len(history) == 0 {
			break
		}
		history = history[1:]
	}
	for len(history) > 0 && history[0].Role != "user" {
		history = history[1:]
	}
	run.Input = append(history, model.Message{Role: "user", Content: run.Prompt})
	run.InputFrozen = true
	return nil
}

func (w *Runner) finish(ctx context.Context, run *domain.Run) error {
	if len(run.Nodes) == 0 {
		return fmt.Errorf("workflow has no nodes")
	}
	last := &run.Nodes[len(run.Nodes)-1]
	if last.Role == "verifier" {
		v, err := agent.ParseVerification(last.Result)
		if err != nil {
			return w.failRun(ctx, run, err)
		}
		evidence, validated, err := w.validationEvidence(ctx, run)
		if err != nil {
			return err
		}
		if !validEvidence(v, evidence) {
			v.Status, v.Summary = "inconclusive", "Verifier cited missing, ambiguous, or fabricated validation evidence"
			v.Evidence = nil // 不发布伪造的 ID 作为已接受的证据。
		} else if v.Status == "passed" && !validated {
			v.Status, v.Summary = "inconclusive", "Latest validator must record every bash call completed with ok:true and explicit exit_code:0"
		}
		data, _ := json.Marshal(v)
		last.Result = string(data)
		if v.Status == "failed" {
			repaired, err := w.repair(ctx, run)
			if err != nil || repaired {
				return err
			}
		}
		if v.Status != "passed" {
			run.Result = last.Result
			return w.failRun(ctx, run, fmt.Errorf("%s: %s", v.Status, v.Summary))
		}
	}
	children, err := w.store.Runs(ctx)
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.ParentRunID != run.ID {
			continue
		}
		if child.State != domain.Succeeded {
			return w.failRun(ctx, run, fmt.Errorf("child %d did not succeed: %s: %s", child.ID, child.State, child.Error))
		}
	}
	run.State, run.Result = domain.Succeeded, last.Result
	mutation := domain.Mutation{Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: run.ID, Kind: "completed", Content: run.Result}}}
	if run.ParentRunID == 0 && run.SessionID != 0 {
		session, err := w.store.Session(ctx, run.SessionID)
		if err != nil {
			return err
		}
		session.AppendAssistant(run.TaskID, run.Result)
		mutation.Sessions = []*domain.Session{session}
	}
	return w.commit(ctx, mutation)
}

func (w *Runner) repair(ctx context.Context, run *domain.Run) (bool, error) {
	// 委派代码保持为快速路径；不扩展其角色图。
	if run.Mode != "agent" && run.Mode != "test" {
		return false, nil
	}
	root := run
	if run.RootRunID != run.ID {
		var err error
		root, err = w.store.Run(ctx, run.RootRunID)
		if err != nil {
			return false, err
		}
	}
	if root.State.Terminal() || root.State == domain.Cancelling {
		return false, context.Canceled
	}
	if root.Repairs >= root.Limits.MaxRepairs {
		return false, nil
	}
	// Root.Repairs 是树级计数。节点图中的 Attempt 跟踪根本地轮次，
	// 而子级的 Repairs 跟踪该子级本地的轮次。
	attempt := 1
	for _, node := range run.Nodes {
		if node.Role == "repair" {
			attempt++
		}
	}
	root.Repairs++
	mutation := domain.Mutation{Runs: []*domain.Run{run}}
	if root != run {
		run.Repairs++
		mutation.Runs = append(mutation.Runs, root)
	}
	for _, role := range []string{"repair", "validator", "verifier"} {
		run.Nodes = append(run.Nodes, domain.Node{ID: fmt.Sprintf("repair-%d-%s", attempt, role), Role: role, State: "pending", Attempt: attempt + 1})
	}
	return true, w.commit(ctx, mutation)
}

func (w *Runner) delegate(ctx context.Context, parent *domain.Run, index int, out agent.Outcome) error {
	if _, err := w.store.Delegation(ctx, out.Key); err == nil {
		parent.State, parent.WaitingID = domain.Waiting, 0
		parent.Nodes[index].State = "children"
		return w.commit(ctx, domain.Mutation{Runs: []*domain.Run{parent}})
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	root := parent
	if parent.RootRunID != parent.ID {
		var err error
		root, err = w.store.Run(ctx, parent.RootRunID)
		if err != nil {
			return err
		}
	}
	if root.State.Terminal() || root.State == domain.Cancelling {
		return context.Canceled
	}
	if len(out.Goals) == 0 || parent.Depth >= root.Limits.MaxDepth || root.Budget.Children+len(out.Goals) > root.Limits.MaxChildren {
		return w.failRun(ctx, parent, domain.ErrBudget)
	}
	link := &domain.Delegation{Key: out.Key, ParentRunID: parent.ID, InvocationID: out.InvocationID}
	mutation := domain.Mutation{Delegations: []*domain.Delegation{link}}
	template, _ := Mode("code")
	for _, goal := range out.Goals {
		task := domain.NewTask(goal.Description, goal.Title, parent.SessionID)
		task.ParentTaskID = parent.TaskID
		child := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: task.ID, TaskVersion: task.Version, SessionID: parent.SessionID, RootRunID: parent.RootRunID, ParentRunID: parent.ID, Depth: parent.Depth + 1, Workspace: parent.Workspace, Mode: "code", TemplateVersion: 1, Model: parent.Model, Policy: parent.Policy.Intersect(template.Policy), Limits: root.Limits, State: domain.Queued, Nodes: template.Nodes(), Prompt: goal.Description, SystemPrompt: parent.SystemPrompt, CreatedAt: time.Now().UnixNano()}
		link.Children = append(link.Children, child.ID)
		mutation.Tasks = append(mutation.Tasks, task)
		mutation.Runs = append(mutation.Runs, child)
	}
	root.Budget.Children += len(out.Goals)
	parent.State, parent.WaitingID = domain.Waiting, 0
	parent.Nodes[index].State = "children"
	mutation.Runs = append(mutation.Runs, parent)
	if root != parent {
		mutation.Runs = append(mutation.Runs, root)
	}
	return w.commit(ctx, mutation)
}

func pendingKey(inv *domain.Invocation) (string, error) {
	if inv.NextCall < 0 || inv.NextCall >= len(inv.Pending) {
		return "", fmt.Errorf("invalid pending invocation checkpoint")
	}
	return fmt.Sprintf("%d:%d:%s", inv.ID, inv.AssistantSequence, inv.Pending[inv.NextCall].ID), nil
}

func (w *Runner) invocationInput(ctx context.Context, inv *domain.Invocation) (*domain.InputRequest, error) {
	key, err := pendingKey(inv)
	if err != nil {
		return nil, err
	}
	return w.store.InputByCall(ctx, key)
}

// PendingInputs 包含树中所有调用的待处理输入，而不仅是 Run.WaitingID。
// 它还会找到在工作流等待检查点失败之前已持久化的输入。
func (w *Runner) PendingInputs(ctx context.Context, id domain.RunID) ([]domain.InputRequest, error) {
	root, err := w.store.Run(ctx, id)
	if err != nil {
		return nil, err
	}
	runs, err := w.store.Runs(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.InputRequest
	seen := map[domain.InputRequestID]bool{}
	for _, run := range runs {
		if run.RootRunID != root.RootRunID || run.State.Terminal() {
			continue
		}
		for _, node := range run.Nodes {
			if node.InvocationID == 0 {
				continue
			}
			inv, err := w.store.Invocation(ctx, node.InvocationID)
			if err != nil {
				return nil, err
			}
			if inv.Phase != "waiting" {
				continue
			}
			input, err := w.invocationInput(ctx, inv)
			if err != nil {
				return nil, err
			}
			if input.State == "pending" && !seen[input.ID] {
				result = append(result, *input)
				seen[input.ID] = true
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (w *Runner) delegationResult(ctx context.Context, id domain.InvocationID) (string, error) {
	inv, err := w.store.Invocation(ctx, id)
	if err != nil {
		return "", err
	}
	if inv.Phase != "delegated" {
		return "", nil
	}
	key, err := pendingKey(inv)
	if err != nil {
		return "", err
	}
	link, err := w.store.Delegation(ctx, key)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var results []map[string]any
	for _, id := range link.Children {
		run, err := w.store.Run(ctx, id)
		if err != nil {
			return "", err
		}
		if run.State == domain.Reconciling {
			return "", domain.ErrUnknown
		}
		if run.State == domain.Interrupted {
			return "", domain.ErrCheckpoint
		}
		if run.State == domain.Cancelling || run.State == domain.Cancelled {
			return "", context.Canceled
		}
		if !run.State.Terminal() {
			return "", nil
		}
		results = append(results, map[string]any{"run_id": run.ID, "state": run.State, "result": run.Result, "error": run.Error})
	}
	data, err := json.Marshal(results)
	return string(data), err
}

func (w *Runner) failRun(ctx context.Context, run *domain.Run, reason error) error {
	run.State, run.Error = domain.Failed, reason.Error()
	return w.commit(ctx, domain.Mutation{Runs: []*domain.Run{run}, Events: []domain.Event{{RunID: run.ID, Kind: "failed", Content: run.Error}}})
}

func (w *Runner) interrupt(ctx context.Context, rootID domain.RunID, reason error) (*domain.Run, error) {
	unknown := errors.Is(reason, domain.ErrUnknown) || errors.Is(reason, model.ErrUnknown)
	cancelled := false
	// 仅重试恢复状态的更新。重新加载所有 CAS 参与者；绝不
	// 重放执行或将过期的成功批次合并到取消操作上。
	for tries := 0; tries < 8; tries++ {
		runs, err := w.store.Runs(ctx)
		if err != nil {
			return nil, errors.Join(reason, err)
		}
		for _, run := range runs {
			if run.RootRunID != rootID {
				continue
			}
			unknown = unknown || run.State == domain.Reconciling
			cancelled = cancelled || run.State == domain.Cancelling || run.State == domain.Cancelled
			for _, node := range run.Nodes {
				if node.InvocationID == 0 {
					continue
				}
				inv, err := w.store.Invocation(ctx, node.InvocationID)
				if err != nil {
					return nil, errors.Join(reason, err)
				}
				if inv.Phase == "model_started" {
					unknown = true
				}
				for _, call := range invocationCalls(inv) {
					tool, err := w.store.Tool(ctx, call.key)
					if errors.Is(err, domain.ErrNotFound) {
						continue
					}
					if err != nil {
						return nil, errors.Join(reason, err)
					}
					if tool.State == "started" || tool.State == "unknown" {
						unknown = true
					}
				}
			}
		}
		mutation := domain.Mutation{}
		intermediate := false
		for i := range runs {
			run := &runs[i]
			if run.RootRunID != rootID || run.State.Terminal() {
				continue
			}
			target := domain.Interrupted
			switch {
			case unknown && run.State == domain.Queued:
				intermediate = true // 排队中 -> 已中断 -> 对账中
			case unknown:
				target = domain.Reconciling
			case cancelled && run.State != domain.Cancelling:
				target, intermediate = domain.Cancelling, true
			case cancelled:
				target = domain.Cancelled
			}
			if run.State == target && run.Error == reason.Error() {
				continue
			}
			run.State, run.Error = target, reason.Error()
			mutation.Runs = append(mutation.Runs, run)
			mutation.Events = append(mutation.Events, domain.Event{RunID: run.ID, Kind: "state", Content: string(run.State)})
		}
		if len(mutation.Runs) > 0 {
			if err := w.commit(ctx, mutation); err != nil {
				if errors.Is(err, domain.ErrConflict) {
					continue
				}
				return nil, errors.Join(reason, err)
			}
		}
		if intermediate {
			continue
		}
		return w.store.Run(ctx, rootID)
	}
	return nil, errors.Join(reason, domain.ErrCheckpoint, domain.ErrConflict)
}
