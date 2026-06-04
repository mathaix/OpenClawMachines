package workflows

import (
	"context"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

type Config struct {
	AppName       string
	DatabaseURL   string
	EnableRuntime bool // Start executor goroutines (workers)
	EnableEnqueue bool // Initialize DBOS context for enqueueing only (API nodes)
	Register      func(dbos.DBOSContext)
}

type Runtime struct {
	ctx     dbos.DBOSContext
	enabled bool // true if executor is running (not just enqueue)
}

func NewRuntime(ctx context.Context, cfg Config) (*Runtime, error) {
	rt := &Runtime{enabled: cfg.EnableRuntime}

	// Neither runtime nor enqueue requested — return empty runtime
	if !cfg.EnableRuntime && !cfg.EnableEnqueue {
		return rt, nil
	}

	dbosCtx, err := dbos.NewDBOSContext(ctx, dbos.Config{
		AppName:            cfg.AppName,
		DatabaseURL:        cfg.DatabaseURL,
		ApplicationVersion: "v1", // Stable: only bump when workflow step signatures change
		Logger:             slog.Default().With("component", "dbos"),
	})
	if err != nil {
		return nil, err
	}

	dbos.NewWorkflowQueue(dbosCtx, QueueMachineLifecycle, dbos.WithPartitionQueue())
	dbos.NewWorkflowQueue(dbosCtx, QueueHostMaintenance)
	dbos.NewWorkflowQueue(dbosCtx, QueueArtifactInstall)
	dbos.NewWorkflowQueue(dbosCtx, QueueReconcile)
	dbos.NewWorkflowQueue(dbosCtx, QueueNotifications)
	if cfg.Register != nil {
		cfg.Register(dbosCtx)
	}

	rt.ctx = dbosCtx
	return rt, nil
}

// Launch starts the DBOS executor goroutines. Must be called after all
// workflows are registered (via the Register callback or RegisterWorkflows).
// Only meaningful when EnableRuntime=true; no-ops otherwise.
func (r *Runtime) Launch() error {
	if r == nil || !r.enabled || r.ctx == nil {
		return nil
	}
	return dbos.Launch(r.ctx)
}

func (r *Runtime) Enabled() bool {
	return r != nil && r.enabled
}

func (r *Runtime) Context() dbos.DBOSContext {
	if r == nil {
		return nil
	}
	return r.ctx
}

func (r *Runtime) Close(timeout time.Duration) {
	if r == nil || !r.enabled || r.ctx == nil {
		return
	}
	dbos.Shutdown(r.ctx, timeout)
}
