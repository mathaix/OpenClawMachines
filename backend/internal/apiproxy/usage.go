package apiproxy

import (
	"sync"
	"time"
)

// UsageRecord represents a single API call's usage and cost.
type UsageRecord struct {
	AccountID      int       `json:"account_id"`
	MachineID      string    `json:"machine_id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	CostMicrocents int64     `json:"cost_microcents"`
	Source         string    `json:"source"` // "platform", "byok", or "subscription"
	Timestamp      time.Time `json:"timestamp"`
}

// UsageTracker tracks per-machine API usage and spend.
type UsageTracker struct {
	mu      sync.RWMutex
	records map[string][]UsageRecord // keyed by machineID
	spend   map[string]int64         // accumulated spend in microcents, keyed by machineID
}

// NewUsageTracker creates a new UsageTracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{
		records: make(map[string][]UsageRecord),
		spend:   make(map[string]int64),
	}
}

// Record adds a usage record and updates the spend total.
func (t *UsageTracker) Record(rec UsageRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records[rec.MachineID] = append(t.records[rec.MachineID], rec)
	t.spend[rec.MachineID] += rec.CostMicrocents
}

// GetSpend returns the total spend in microcents for a machine.
func (t *UsageTracker) GetSpend(machineID string) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.spend[machineID]
}

// GetRecords returns all usage records for a machine.
func (t *UsageTracker) GetRecords(machineID string) []UsageRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	recs := t.records[machineID]
	if recs == nil {
		return []UsageRecord{}
	}
	// Return a copy to avoid data races
	out := make([]UsageRecord, len(recs))
	copy(out, recs)
	return out
}

// GetAllRecords returns all usage records for all machines.
func (t *UsageTracker) GetAllRecords() map[string][]UsageRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string][]UsageRecord, len(t.records))
	for k, v := range t.records {
		recs := make([]UsageRecord, len(v))
		copy(recs, v)
		out[k] = recs
	}
	return out
}

// FlushUsage returns all records for a machine and removes them from the tracker.
func (t *UsageTracker) FlushUsage(machineID string) []UsageRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	recs := t.records[machineID]
	if recs == nil {
		recs = []UsageRecord{}
	}
	delete(t.records, machineID)
	delete(t.spend, machineID)
	return recs
}

// RemoveMachine removes all records and spend tracking for a machine.
func (t *UsageTracker) RemoveMachine(machineID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.records, machineID)
	delete(t.spend, machineID)
}
