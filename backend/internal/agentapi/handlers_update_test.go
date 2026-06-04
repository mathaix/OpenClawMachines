package agentapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeAgentUpdater struct {
	updated bool
	err     error

	started chan struct{}
	unblock chan struct{}
	done    chan struct{}

	startOnce sync.Once
	doneOnce  sync.Once
	calls     atomic.Int32
}

func (f *fakeAgentUpdater) CheckAndUpdate(ctx context.Context) (bool, error) {
	f.calls.Add(1)
	if f.started != nil {
		f.startOnce.Do(func() { close(f.started) })
	}
	if f.done != nil {
		defer f.doneOnce.Do(func() { close(f.done) })
	}
	if f.unblock != nil {
		select {
		case <-f.unblock:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return f.updated, f.err
}

func TestHandleTriggerUpdate_ReturnsImmediatelyAfterSchedulingBackgroundUpdate(t *testing.T) {
	resetUpdateInProgressForTest(t)

	updater := &fakeAgentUpdater{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
		done:    make(chan struct{}),
	}
	srv := &Server{updater: updater, orchestrator: newMockOrchestrator()}

	req := httptest.NewRequest(http.MethodPost, "/trigger-update", nil)
	w := httptest.NewRecorder()
	startedAt := time.Now()
	srv.handleTriggerUpdate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("trigger-update handler blocked for %s", elapsed)
	}

	select {
	case <-updater.started:
	case <-time.After(time.Second):
		t.Fatal("background update did not start")
	}
	close(updater.unblock)
	select {
	case <-updater.done:
	case <-time.After(time.Second):
		t.Fatal("background update did not finish")
	}
}

func TestHandleTriggerUpdate_RejectsConcurrentRequest(t *testing.T) {
	resetUpdateInProgressForTest(t)

	updater := &fakeAgentUpdater{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
		done:    make(chan struct{}),
	}
	srv := &Server{updater: updater, orchestrator: newMockOrchestrator()}

	firstReq := httptest.NewRequest(http.MethodPost, "/trigger-update", nil)
	firstW := httptest.NewRecorder()
	srv.handleTriggerUpdate(firstW, firstReq)
	if firstW.Code != http.StatusAccepted {
		t.Fatalf("expected first request 202, got %d: %s", firstW.Code, firstW.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/trigger-update", nil)
	secondW := httptest.NewRecorder()
	srv.handleTriggerUpdate(secondW, secondReq)
	if secondW.Code != http.StatusConflict {
		t.Fatalf("expected second request 409, got %d: %s", secondW.Code, secondW.Body.String())
	}

	close(updater.unblock)
	waitForFakeUpdaterDone(t, updater)
}

func TestHandleTriggerUpdate_ClearsInProgressAfterUpdateFailure(t *testing.T) {
	resetUpdateInProgressForTest(t)

	updater := &fakeAgentUpdater{err: errors.New("manifest unavailable"), done: make(chan struct{})}
	srv := &Server{updater: updater, orchestrator: newMockOrchestrator()}

	req := httptest.NewRequest(http.MethodPost, "/trigger-update", nil)
	w := httptest.NewRecorder()
	srv.handleTriggerUpdate(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	waitForFakeUpdaterDone(t, updater)
	waitForUpdateInProgressIdle(t)

	retryUpdater := &fakeAgentUpdater{done: make(chan struct{})}
	srv.updater = retryUpdater
	retryReq := httptest.NewRequest(http.MethodPost, "/trigger-update", nil)
	retryW := httptest.NewRecorder()
	srv.handleTriggerUpdate(retryW, retryReq)
	if retryW.Code != http.StatusAccepted {
		t.Fatalf("expected retry 202 after failed update cleared lock, got %d: %s", retryW.Code, retryW.Body.String())
	}
	waitForFakeUpdaterDone(t, retryUpdater)
}

func TestHandleTriggerUpdate_ClearsInProgressWhenAlreadyCurrent(t *testing.T) {
	resetUpdateInProgressForTest(t)

	updater := &fakeAgentUpdater{updated: false, done: make(chan struct{})}
	srv := &Server{updater: updater, orchestrator: newMockOrchestrator()}

	req := httptest.NewRequest(http.MethodPost, "/trigger-update", nil)
	w := httptest.NewRecorder()
	srv.handleTriggerUpdate(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	waitForFakeUpdaterDone(t, updater)
	waitForUpdateInProgressIdle(t)
}

func resetUpdateInProgressForTest(t *testing.T) {
	t.Helper()
	updateInProgress.Store(false)
	t.Cleanup(func() { updateInProgress.Store(false) })
}

func waitForFakeUpdaterDone(t *testing.T, updater *fakeAgentUpdater) {
	t.Helper()
	select {
	case <-updater.done:
	case <-time.After(time.Second):
		t.Fatal("fake updater did not finish")
	}
}

func waitForUpdateInProgressIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !updateInProgress.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("updateInProgress did not clear")
}
