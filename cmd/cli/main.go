package main

import (
	"bufio"
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

// service 是 CLI 需要的最小应用服务接口。
type service interface {
	Submit(context.Context, app.SubmitRequest) (*domain.Run, error)
	Runs(context.Context) ([]domain.Run, error)
	Wait(context.Context, domain.RunID) (*domain.Run, error)
	Observe(context.Context, domain.RunID, int64) ([]domain.Event, error)
	Subscribe(domain.RunID) (<-chan domain.Event, func())
	PendingInputs(context.Context, domain.RunID) ([]domain.InputRequest, error)
	RespondToInput(context.Context, domain.InputRequestID, string, bool) (*domain.Run, error)
	Cancel(context.Context, domain.RunID) error
}

var _ service = (*app.Service)(nil)

// main 是 CLI 程序入口，运行失败时打印错误并以非零状态退出。
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "orca: %q\n", err.Error())
		os.Exit(1)
	}
}

// run 解析参数、打开环境、提交任务并跟踪到结束。
func run() (err error) {
	a, err := ParseArgs()
	if errors.Is(err, ErrHelpRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	req, err := prepareRequest(a)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env, err := bootstrap.Open("")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, env.Close()) }()
	// 提交本身不受信号取消影响，避免事务中途被打断
	opCtx := context.WithoutCancel(ctx)
	run, err := env.Service.Submit(opCtx, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "session=%d task=%d run=%d\n", run.SessionID, run.TaskID, run.ID)
	return followRun(ctx, env.Service, run, os.Stdout, bufio.NewReader(os.Stdin))
}

// prepareRequest 根据 CLI 参数构造提交请求，包括读取附加文件和系统提示词。
func prepareRequest(a *CliArgs) (app.SubmitRequest, error) {
	if err := validateSubmitArgs(a); err != nil {
		return app.SubmitRequest{}, err
	}
	prompt, err := userPrompt(a)
	if err != nil {
		return app.SubmitRequest{}, err
	}
	req := app.SubmitRequest{Input: a.Prompt, Prompt: prompt, Mode: a.Mode, Model: model.Ref{Provider: a.Provider, Model: a.Model}}
	if a.SystemPrompt != "" {
		data, err := os.ReadFile(a.SystemPrompt)
		if err != nil {
			return app.SubmitRequest{}, fmt.Errorf("read system prompt %s: %w", a.SystemPrompt, err)
		}
		req.SystemPrompt = string(data)
	}
	return req, nil
}

// userPrompt 将附加文件内容包裹在 XML 标签中，并与用户提示词拼接。
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

// waitResult 封装 Wait 协程的返回结果。
type waitResult struct {
	run *domain.Run
	err error
}

// followRun 输出运行过程，直到运行结束或用户中断。
// 需要回答或审批时在这里读标准输入，整轮执行始终在同一个进程内完成。
func followRun(ctx context.Context, s service, run *domain.Run, out io.Writer, in *bufio.Reader) error {
	// Wait 用独立于信号的上下文，取消后仍需要它把运行收敛到终态
	waitCtx := context.WithoutCancel(ctx)
	startWait := func() <-chan waitResult {
		finished := make(chan waitResult, 1)
		go func() {
			r, err := s.Wait(waitCtx, run.ID)
			finished <- waitResult{r, err}
		}()
		return finished
	}
	finished := startWait()

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
		events, ids, err := readEvents(waitCtx, s, root, cursor)
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
			if err := s.Cancel(waitCtx, run.ID); err != nil {
				return fmt.Errorf("cancel run %d: %w", run.ID, err)
			}
			result := <-startWait()
			if result.err != nil {
				return result.err
			}
			if err := drain(); err != nil {
				return err
			}
			if err := display.clear(); err != nil {
				return err
			}
			return printOutcome(result.run, out)
		case result := <-finished:
			if result.err != nil {
				return result.err
			}
			if err := drain(); err != nil {
				return err
			}
			if err := display.clear(); err != nil {
				return err
			}
			if result.run.State == domain.Waiting {
				pending, err := s.PendingInputs(waitCtx, run.ID)
				if err != nil {
					return err
				}
				if len(pending) > 0 {
					for _, input := range pending {
						response, approved, err := prompt(out, in, input)
						if err != nil {
							return err
						}
						if _, err := s.RespondToInput(waitCtx, input.ID, response, approved); err != nil {
							return err
						}
					}
					finished = startWait()
					continue
				}
				// 状态刚变为等待但请求尚未落库，稍等再看
				time.Sleep(100 * time.Millisecond)
				finished = startWait()
				continue
			}
			return printOutcome(result.run, out)
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

// prompt 向用户展示等待中的请求，并从标准输入读取回答。
func prompt(out io.Writer, in *bufio.Reader, input domain.InputRequest) (string, bool, error) {
	fmt.Fprintf(out, "--- %s required ---\n%s\n", input.Kind, input.Prompt)
	if input.Kind == "approval" {
		fmt.Fprint(out, "approve? [y/N] ")
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return "", false, fmt.Errorf("approval %d needs a yes/no answer on stdin", input.ID)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return "", answer == "y" || answer == "yes", nil
	}
	fmt.Fprint(out, "answer: ")
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", false, fmt.Errorf("input %d needs an answer on stdin", input.ID)
	}
	answer := strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(answer) == "" {
		return "", false, fmt.Errorf("answer cannot be empty")
	}
	return answer, false, nil
}

// readEvents 读取根运行及其所有子运行的新事件，每个 run 维护独立游标，
// 合并后按持久化序列排序。
func readEvents(ctx context.Context, s service, root domain.RunID, cursors map[domain.RunID]int64) ([]domain.Event, map[domain.RunID]bool, error) {
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
	for id := range ids {
		cursor := cursors[id]
		for {
			batch, err := s.Observe(ctx, id, cursor)
			if err != nil {
				return nil, nil, fmt.Errorf("observe run %d after %d: %w", id, cursor, err)
			}
			if len(batch) == 0 {
				break
			}
			sort.SliceStable(batch, func(i, j int) bool { return batch[i].Sequence < batch[j].Sequence })
			advanced := false
			for _, e := range batch {
				if e.RunID == id && e.Sequence > cursor {
					events = append(events, e)
					cursor = e.Sequence
					advanced = true
				}
			}
			if !advanced {
				return nil, nil, fmt.Errorf("observe run %d returned no advancing sequence", id)
			}
		}
		cursors[id] = cursor
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	return events, ids, nil
}

// printOutcome 输出运行的最终状态。
func printOutcome(run *domain.Run, out io.Writer) error {
	if run == nil {
		return fmt.Errorf("service returned no final run")
	}
	if _, err := fmt.Fprintf(out, "--- run=%d session=%d task=%d state=%s ---\n", run.ID, run.SessionID, run.TaskID, run.State); err != nil {
		return err
	}
	if run.State == domain.Failed {
		return fmt.Errorf("run %d failed: %s", run.ID, run.Error)
	}
	return nil
}
