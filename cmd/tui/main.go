package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiws/orca/internal/bootstrap"
)

// main 是 TUI 程序入口，运行失败时打印安全处理后的错误信息。
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "orca tui:", safeText(err.Error()))
		os.Exit(1)
	}
}

// run 负责参数校验、环境打开、构造 model 并运行 Bubble Tea 程序；
// 退出时.join 命令 gate，再停止活跃运行，最后由 defer 关闭环境。
func run() (err error) {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help") {
			fmt.Fprintln(os.Stdout, "Usage: orca-tui\n"+welcomeText())
			return nil
		}
		return fmt.Errorf("usage: orca-tui (no arguments; use /help inside the TUI)")
	}
	env, err := bootstrap.Open("")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, env.Close()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newModel(env.Service)
	m.ctx = ctx
	// Bubble Tea 默认的信号处理会立即退出。将信号路由到 Update，
	// 以便在 tea.Quit 前持久化 Cancel 并等待。
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithoutSignalHandler())
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-signals.Done():
			p.Send(exitRequestedMsg{})
		case <-finished:
		}
	}()
	final, err := p.Run()
	if last, ok := final.(model); ok {
		last.stopObserving()
	}
	// 终端失败可能与正在进行的 Submit 竞争，Update 尚未收到其 RunID。
	// 先 .join 命令 gate，再对实际的 run 做 Cancel/Wait，最后由
	// Environment.Close .join worker 并关闭持久化。
	id := m.operations.finish()
	err = errors.Join(err, stopActiveRun(context.Background(), env.Service, id))
	return err
}
