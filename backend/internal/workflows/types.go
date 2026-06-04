package workflows

import (
	"encoding/json"
	"time"
)

const (
	StatusQueued               = "queued"
	StatusRunning              = "running"
	StatusWaiting              = "waiting"
	StatusCompleted            = "completed"
	StatusFailed               = "failed"
	StatusCancelled            = "cancelled"
	StatusManualActionRequired = "manual_action_required"
)

const (
	KindMigration     = "migration"
	KindProvision     = "provision"
	KindBackup        = "backup"
	KindRestore       = "restore"
	KindInstallPlugin = "install_plugin"
	KindNotification  = "send_notification"
)

const (
	QueueMachineLifecycle = "machine-lifecycle"
	QueueHostMaintenance  = "host-maintenance"
	QueueArtifactInstall  = "artifact-install"
	QueueReconcile        = "reconcile"
	QueueNotifications    = "notifications"
)

type CreateRunParams struct {
	ID          string
	Kind        string
	ScopeType   string
	ScopeID     string
	RequestedBy *int
	AccountID   *int
	Priority    string
	InputJSON   json.RawMessage
}

type EventParams struct {
	WorkflowID  string
	Phase       *string
	Level       string
	EventType   string
	Message     string
	DetailsJSON json.RawMessage
}

type LockParams struct {
	ResourceType   string
	ResourceID     string
	LockKind       string
	WorkflowID     string
	LeaseExpiresAt time.Time
}
