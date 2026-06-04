package agentapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ProgressEvent represents a provisioning progress update.
type ProgressEvent struct {
	MachineID string `json:"machine_id"`
	Step      string `json:"step"`
	Message   string `json:"message"`
}

// ProgressManager implements pub-sub for VM provisioning progress.
// It keeps a per-machine event history so late subscribers receive all past events.
type ProgressManager struct {
	mu          sync.RWMutex
	subscribers map[string][]chan ProgressEvent // keyed by machineID
	history     map[string][]ProgressEvent      // past events per machineID
}

func NewProgressManager() *ProgressManager {
	return &ProgressManager{
		subscribers: make(map[string][]chan ProgressEvent),
		history:     make(map[string][]ProgressEvent),
	}
}

// Subscribe returns a channel that receives progress events for a machine.
// Any previously emitted events are replayed into the channel immediately.
func (pm *ProgressManager) Subscribe(machineID string) chan ProgressEvent {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ch := make(chan ProgressEvent, 16)

	// Replay past events so late subscribers see the full history
	for _, event := range pm.history[machineID] {
		ch <- event
	}

	pm.subscribers[machineID] = append(pm.subscribers[machineID], ch)
	return ch
}

// Unsubscribe removes a subscriber channel for a machine.
func (pm *ProgressManager) Unsubscribe(machineID string, ch chan ProgressEvent) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	subs := pm.subscribers[machineID]
	for i, sub := range subs {
		if sub == ch {
			pm.subscribers[machineID] = append(subs[:i], subs[i+1:]...)
			close(ch)
			break
		}
	}

	if len(pm.subscribers[machineID]) == 0 {
		delete(pm.subscribers, machineID)
	}
}

// Emit sends a progress event to all subscribers for a machine
// and stores it in history for late subscribers.
func (pm *ProgressManager) Emit(machineID, step, message string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	event := ProgressEvent{
		MachineID: machineID,
		Step:      step,
		Message:   message,
	}

	pm.history[machineID] = append(pm.history[machineID], event)

	for _, ch := range pm.subscribers[machineID] {
		select {
		case ch <- event:
		default:
			// Subscriber too slow, drop event
		}
	}
}

// Emitter returns a VMProgressEmitter for convenience.
func (pm *ProgressManager) Emitter(machineID string) *VMProgressEmitter {
	return &VMProgressEmitter{
		manager:   pm,
		machineID: machineID,
	}
}

// VMProgressEmitter is a convenience wrapper for emitting progress events.
type VMProgressEmitter struct {
	manager   *ProgressManager
	machineID string
}

// Emit sends a progress event for this VM.
func (e *VMProgressEmitter) Emit(step, message string) {
	slog.Info("vm.progress", "machine_id", e.machineID, "step", step, "message", message)
	e.manager.Emit(e.machineID, step, message)
}

// HandleProgress is the SSE handler for progress events.
// Query param: machine_id (required)
func (s *Server) HandleProgress(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		machineID := r.URL.Query().Get("machine_id")
		if machineID == "" {
			http.Error(w, "machine_id required", http.StatusBadRequest)
			return
		}
		vm, err := s.orchestrator.Get(r.Context(), machineID)
		if err != nil {
			http.Error(w, "VM not found", http.StatusNotFound)
			return
		}
		mi = &machineInfo{MachineID: machineID, ProxyToken: vm.ProxyToken}
	}

	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.progress.Subscribe(mi.MachineID)
	defer s.progress.Unsubscribe(mi.MachineID, ch)

	// SSE keepalive: send comment lines to prevent proxy idle timeouts (~100s on Cloudflare).
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

			// Close stream on terminal events
			if event.Step == "machine_ready" || event.Step == "error" {
				return
			}
		}
	}
}
