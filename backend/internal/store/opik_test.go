package store

import (
	"encoding/json"
	"testing"
)

func TestExtractTokenCounts(t *testing.T) {
	tests := []struct {
		name           string
		usage          json.RawMessage
		wantPrompt     *int
		wantCompletion *int
		wantTotal      *int
	}{
		{
			name:           "all fields present",
			usage:          json.RawMessage(`{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}`),
			wantPrompt:     intPtr(100),
			wantCompletion: intPtr(50),
			wantTotal:      intPtr(150),
		},
		{
			name:           "partial fields",
			usage:          json.RawMessage(`{"prompt_tokens":42}`),
			wantPrompt:     intPtr(42),
			wantCompletion: nil,
			wantTotal:      nil,
		},
		{
			name:           "nil usage",
			usage:          nil,
			wantPrompt:     nil,
			wantCompletion: nil,
			wantTotal:      nil,
		},
		{
			name:           "empty usage",
			usage:          json.RawMessage(`{}`),
			wantPrompt:     nil,
			wantCompletion: nil,
			wantTotal:      nil,
		},
		{
			name:           "invalid JSON",
			usage:          json.RawMessage(`not json`),
			wantPrompt:     nil,
			wantCompletion: nil,
			wantTotal:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			span := &OpikSpan{Usage: tc.usage}
			span.extractTokenCounts()

			checkIntPtr(t, "PromptTokens", span.PromptTokens, tc.wantPrompt)
			checkIntPtr(t, "CompletionTokens", span.CompletionTokens, tc.wantCompletion)
			checkIntPtr(t, "TotalTokens", span.TotalTokens, tc.wantTotal)
		})
	}
}

func intPtr(v int) *int { return &v }

func checkIntPtr(t *testing.T, field string, got, want *int) {
	t.Helper()
	if got == nil && want == nil {
		return
	}
	if got == nil || want == nil {
		t.Errorf("%s: got %v, want %v", field, got, want)
		return
	}
	if *got != *want {
		t.Errorf("%s: got %d, want %d", field, *got, *want)
	}
}
