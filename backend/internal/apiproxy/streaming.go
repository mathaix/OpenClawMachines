package apiproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// usageSource returns the usage source label for a provider.
func usageSource(providerName string) string {
	switch providerName {
	case "nebius":
		return "platform"
	case "openai-codex":
		return "subscription"
	default:
		return "byok"
	}
}

// SSECopier copies an SSE stream from upstream to the client while tracking usage.
type SSECopier struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	provider *Provider
	usage    struct {
		model        string
		inputTokens  int
		outputTokens int
	}
}

// Copy reads the upstream SSE stream, writes each line to the client, and parses
// usage information from SSE events via the provider's ParseSSEEvent function.
func (c *SSECopier) Copy(upstream io.Reader) error {
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB max buffer

	var currentEvent string
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		// Write the line to the client immediately
		_, err := io.WriteString(c.w, line+"\n")
		if err != nil {
			return err
		}
		c.flusher.Flush()

		// Parse SSE protocol
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			dataLines = nil
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
		} else if line == "" {
			// Blank line = event boundary
			if len(dataLines) > 0 && c.provider.ParseSSEEvent != nil {
				dataStr := strings.TrimSpace(strings.Join(dataLines, "\n"))
				model, input, output, isFinal := c.provider.ParseSSEEvent(currentEvent, dataStr)
				if model != "" {
					c.usage.model = model
				}
				if input > 0 {
					c.usage.inputTokens = input
				}
				if output > 0 {
					c.usage.outputTokens = output
				}
				_ = isFinal // We accumulate usage regardless
			}
			currentEvent = ""
			dataLines = nil
		}
	}

	return scanner.Err()
}

// Usage returns the accumulated usage from the SSE stream.
func (c *SSECopier) Usage() (model string, inputTokens, outputTokens int) {
	return c.usage.model, c.usage.inputTokens, c.usage.outputTokens
}

// streamSSE proxies an SSE stream to the client and records usage after completion.
func (p *Proxy) streamSSE(w http.ResponseWriter, body io.ReadCloser, ctx *proxyCallContext) {
	defer func() { _ = body.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("apiproxy.streaming_not_supported", "machine_id", ctx.machineID)
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	copier := &SSECopier{
		w:        w,
		flusher:  flusher,
		provider: ctx.provider,
	}

	if err := copier.Copy(body); err != nil {
		slog.Warn("apiproxy.sse_copy_error", "machine_id", ctx.machineID, "provider", ctx.provider.Name, "error", err)
	}

	model, inputTokens, outputTokens := copier.Usage()
	p.recordAndLogUsage(ctx, model, inputTokens, outputTokens)
}

// forwardJSON reads a non-streaming JSON response, writes it to the client,
// and records usage.
func (p *Proxy) forwardJSON(w http.ResponseWriter, resp *http.Response, ctx *proxyCallContext) {
	defer func() { _ = resp.Body.Close() }()

	// Read response body (limit to 10MB)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		slog.Error("apiproxy.read_body_failed", "machine_id", ctx.machineID, "provider", ctx.provider.Name, "error", err)
		http.Error(w, "failed to read upstream response", http.StatusBadGateway)
		return
	}

	// Log error response bodies for diagnostics
	if resp.StatusCode >= 400 {
		errBody := string(body)
		if len(errBody) > 500 {
			errBody = errBody[:500]
		}
		slog.Warn("apiproxy.upstream_error_response",
			"provider", ctx.provider.Name,
			"machine_id", ctx.machineID,
			"status", resp.StatusCode,
			"body", errBody,
		)
		body = normalizeErrorResponseBody(body)
	}

	// Write status and body to client
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)

	// Parse usage from body
	var model string
	var inputTokens, outputTokens int
	if ctx.provider.ParseJSONUsage != nil {
		model, inputTokens, outputTokens = ctx.provider.ParseJSONUsage(body)
	}
	p.recordAndLogUsage(ctx, model, inputTokens, outputTokens)
}

func normalizeErrorResponseBody(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return body
	}

	var payload []json.RawMessage
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return body
	}
	if len(payload) != 1 {
		return body
	}

	var first map[string]any
	if err := json.Unmarshal(payload[0], &first); err != nil {
		return body
	}
	if _, ok := first["error"]; !ok {
		return body
	}

	normalized, err := json.Marshal(first)
	if err != nil {
		return body
	}
	return normalized
}

// recordAndLogUsage records a usage record and emits a single structured debug log
// with the full call details: provider, model, tokens, cost, duration, status.
func (p *Proxy) recordAndLogUsage(ctx *proxyCallContext, model string, inputTokens, outputTokens int) {
	durationMs := time.Since(ctx.startTime).Milliseconds()
	source := usageSource(ctx.provider.Name)
	cost := CalculateCost(model, inputTokens, outputTokens)

	if model != "" || inputTokens > 0 || outputTokens > 0 {
		rec := UsageRecord{
			AccountID:      ctx.accountID,
			MachineID:      ctx.machineID,
			Provider:       ctx.provider.Name,
			Model:          model,
			InputTokens:    inputTokens,
			OutputTokens:   outputTokens,
			CostMicrocents: cost,
			Source:         source,
			Timestamp:      time.Now(),
		}
		p.usage.Record(rec)
		go p.reportUsageToBackend(rec)
	}

	// Unified debug log — one line per API call with everything you need
	slog.Debug("apiproxy.call",
		"machine_id", ctx.machineID,
		"account_id", ctx.accountID,
		"provider", ctx.provider.Name,
		"credential_type", ctx.credType,
		"source", source,
		"model", model,
		"input_tokens", inputTokens,
		"output_tokens", outputTokens,
		"total_tokens", inputTokens+outputTokens,
		"cost_microcents", cost,
		"status", ctx.status,
		"method", ctx.method,
		"path", ctx.path,
		"duration_ms", durationMs,
	)
}
