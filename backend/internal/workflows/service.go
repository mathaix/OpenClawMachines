package workflows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type Service struct {
	repo    store.WorkflowRepo
	runtime *Runtime
}

func NewService(ctx context.Context, repo store.WorkflowRepo, cfg Config) (*Service, error) {
	runtime, err := NewRuntime(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, runtime: runtime}, nil
}

// RegisterWorkflows registers workflow functions on the DBOS context.
// Called after Server creation to break the circular dependency between
// Service (needs DB) and Server (needs Service, provides workflow handlers).
func (s *Service) RegisterWorkflows(fn func(dbos.DBOSContext)) {
	if s == nil || s.runtime == nil || s.runtime.Context() == nil {
		return
	}
	fn(s.runtime.Context())
}

// Launch starts the DBOS executor. Must be called after RegisterWorkflows.
func (s *Service) Launch() error {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.Launch()
}

func (s *Service) Close(timeout time.Duration) {
	if s == nil || s.runtime == nil {
		return
	}
	s.runtime.Close(timeout)
}

func (s *Service) RuntimeEnabled() bool {
	return s != nil && s.runtime != nil && s.runtime.Enabled()
}

func (s *Service) Context() dbos.DBOSContext {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.Context()
}

func (s *Service) CreateRun(ctx context.Context, params CreateRunParams) (*store.WorkflowRun, error) {
	if params.ID == "" {
		id, err := generateWorkflowID()
		if err != nil {
			return nil, err
		}
		params.ID = id
	}
	if params.Priority == "" {
		params.Priority = "normal"
	}
	if len(params.InputJSON) == 0 {
		params.InputJSON = json.RawMessage(`{}`)
	}

	run := &store.WorkflowRun{
		ID:          params.ID,
		Kind:        params.Kind,
		ScopeType:   params.ScopeType,
		ScopeID:     params.ScopeID,
		Status:      StatusQueued,
		RequestedBy: params.RequestedBy,
		AccountID:   params.AccountID,
		Priority:    params.Priority,
		InputJSON:   params.InputJSON,
	}
	if err := s.repo.CreateWorkflowRun(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Service) GetRun(ctx context.Context, id string) (*store.WorkflowRun, error) {
	return s.repo.GetWorkflowRun(ctx, id)
}

func (s *Service) ListRunsByScope(ctx context.Context, scopeType, scopeID string, limit int) ([]store.WorkflowRun, error) {
	return s.repo.ListWorkflowRunsByScope(ctx, scopeType, scopeID, limit)
}

func (s *Service) ListEvents(ctx context.Context, workflowID string, limit int) ([]store.WorkflowEvent, error) {
	return s.repo.ListWorkflowEvents(ctx, workflowID, limit)
}

func (s *Service) AddEvent(ctx context.Context, params EventParams) error {
	event := &store.WorkflowEvent{
		WorkflowID:  params.WorkflowID,
		Phase:       params.Phase,
		Level:       params.Level,
		EventType:   params.EventType,
		Message:     params.Message,
		DetailsJSON: params.DetailsJSON,
	}
	return s.repo.CreateWorkflowEvent(ctx, event)
}

func (s *Service) UpdateStatus(ctx context.Context, id, status string, phase *string, summary json.RawMessage, errCode, errMsg *string) error {
	return s.repo.UpdateWorkflowRunStatus(ctx, id, status, phase, summary, errCode, errMsg)
}

func (s *Service) Complete(ctx context.Context, id, status string, output json.RawMessage, errCode, errMsg *string) error {
	return s.repo.CompleteWorkflowRun(ctx, id, status, output, errCode, errMsg)
}

func (s *Service) AcquireLock(ctx context.Context, params LockParams) error {
	lock := &store.WorkflowLock{
		ResourceType:   params.ResourceType,
		ResourceID:     params.ResourceID,
		LockKind:       params.LockKind,
		WorkflowID:     params.WorkflowID,
		LeaseExpiresAt: params.LeaseExpiresAt,
	}
	return s.repo.AcquireWorkflowLock(ctx, lock)
}

func (s *Service) RenewLock(ctx context.Context, workflowID, resourceType, resourceID, lockKind string, leaseUntil time.Time) error {
	return s.repo.RenewWorkflowLock(ctx, workflowID, resourceType, resourceID, lockKind, leaseUntil)
}

func (s *Service) ReleaseLocks(ctx context.Context, workflowID string) error {
	return s.repo.ReleaseWorkflowLocks(ctx, workflowID)
}

func generateWorkflowID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating workflow ID: %w", err)
	}
	return "wf_" + hex.EncodeToString(b[:]), nil
}

func NewID() (string, error) {
	return generateWorkflowID()
}
