package commands

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// staticToken returns a serveChatConfig.machineToken func that always yields
// the given token (no refresh behavior). Used by tests that don't exercise
// token rotation.
func staticToken(tok string) func() (string, error) {
	return func() (string, error) { return tok, nil }
}

// Fake upstream that mimics the auth proxy's /gateway/v1/... route.
// Verifies token forwarding, exact path rewrite, absence of inbound Authorization,
// and that the body is forwarded intact.
func fakeUpstream(t *testing.T, wantToken string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Machine-Token") != wantToken {
			t.Errorf("upstream: X-Machine-Token = %q, want %q", r.Header.Get("X-Machine-Token"), wantToken)
		}
		if got := r.URL.Path; got != "/gateway/v1/chat/completions" {
			t.Errorf("upstream: path = %q, want %q", got, "/gateway/v1/chat/completions")
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("upstream: inbound Authorization leaked = %q, want empty", got)
		}
		body, _ := io.ReadAll(r.Body)
		// Only assert content when the test actually sent a non-trivial body —
		// otherwise "{}" (len 2) would spuriously fail. Tests that care about
		// body fidelity send a payload containing the field name "model".
		if len(body) > 2 && !strings.Contains(string(body), `"model"`) {
			t.Errorf("upstream: body did not contain the forwarded payload; got %q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","choices":[]}`)
	}))
}

func TestServeChatHandler_ForwardsWithMachineToken(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: staticToken("JWT_ABC"),
		apiKey:       "sk-ocm_test",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-ocm_test")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"id":"test"`) {
		t.Errorf("body = %q, want upstream body", rr.Body.String())
	}
}

func TestServeChatHandler_RejectsMissingAPIKey(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: staticToken("JWT_ABC"),
		apiKey:       "sk-ocm_test",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestServeChatHandler_RejectsWrongAPIKey(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: staticToken("JWT_ABC"),
		apiKey:       "sk-ocm_correct",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk-ocm_wrong")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestServeChatHandler_NoAuthSkipsInboundCheck(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: staticToken("JWT_ABC"),
		apiKey:       "", // disabled
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestServeChatHandler_TokenRefreshFailureReturns502(t *testing.T) {
	// Upstream never reached — handler should short-circuit on refresh error.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream: unexpectedly called when token refresh failed")
	}))
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: func() (string, error) { return "", errors.New("refresh boom") },
		apiKey:       "",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "failed to refresh machine token") {
		t.Errorf("body = %q, want refresh error message", rr.Body.String())
	}
}

func TestTokenRefresher_RefreshesOnceExpired(t *testing.T) {
	// Initial token already expired so the next Get() must trigger a refresh.
	past := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	future := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)

	var calls int32
	tr := newTokenRefresher(
		&machineTokenResp{Token: "old", ExpiresAt: past},
		func() (*machineTokenResp, error) {
			atomic.AddInt32(&calls, 1)
			return &machineTokenResp{Token: "new", ExpiresAt: future}, nil
		},
	)

	got, err := tr.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "new" {
		t.Errorf("Get = %q, want %q", got, "new")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("fetch called %d times, want 1", atomic.LoadInt32(&calls))
	}

	// Second call within skew: no additional fetch.
	got, err = tr.Get()
	if err != nil {
		t.Fatalf("Get #2: %v", err)
	}
	if got != "new" {
		t.Errorf("Get #2 = %q, want cached %q", got, "new")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("fetch called %d times, want still 1", atomic.LoadInt32(&calls))
	}
}

func TestTokenRefresher_PassesThroughFetchErrors(t *testing.T) {
	past := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	tr := newTokenRefresher(
		&machineTokenResp{Token: "old", ExpiresAt: past},
		func() (*machineTokenResp, error) { return nil, errors.New("network down") },
	)

	if _, err := tr.Get(); err == nil {
		t.Fatal("Get: expected error from failed refresh, got nil")
	}
}

func TestServeChatHandler_StreamingPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test upstream: no flusher")
		}
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n", "data: [DONE]\n\n"} {
			_, _ = io.WriteString(w, chunk)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: staticToken("JWT"),
		apiKey:       "",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"data: one", "data: two", "[DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: got %q", want, body)
		}
	}
}
