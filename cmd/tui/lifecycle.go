// Package main 实现 Orca 的交互式终端界面（TUI），基于 Bubble Tea 框架。
package main

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiws/orca/internal/domain"
)

// operationGroup 跟踪 Bubble Tea 命令的执行。当终端 I/O 出错时，命令可能比
// Bubble Tea 存活更久；关闭此 gate 可阻止尚未调度的命令提交工作，并.join 已
// 启动的命令，之后才关闭环境。
type operationGroup struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	closed  bool
	lastRun domain.RunID
}

// track 包装一个 tea.Cmd，使其在执行期间被 operationGroup 跟踪；若组已关闭则丢弃。
func (g *operationGroup) track(cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return nil
		}
		g.wg.Add(1)
		g.mu.Unlock()
		defer g.wg.Done()
		result := cmd()
		var run *domain.Run
		switch msg := result.(type) {
		case operationMsg:
			run = msg.run
		case cancelMsg:
			run = msg.run
		}
		if run != nil {
			g.mu.Lock()
			g.lastRun = run.ID
			g.mu.Unlock()
		}
		return result
	}
}

// finish 关闭 operationGroup，等待所有已启动的命令完成后返回最后一个 run ID。
func (g *operationGroup) finish() domain.RunID {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastRun
}

// stopActiveRun 在 TUI 退出时安全停止活跃的运行；若运行处于 Waiting 且仅等待
// 人工输入（无子任务运行），则保留等待状态不取消。
func stopActiveRun(ctx context.Context, s service, id domain.RunID) error {
	if id == 0 {
		return nil
	}
	r, err := s.Run(ctx, id)
	if err != nil {
		return err
	}
	if r == nil || r.State.Terminal() || r.State == domain.Interrupted || r.State == domain.Reconciling {
		// 已经终结/中断/待核查的运行无需再取消
		return nil
	}
	if r.State == domain.Waiting {
		inputs, err := s.PendingInputs(ctx, id)
		if err != nil {
			return err
		}
		if len(inputs) > 0 {
			// 检查是否存在仍在运行的子 run
			runs, err := s.Runs(ctx)
			if err != nil {
				return err
			}
			active := false
			root := r.RootRunID
			if root == 0 {
				root = r.ID
			}
			for _, child := range runs {
				if child.RootRunID == root && (child.State == domain.Running || child.State == domain.Queued || child.State == domain.Cancelling) {
					active = true
					break
				}
			}
			if !active {
				return nil
			} // 正常退出时保留等待人工输入的状态
		}
	}
	if err := s.Cancel(ctx, id); err != nil {
		return err
	}
	_, err = s.Wait(ctx, id)
	return err
}
