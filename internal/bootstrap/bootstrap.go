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

type Environment struct {
	Service *app.Service
	store   *store.Store
	tools   *tools.Gateway
}

func Open(workspace string) (*Environment, error) {
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
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(workspace)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(workspace)
	if err != nil {
		return nil, err
	}
	if err := db.Recover(context.Background()); err != nil && !errors.Is(err, domain.ErrUnknown) {
		db.Close()
		return nil, err
	}
	gateway, err := tools.New(workspace, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	client := providers.New(cfg)
	agentRunner := agent.NewRunner(db, client, gateway, 4)
	workflowRunner := workflow.NewRunner(db, agentRunner)
	service := app.NewService(db, workflowRunner, app.Options{
		Workspace: workspace, DefaultModel: cfg.DefaultModel(), Limits: domain.DefaultLimits(),
		Policy:        domain.Policy{Tools: []string{"read", "write", "edit", "bash", "create_task", "request_input"}},
		SystemPrompt:  cfg.SystemPrompt,
		ValidateModel: func(ctx context.Context, ref model.Ref) error { _, err := cfg.Resolve(ctx, ref); return err },
	})
	agentRunner.Sink = service.Publish
	return &Environment{Service: service, store: db, tools: gateway}, nil
}

func (e *Environment) Import(ctx context.Context, path, owner string) (domain.SessionID, error) {
	return e.store.Import(ctx, path, owner)
}

func (e *Environment) Close() error {
	return errors.Join(e.Service.Close(), e.tools.Close(), e.store.Close())
}
