package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/tools"
)

type ExecutionStore interface {
	Apply(context.Context, domain.Mutation) error
	Run(context.Context, domain.RunID) (*domain.Run, error)
	Invocation(context.Context, domain.InvocationID) (*domain.Invocation, error)
}

type ToolGateway interface {
	Definitions(domain.Policy) []model.Tool
	Execute(context.Context, *domain.Run, *domain.Invocation, model.Call, string) (tools.Result, error)
}

type Outcome struct {
	Kind         string
	InvocationID domain.InvocationID
	Result       string
	Err          error
	Key          string
	Goals        []tools.Goal
	Input        *domain.InputRequest
}

type Runner struct {
	store     ExecutionStore
	client    model.Client
	tools     ToolGateway
	limitMu   sync.Mutex
	limit     int
	inFlight  int
	providers map[string]int
	available chan struct{}
	budgetMu  sync.Mutex
	active    sync.Map
	Sink      func(domain.Event)
}

func NewRunner(store ExecutionStore, client model.Client, gateway ToolGateway, concurrency int) *Runner {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Runner{store: store, client: client, tools: gateway, limit: concurrency, providers: map[string]int{}, available: make(chan struct{})}
}

func (r *Runner) Advance(ctx context.Context, id domain.InvocationID, resumeInput string) Outcome {
	out := Outcome{InvocationID: id}
	if _, busy := r.active.LoadOrStore(id, struct{}{}); busy {
		out.Kind = "failed"
		out.Err = domain.ErrConflict
		return out
	}
	defer r.active.Delete(id)
	inv, err := r.store.Invocation(ctx, id)
	if err != nil {
		out.Kind = "failed"
		out.Err = errors.Join(domain.ErrCheckpoint, err)
		return out
	}
	run, err := r.store.Run(ctx, inv.RunID)
	if err != nil {
		out.Kind = "failed"
		out.Err = errors.Join(domain.ErrCheckpoint, err)
		return out
	}
	profile, err := Role(inv.Role)
	if err != nil || inv.RoleVersion != profile.Version {
		if err == nil {
			err = fmt.Errorf("unsupported role version %d", inv.RoleVersion)
		}
		out.Kind = "failed"
		out.Err = err
		return out
	}
	inv.Policy = inv.Policy.Intersect(run.Policy).Intersect(profile.Capabilities)
	if run.Limits.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Unix(run.Limits.Deadline, 0))
		defer cancel()
	}
	for {
		if err := ctx.Err(); err != nil {
			out.Kind = "failed"
			out.Err = err
			return out
		}
		run, err = r.store.Run(ctx, inv.RunID)
		if err != nil {
			out.Kind, out.Err = "failed", errors.Join(domain.ErrCheckpoint, err)
			return out
		}
		if run.State == domain.Cancelling || run.State.Terminal() || run.State == domain.Reconciling {
			out.Kind, out.Err = "failed", context.Canceled
			return out
		}
		inv.Policy = inv.Policy.Intersect(run.Policy).Intersect(profile.Capabilities)
		switch inv.Phase {
		case "completed":
			out.Kind = "completed"
			out.Result = inv.Result
			return out
		case "failed":
			out.Kind = "failed"
			out.Err = errors.New(inv.Error)
			return out
		case "model_started":
			out.Kind = "failed"
			out.Err = domain.ErrUnknown
			return out
		case "tools", "waiting", "delegated":
			if inv.NextCall >= len(inv.Pending) {
				inv.Phase = "model"
				inv.Pending = nil
				inv.NextCall = 0
				if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				continue
			}
			call := inv.Pending[inv.NextCall]
			key := fmt.Sprintf("%d:%d:%s", id, inv.AssistantSequence, call.ID)
			if inv.Phase == "delegated" && resumeInput != "" {
				inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: resumeInput})
				inv.NextCall++
				inv.Phase = "tools"
				resumeInput = ""
				if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				continue
			}
			result, err := r.tools.Execute(ctx, run, inv, call, key)
			if err != nil {
				out.Kind = "failed"
				out.Err = err
				if !errors.Is(err, domain.ErrUnknown) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					out.Err = errors.Join(domain.ErrCheckpoint, err)
				}
				return out
			}
			if result.Waiting != nil {
				inv.Phase = "waiting"
				mutation := domain.Mutation{Invocations: []*domain.Invocation{inv}}
				if result.Waiting.Version == 0 {
					mutation.Inputs = []*domain.InputRequest{result.Waiting}
				}
				if err := r.commit(ctx, mutation); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				out.Kind = "input"
				out.Input = result.Waiting
				return out
			}
			if len(result.Goals) > 0 {
				inv.Phase = "delegated"
				if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
					out.Kind = "failed"
					out.Err = err
					return out
				}
				out.Kind = "delegated"
				out.Key = key
				out.Goals = result.Goals
				return out
			}
			inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: result.Content})
			inv.NextCall++
			inv.Phase = "tools"
			if err := r.commit(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}, Events: []domain.Event{{RunID: run.ID, InvocationID: id, Kind: "tool", Content: result.Content}}}); err != nil {
				out.Kind = "failed"
				out.Err = err
				return out
			}
		case "model":
			if err := r.acquire(ctx, inv.Model.Provider); err != nil {
				out.Kind, out.Err = "failed", err
				return out
			}
			err := r.reserve(ctx, run.RootRunID, inv)
			if err != nil {
				r.release(inv.Model.Provider)
				out.Kind = "failed"
				out.Err = err
				return out
			}
			assistantSequence := inv.Thread.SequenceOffset + len(inv.Thread.Messages) + 1
			response, callErr := r.client.Complete(ctx, model.Request{
				Model: inv.Model, ThreadID: int64(inv.ID), ContextVersion: inv.Thread.ContextVersion,
				Messages: inv.Thread.Messages, Cursor: inv.Thread.Cursor, Tools: r.tools.Definitions(inv.Policy), MaxOutputTokens: 4096,
			}, func(delta string) {
				if r.Sink != nil {
					r.Sink(domain.Event{RunID: run.ID, InvocationID: id, AssistantSequence: assistantSequence, Kind: "delta", Content: delta})
				}
			})
			r.release(inv.Model.Provider)
			if callErr != nil || !response.Complete {
				if callErr == nil {
					callErr = model.ErrUnknown
				}
				if errors.Is(callErr, model.ErrUnknown) || ctx.Err() != nil {
					out.Kind = "failed"
					out.Err = errors.Join(domain.ErrUnknown, callErr)
					return out
				}
				inv.Phase = "failed"
				inv.Error = callErr.Error()
				if err := r.settle(ctx, run.RootRunID, inv, model.Usage{}, nil); err != nil {
					callErr = errors.Join(callErr, err)
				}
				out.Kind = "failed"
				out.Err = callErr
				return out
			}
			message := response.Message
			message.Role = "assistant"
			ids := map[string]bool{}
			for i := range message.ToolCalls {
				if message.ToolCalls[i].ID == "" {
					message.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", len(inv.Thread.Messages), i)
				}
				if ids[message.ToolCalls[i].ID] {
					out.Kind = "failed"
					out.Err = errors.Join(domain.ErrUnknown, fmt.Errorf("duplicate tool call identity"))
					return out
				}
				ids[message.ToolCalls[i].ID] = true
			}
			inv.Thread.Messages = append(inv.Thread.Messages, message)
			inv.Thread.Cursor = response.Cursor
			inv.AssistantSequence = inv.Thread.SequenceOffset + len(inv.Thread.Messages)
			inv.Pending = message.ToolCalls
			inv.NextCall = 0
			if len(message.ToolCalls) == 0 {
				inv.Result = message.Content
				if err := ValidateResult(profile, message.Content); err != nil {
					inv.Phase = "failed"
					inv.Error = err.Error()
				} else {
					inv.Phase = "completed"
				}
			} else {
				inv.Phase = "tools"
			}
			usage := response.Usage
			if usage.TotalTokens <= 0 {
				usage.TotalTokens, err = r.estimateTokens(inv)
				if err != nil {
					out.Kind, out.Err = "failed", errors.Join(domain.ErrCheckpoint, err)
					return out
				}
				usage.Estimated = true
			}
			inv.Usage.PromptTokens += usage.PromptTokens
			inv.Usage.CompletionTokens += usage.CompletionTokens
			inv.Usage.TotalTokens += usage.TotalTokens
			inv.Usage.Estimated = inv.Usage.Estimated || usage.Estimated
			if err := r.settle(ctx, run.RootRunID, inv, usage, []domain.Event{{RunID: run.ID, InvocationID: id, AssistantSequence: inv.AssistantSequence, Kind: "message", Content: message.Content}}); err != nil {
				out.Kind = "failed"
				out.Err = err
				return out
			}
		default:
			out.Kind = "failed"
			out.Err = fmt.Errorf("invalid invocation checkpoint phase %q", inv.Phase)
			return out
		}
	}
}

func (r *Runner) acquire(ctx context.Context, provider string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.limitMu.Lock()
		if r.inFlight < r.limit && r.providers[provider] < min(2, r.limit) {
			r.inFlight++
			r.providers[provider]++
			r.limitMu.Unlock()
			return nil
		}
		available := r.available
		r.limitMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-available:
		}
	}
}

func (r *Runner) release(provider string) {
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	r.inFlight--
	r.providers[provider]--
	close(r.available)
	r.available = make(chan struct{})
}

func (r *Runner) estimateTokens(inv *domain.Invocation) (int64, error) {
	prompt, err := model.ToolPrompt(r.tools.Definitions(inv.Policy))
	if err != nil {
		return 0, err
	}
	return model.Estimate(inv.Thread.Messages) + model.Estimate([]model.Message{{Role: "system", Content: prompt}}), nil
}

func (r *Runner) reserve(ctx context.Context, rootID domain.RunID, inv *domain.Invocation) error {
	r.budgetMu.Lock()
	defer r.budgetMu.Unlock()
	for tries := 0; tries < 8; tries++ {
		root, err := r.store.Run(ctx, rootID)
		if err != nil {
			return errors.Join(domain.ErrCheckpoint, err)
		}
		if root.State.Terminal() || root.State == domain.Cancelling {
			return context.Canceled
		}
		estimate, err := r.estimateTokens(inv)
		if err != nil {
			return err
		}
		reserve := estimate + 4096
		if root.Budget.Turns >= root.Limits.MaxTurns || root.Budget.Tokens+root.Budget.Reserved+reserve > root.Limits.MaxTokens {
			return domain.ErrBudget
		}
		root.Budget.Turns++
		root.Budget.Reserved += reserve
		inv.Phase = "model_started"
		inv.Reservation = reserve
		err = r.commit(ctx, domain.Mutation{Runs: []*domain.Run{root}, Invocations: []*domain.Invocation{inv}})
		if !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return errors.Join(domain.ErrCheckpoint, domain.ErrConflict)
}

func (r *Runner) settle(ctx context.Context, rootID domain.RunID, inv *domain.Invocation, usage model.Usage, events []domain.Event) error {
	r.budgetMu.Lock()
	defer r.budgetMu.Unlock()
	for tries := 0; tries < 8; tries++ {
		root, err := r.store.Run(ctx, rootID)
		if err != nil {
			return errors.Join(domain.ErrCheckpoint, err)
		}
		root.Budget.Reserved -= inv.Reservation
		root.Budget.Tokens += usage.TotalTokens
		reservation := inv.Reservation
		inv.Reservation = 0
		err = r.commit(ctx, domain.Mutation{Runs: []*domain.Run{root}, Invocations: []*domain.Invocation{inv}, Events: events})
		if !errors.Is(err, domain.ErrConflict) {
			return err
		}
		inv.Reservation = reservation
	}
	return errors.Join(domain.ErrCheckpoint, domain.ErrConflict)
}

func (r *Runner) commit(ctx context.Context, mutation domain.Mutation) error {
	if err := r.store.Apply(ctx, mutation); err != nil {
		return errors.Join(domain.ErrCheckpoint, err)
	}
	return nil
}

func Rebase(inv *domain.Invocation, messages []model.Message) error {
	if inv.Phase != "model" || len(inv.Pending) > 0 {
		return fmt.Errorf("cannot replace context with pending tool results")
	}
	inv.Thread.ContextVersion++
	inv.Thread.SequenceOffset += len(inv.Thread.Messages)
	inv.Thread.Messages = append([]model.Message(nil), messages...)
	inv.Thread.Cursor = nil
	return nil
}
