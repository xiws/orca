// Package bootstrap 负责初始化运行环境，将所有子系统连接组装为可用的 Environment。
package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/config"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/providers"
	"github.com/xiws/orca/internal/store"
	"github.com/xiws/orca/internal/tools"
	"github.com/xiws/orca/internal/workflow"
)

// Environment 组装了应用运行所需的所有核心组件。
type Environment struct {
	Service *app.Service   // 应用服务层
	store   *store.Store   // 状态存储
	tools   *tools.Gateway // 工具网关
}

// Open 打开工作空间并初始化所有子系统：配置加载、状态存储、工具网关、
// 模型客户端、Agent 运行器、工作流运行器和应用服务。
func Open(workspace string) (*Environment, error) {
	// 按优先级确定工作空间路径：参数 > 环境变量 > 当前目录。
	if workspace == "" {
		workspace = os.Getenv("WORKSPACE")
	}
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	// 解析为绝对路径并跟随符号链接。
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	// 加载多层配置。
	cfg, err := config.Load(workspace)
	if err != nil {
		return nil, err
	}
	// 打开状态存储（含工作空间锁和崩溃恢复）。
	db, err := store.Open(workspace)
	if err != nil {
		return nil, err
	}
	if err := db.Recover(context.Background()); err != nil && !errors.Is(err, domain.ErrUnknown) {
		db.Close()
		return nil, err
	}
	// 初始化工具网关。
	gateway, err := tools.New(workspace, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	// 创建模型客户端和 Agent/Workflow 运行器。
	client := providers.New(cfg)
	agentRunner := agent.NewRunner(db, client, gateway, 4)
	workflowRunner := workflow.NewRunner(db, agentRunner)
	// 组装应用服务。
	service := app.NewService(db, workflowRunner, app.Options{
		Workspace: workspace, DefaultModel: cfg.DefaultModel(), Limits: domain.DefaultLimits(),
		Policy:        domain.Policy{Tools: []string{"read", "write", "edit", "bash", "create_task", "request_input"}},
		SystemPrompt:  cfg.SystemPrompt,
		ValidateModel: func(ctx context.Context, ref model.Ref) error { _, err := cfg.Resolve(ctx, ref); return err },
	})
	// 将事件发布回调绑定到 Agent 运行器。
	agentRunner.Sink = service.Publish
	return &Environment{Service: service, store: db, tools: gateway}, nil
}

// Import 从外部 JSON 文件导入会话数据。
func (e *Environment) Import(ctx context.Context, path, owner string) (domain.SessionID, error) {
	return e.store.Import(ctx, path, owner)
}

// Close 按顺序关闭所有子系统：应用服务、工具网关、状态存储。
func (e *Environment) Close() error {
	return errors.Join(e.Service.Close(), e.tools.Close(), e.store.Close())
}
