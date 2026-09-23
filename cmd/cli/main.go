package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/bootstrap"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

type service interface {
	Submit(context.Context, app.SubmitRequest) (*domain.Run, error)
	ContinueSession(context.Context, domain.SessionID, app.SubmitRequest) (*domain.Run, error)
	Run(context.Context, domain.RunID) (*domain.Run, error)
	Runs(context.Context) ([]domain.Run, error)
	Session(context.Context, domain.SessionID) (*domain.Session, error)
	Sessions(context.Context) ([]domain.Session, error)
	Wait(context.Context, domain.RunID) (*domain.Run, error)
	Observe(context.Context, domain.RunID, int64) ([]domain.Event, error)
	Subscribe(domain.RunID) (<-chan domain.Event, func())
	PendingInputs(context.Context, domain.RunID) ([]domain.InputRequest, error)
	RespondToInput(context.Context, domain.InputRequestID, string, bool) (*domain.Run, error)
	ResumeRun(context.Context, domain.RunID) (*domain.Run, error)
	RetryTask(context.Context, domain.TaskID) (*domain.Run, error)
	Cancel(context.Context, domain.RunID) error
	DeleteSession(context.Context, domain.SessionID) error
	DeleteAllSessions(context.Context) error
	UpdateTask(context.Context, domain.TaskID, string, string) (*domain.Task, error)
	ReconcileRun(context.Context, domain.RunID, string) (*domain.Run, error)
}

var _ service = (*app.Service)(nil)

type importSession func(context.Context, string, string) (domain.SessionID, error)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "orca: %q\n", err.Error())
		os.Exit(1)
	}
}

func run() (err error) {
	a, err := ParseArgs()
	if errors.Is(err, ErrHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.SubCommand != "" && commandHelp(a.SubArgs) {
		PrintHelp()
		return nil
	}
	// Validate files before opening the environment; parse errors never start work.
	var req app.SubmitRequest
	if a.SubCommand == "" {
		req, err = prepareRequest(a)
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env, err := bootstrap.Open("")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, env.Close()) }()
	return execute(ctx, env.Service, env.Import, a, req, os.Stdout)
}

func prepareRequest(a *CliArgs) (app.SubmitRequest, error) {
	if err := validateSubmitArgs(a); err != nil {
		return app.SubmitRequest{}, err
	}
	prompt, err := userPrompt(a)
	if err != nil {
		return app.SubmitRequest{}, err
	}
	req := app.SubmitRequest{SessionID: domain.SessionID(a.SessionId), Input: a.Prompt, Prompt: prompt, Mode: a.Mode, Model: model.Ref{Provider: a.Provider, Model: a.Model}}
	if a.SystemPrompt != "" {
		data, err := os.ReadFile(a.SystemPrompt)
		if err != nil {
			return app.SubmitRequest{}, fmt.Errorf("read system prompt %s: %w", a.SystemPrompt, err)
		}
		req.SystemPrompt = string(data)
	}
	return req, nil
}

func userPrompt(a *CliArgs) (string, error) {
	var b strings.Builder
	for _, file := range a.Files {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read attached file %s: %w", file, err)
		}
		fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n\n", file, strings.TrimRight(string(data), "\n"))
	}
	b.WriteString(a.Prompt)
	return b.String(), nil
}

func execute(ctx context.Context, s service, importer importSession, a *CliArgs, req app.SubmitRequest, out io.Writer) error {
	if a == nil {
		return fmt.Errorf("missing arguments")
	}
	if a.SubCommand != "" {
		if err := validateCommand(a); err != nil {
			return err
		}
		if commandHelp(a.SubArgs) {
			_, err := fmt.Fprint(out, helpText)
			return err
		}
	} else if err := validateSubmitArgs(a); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.SubCommand == "session" {
		return handleSession(ctx, s, importer, a.SubArgs, out)
	}
	if a.SubCommand == "task" {
		id, _ := ParseSessionId(a.SubArgs[1])
		input := strings.Join(a.SubArgs[2:], " ")
		title := []rune(input)
		if len(title) > 80 {
			title = title[:80]
		}
		task, err := s.UpdateTask(ctx, domain.TaskID(id), input, string(title))
		if err != nil {
			return err
		}
		return writeJSON(out, task)
	}
	// Once submitted, cancellation is an explicit, durable Cancel operation, not
	// cancellation of the persistence context halfway through a transaction.
	opCtx := context.WithoutCancel(ctx)
	var run *domain.Run
	var err error
	if a.SubCommand == "run" {
		args := a.SubArgs
		if commandHelp(args) {
			_, err = fmt.Fprint(out, helpText)
			return err
		}
		if args[0] == "list" || args[0] == "ls" {
			runs, err := s.Runs(ctx)
			if err != nil {
				return err
			}
			return writeJSON(out, runs)
		}
		id, err := ParseSessionId(args[1])
		if err != nil {
			return err
		}
		switch args[0] {
		case "show":
			return showRun(ctx, s, domain.RunID(id), a.After, out)
		case "reconcile":
			reconciled, err := s.ReconcileRun(opCtx, domain.RunID(id), args[3])
			if err != nil {
				return err
			}
			if err := writeJSON(out, reconciled); err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, "Manual verification recorded; run ended as failed. No operation was replayed.")
			return err
		case "resume":
			run, err = s.ResumeRun(opCtx, domain.RunID(id))
		case "retry":
			run, err = s.RetryTask(opCtx, domain.TaskID(id))
		case "respond":
			run, err = s.RespondToInput(opCtx, domain.InputRequestID(id), strings.Join(args[2:], " "), false)
		case "approve", "reject":
			run, err = s.RespondToInput(opCtx, domain.InputRequestID(id), "", args[0] == "approve")
		case "cancel":
			err = s.Cancel(opCtx, domain.RunID(id))
			if err == nil {
				run, err = s.Run(opCtx, domain.RunID(id))
			}
		default:
			return fmt.Errorf("unknown run subcommand %q", args[0])
		}
		if err != nil {
			return err
		}
	} else {
		if req.SessionID != 0 {
			run, err = s.ContinueSession(opCtx, req.SessionID, req)
		} else {
			run, err = s.Submit(opCtx, req)
		}
		if err != nil {
			return err
		}
	}
	if run == nil {
		return fmt.Errorf("service returned no run")
	}
	fmt.Fprintf(out, "session=%d task=%d run=%d state=%s\n", run.SessionID, run.TaskID, run.ID, run.State)
	return followRun(ctx, s, run, out)
}

type waitResult struct {
	run *domain.Run
	err error
}

// Observation owns no execution: disconnecting it never calls Cancel. Only a
// user signal does. Environment.Close subsequently joins the service worker.
func followRun(ctx context.Context, s service, run *domain.Run, out io.Writer) error {
	waitCtx, stopWait := context.WithCancel(context.WithoutCancel(ctx))
	finished := make(chan waitResult, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		r, err := waitForOutcome(waitCtx, s, run.ID)
		finished <- waitResult{r, err}
	}()
	defer func() { stopWait(); <-joined }()
	deltas, unsubscribe := s.Subscribe(0)
	defer func() { unsubscribe() }()
	display := newStreamDisplay(out)
	defer display.clear()
	cursor := map[domain.RunID]int64{}
	known := map[domain.RunID]bool{run.ID: true}
	root := run.RootRunID
	if root == 0 {
		root = run.ID
	}
	drain := func() error {
		events, ids, err := readEvents(waitCtx, s, root, 0, cursor)
		if err != nil {
			return err
		}
		known = ids
		return display.events(events)
	}
	if err := drain(); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := s.Cancel(context.WithoutCancel(ctx), run.ID); err != nil {
				return fmt.Errorf("cancel run %d: %w", run.ID, err)
			}
			// Wait again after Cancel, since an earlier Wait may have returned waiting.
			stopped, err := s.Wait(context.WithoutCancel(ctx), run.ID)
			if err != nil {
				return err
			}
			if err := drain(); err != nil {
				return err
			}
			if err := display.clear(); err != nil {
				return err
			}
			return printOutcome(waitCtx, s, stopped, out)
		case result := <-finished:
			if ctx.Err() != nil { // Prefer durable cancellation over a racing Wait result.
				if err := s.Cancel(context.WithoutCancel(ctx), run.ID); err != nil {
					return err
				}
				result.run, result.err = s.Wait(context.WithoutCancel(ctx), run.ID)
			}
			if result.err != nil {
				return result.err
			}
			if err := drain(); err != nil {
				return err
			}
			if err := display.clear(); err != nil {
				return err
			}
			return printOutcome(waitCtx, s, result.run, out)
		case event, ok := <-deltas:
			if !ok {
				unsubscribe()
				deltas = nil
				continue
			}
			if event.Kind == "delta" && known[event.RunID] {
				if err := display.delta(event); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if deltas == nil {
				deltas, unsubscribe = s.Subscribe(0)
			}
			if err := drain(); err != nil {
				return err
			}
		}
	}
}

func waitForOutcome(ctx context.Context, s service, id domain.RunID) (*domain.Run, error) {
	for {
		r, err := s.Wait(ctx, id)
		if err != nil || r == nil || r.State != domain.Waiting {
			return r, err
		}
		inputs, err := s.PendingInputs(ctx, id)
		if err != nil || len(inputs) > 0 {
			return r, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Each run has its own after cursor; sorting the combined batches preserves the
// store's durable sequence across parent, child and critic invocations.
func readEvents(ctx context.Context, s service, root domain.RunID, after int64, cursors map[domain.RunID]int64) ([]domain.Event, map[domain.RunID]bool, error) {
	runs, err := s.Runs(ctx)
	if err != nil {
		return nil, nil, err
	}
	ids := map[domain.RunID]bool{root: true}
	for _, r := range runs {
		if r.RootRunID == root {
			ids[r.ID] = true
		}
	}
	var events []domain.Event
	next := make(map[domain.RunID]int64, len(ids))
	for id := range ids {
		cursor, exists := cursors[id]
		if !exists {
			cursor = after
		}
		for {
			batch, err := s.Observe(ctx, id, cursor)
			if err != nil {
				return nil, nil, fmt.Errorf("observe run %d after %d: %w", id, cursor, err)
			}
			if len(batch) == 0 {
				break
			}
			sort.SliceStable(batch, func(i, j int) bool { return batch[i].Sequence < batch[j].Sequence })
			previous := cursor
			for _, e := range batch {
				if e.RunID == id && e.Sequence > cursor {
					events = append(events, e)
					cursor = e.Sequence
				}
			}
			if cursor == previous {
				return nil, nil, fmt.Errorf("observe run %d returned no advancing sequence", id)
			}
		}
		next[id] = cursor
	}
	for id, cursor := range next {
		cursors[id] = cursor
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	return events, ids, nil
}

func printEvents(out io.Writer, events []domain.Event) error {
	return printDurableEvents(out, events, map[domain.RunID][32]byte{})
}

func printOutcome(ctx context.Context, s service, run *domain.Run, out io.Writer) error {
	if run == nil {
		return fmt.Errorf("service returned no final run")
	}
	if _, err := fmt.Fprintf(out, "--- run=%d session=%d task=%d state=%s waiting=%d ---\n", run.ID, run.SessionID, run.TaskID, run.State, run.WaitingID); err != nil {
		return err
	}
	if err := printPending(ctx, s, run.ID, out); err != nil {
		return err
	}
	switch run.State {
	case domain.Failed:
		return fmt.Errorf("run %d failed: %s", run.ID, run.Error)
	case domain.Interrupted:
		_, err := fmt.Fprintf(out, "Saved; resume with: orca run resume %d\n", run.ID)
		return err
	case domain.Reconciling:
		_, err := fmt.Fprintln(out, "Saved; reconciliation required. Do not retry unknown side effects.")
		return err
	case domain.Waiting:
		_, err := fmt.Fprintln(out, "Saved; waiting for input/approval (not a failure).")
		return err
	}
	return nil
}

func showRun(ctx context.Context, s service, id domain.RunID, after int64, out io.Writer) error {
	r, err := s.Run(ctx, id)
	if err != nil {
		return err
	}
	if err := writeJSON(out, r); err != nil {
		return err
	}
	events, _, err := readEvents(ctx, s, id, after, map[domain.RunID]int64{})
	if err != nil {
		return err
	}
	if err := printEvents(out, events); err != nil {
		return err
	}
	return printPending(ctx, s, id, out)
}

func printPending(ctx context.Context, s service, id domain.RunID, out io.Writer) error {
	inputs, err := s.PendingInputs(ctx, id)
	if err != nil {
		return err
	}
	for _, input := range inputs {
		// Prompt includes the gateway's exact workspace/tool/call/arguments binding.
		if err := writeJSON(out, input); err != nil {
			return err
		}
		if input.Kind == "approval" {
			_, err = fmt.Fprintf(out, "orca run approve %d\norca run reject %d\n", input.ID, input.ID)
		} else {
			_, err = fmt.Fprintf(out, "orca run respond %d TEXT\n", input.ID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
