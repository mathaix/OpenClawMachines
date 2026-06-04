package agentapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// LogEntry is a single log line from a VM.
type LogEntry struct {
	MachineID string    `json:"machine_id"`
	Source    string    `json:"source,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Replay    bool      `json:"-"` // true for buffered entries sent on subscribe
}

// LogManager implements a circular buffer + pub-sub for VM log streaming.
type LogManager struct {
	mu          sync.RWMutex
	bufferSize  int
	buffers     map[string][]LogEntry      // circular buffers keyed by machineID
	subscribers map[string][]chan LogEntry // keyed by machineID
}

func NewLogManager(bufferSize int) *LogManager {
	return &LogManager{
		bufferSize:  bufferSize,
		buffers:     make(map[string][]LogEntry),
		subscribers: make(map[string][]chan LogEntry),
	}
}

// Append adds a log entry to the buffer and notifies subscribers.
func (lm *LogManager) Append(machineID, message string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	entry := LogEntry{
		MachineID: machineID,
		Message:   message,
		Timestamp: time.Now(),
	}

	// Circular buffer
	buf := lm.buffers[machineID]
	if len(buf) >= lm.bufferSize {
		buf = buf[1:]
	}
	lm.buffers[machineID] = append(buf, entry)

	// Notify subscribers
	for _, ch := range lm.subscribers[machineID] {
		select {
		case ch <- entry:
		default:
			slog.Warn("log.subscriber.dropped", "machine_id", entry.MachineID)
		}
	}
}

// AppendWithSource adds a log entry with a source tag and notifies subscribers.
func (lm *LogManager) AppendWithSource(machineID, source, message string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	entry := LogEntry{
		MachineID: machineID,
		Source:    source,
		Message:   message,
		Timestamp: time.Now(),
	}

	buf := lm.buffers[machineID]
	if len(buf) >= lm.bufferSize {
		buf = buf[1:]
	}
	lm.buffers[machineID] = append(buf, entry)

	for _, ch := range lm.subscribers[machineID] {
		select {
		case ch <- entry:
		default:
			slog.Warn("log.subscriber.dropped", "machine_id", entry.MachineID, "source", source)
		}
	}
}

// Subscribe returns a channel that receives log entries for a machine.
// Also sends all buffered entries immediately.
func (lm *LogManager) Subscribe(machineID string) chan LogEntry {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	ch := make(chan LogEntry, lm.bufferSize+64)

	// Send buffered entries marked as replay
	for _, entry := range lm.buffers[machineID] {
		entry.Replay = true
		ch <- entry
	}

	lm.subscribers[machineID] = append(lm.subscribers[machineID], ch)
	return ch
}

// Unsubscribe removes a subscriber channel.
func (lm *LogManager) Unsubscribe(machineID string, ch chan LogEntry) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	subs := lm.subscribers[machineID]
	for i, sub := range subs {
		if sub == ch {
			lm.subscribers[machineID] = append(subs[:i], subs[i+1:]...)
			close(ch)
			break
		}
	}

	if len(lm.subscribers[machineID]) == 0 {
		delete(lm.subscribers, machineID)
	}
}

// Writer returns an io.Writer that appends log entries for a machine.
func (lm *LogManager) Writer(machineID string) *LogWriter {
	return &LogWriter{
		manager:   lm,
		machineID: machineID,
	}
}

// Clear removes the log buffer for a machine.
func (lm *LogManager) Clear(machineID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	delete(lm.buffers, machineID)
}

// LogWriter is an io.Writer adapter that writes to the LogManager.
type LogWriter struct {
	manager   *LogManager
	machineID string
}

func (w *LogWriter) Write(p []byte) (int, error) {
	w.manager.Append(w.machineID, string(p))
	return len(p), nil
}

// HandleLogs is the SSE handler for log streaming.
// Query param: machine_id (required)
func (s *Server) HandleLogs(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		// Fallback to query param if URL path doesn't have machineID (for backward compat or direct calls)
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

	ch := s.logMgr.Subscribe(mi.MachineID)
	defer s.logMgr.Unsubscribe(mi.MachineID, ch)

	// SSE keepalive: send comment lines to prevent proxy idle timeouts (~100s on Cloudflare).
	// SSE spec says lines starting with ":" are comments, ignored by EventSource clients.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case entry, ok := <-ch:
			if !ok {
				return
			}
			if entry.Replay {
				_, _ = fmt.Fprintf(w, "event: replay\ndata: %s\n\n", entry.Message)
			} else {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", entry.Message)
			}
			flusher.Flush()
		}
	}
}
