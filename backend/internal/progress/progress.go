package progress

import (
	"context"
	"log/slog"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// Tracker tracks provisioning progress for Machines.
// Updates provision_step and emits events during Machine boot.
// Ported from ClaraTeach progress tracker.
type Tracker struct {
	store    store.Store
	activity *events.Activity
}

func New(s store.Store, a *events.Activity) *Tracker {
	return &Tracker{store: s, activity: a}
}

// Step represents a provisioning step.
type Step struct {
	Name    string
	Message string
}

// Standard provisioning steps for starting a Machine.
var ProvisionSteps = []Step{
	{Name: "allocating", Message: "Allocating host capacity"},
	{Name: "rootfs", Message: "Preparing root filesystem"},
	{Name: "network", Message: "Configuring network"},
	{Name: "booting", Message: "Booting MicroVM"},
	{Name: "openclaw", Message: "Starting OpenClaw agent"},
	{Name: "channels", Message: "Connecting channels"},
	{Name: "ready", Message: "Machine is running"},
}

// UpdateStep updates the provisioning step for a Machine.
func (t *Tracker) UpdateStep(ctx context.Context, machineID string, step Step) error {
	slog.Info("progress.step.updated", "machine_id", machineID, "step", step.Name, "message", step.Message)

	if err := t.store.UpdateMachineStatus(ctx, machineID, "provisioning", &step.Message); err != nil {
		return err
	}

	mid := machineID
	t.activity.Log(ctx, events.LogParams{
		Category:  "machine",
		Action:    "machine.provision_step",
		Status:    "success",
		ActorType: "system",
		MachineID: &mid,
		Summary:   step.Message,
		Detail:    map[string]any{"step": step.Name},
	})
	return nil
}

// MarkComplete marks provisioning as complete.
func (t *Tracker) MarkComplete(ctx context.Context, machineID string) error {
	slog.Info("progress.completed", "machine_id", machineID)

	msg := "Machine is running"
	return t.store.UpdateMachineStatus(ctx, machineID, "running", &msg)
}

// MarkError marks provisioning as failed.
func (t *Tracker) MarkError(ctx context.Context, machineID string, errMsg string) error {
	slog.Error("progress.error", "machine_id", machineID, "error_message", errMsg)

	return t.store.UpdateMachineStatus(ctx, machineID, "error", &errMsg)
}
