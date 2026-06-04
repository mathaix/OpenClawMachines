package sampler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPPusher implements Pusher by POSTing batches to the backend's
// agent-ingest endpoint. Wire format mirrors the design spec:
//
//	POST /api/agent/vm-metrics
//	Authorization: Bearer <agent-token>
//	{"host_id": 7, "points": [...]}
type HTTPPusher struct {
	BackendURL string
	AgentToken string
	Client     *http.Client // optional; defaults to a 10 s timeout client
}

type wirePoint struct {
	VMID      string   `json:"vm_id"`
	Kind      string   `json:"kind"`
	Timestamp string   `json:"ts"`
	CPUPct    *float32 `json:"cpu_pct"` // explicit null encoded as "cpu_pct":null
	MemBytes  int64    `json:"mem_bytes"`
}

type wireBody struct {
	HostID int         `json:"host_id"`
	Points []wirePoint `json:"points"`
}

// Push serializes the batch and POSTs it. Non-2xx responses are returned
// as errors so the Sampler can requeue and back off.
func (p *HTTPPusher) Push(ctx context.Context, hostID int, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	body := wireBody{HostID: hostID, Points: make([]wirePoint, len(points))}
	for i, pt := range points {
		body.Points[i] = wirePoint{
			VMID:      pt.VMID,
			Kind:      string(pt.Kind),
			Timestamp: pt.Timestamp.UTC().Format(time.RFC3339Nano),
			CPUPct:    pt.CPUPct,
			MemBytes:  pt.MemBytes,
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimSuffix(p.BackendURL, "/") + "/api/agent/vm-metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.AgentToken)

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vm-metrics ingest %s: %d: %s", url, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
