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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "orca tui:", safeText(err.Error()))
		os.Exit(1)
	}
}

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
	// Bubble Tea's default signal handler quits immediately. Route signals
	// through Update instead, so it persists Cancel and waits before tea.Quit.
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
	// A terminal failure may race an in-flight Submit before Update receives its
	// RunID. Join the command gate first, then Cancel/Wait the actual run before
	// Environment.Close joins workers and closes persistence.
	id := m.operations.finish()
	err = errors.Join(err, stopActiveRun(context.Background(), env.Service, id))
	return err
}
