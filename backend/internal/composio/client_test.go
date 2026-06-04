package composio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListConnections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/connected_accounts" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("user_id") != "machine-123" {
			t.Errorf("missing or wrong user_id query param: %s", r.URL.Query().Get("user_id"))
		}
		if r.Header.Get("x-api-key") != "ck_test" {
			t.Errorf("missing or wrong x-api-key header: %s", r.Header.Get("x-api-key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]interface{}{
				{
					"id":         "ca_abc",
					"user_id":    "machine-123",
					"status":     "ACTIVE",
					"created_at": "2026-03-28T12:00:00Z",
					"toolkit":    map[string]interface{}{"slug": "gmail"},
				},
				{
					"id":         "ca_other",
					"user_id":    "machine-123",
					"status":     "ACTIVE",
					"created_at": "2026-03-28T12:00:00Z",
					"toolkit":    map[string]interface{}{"slug": "slack"},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	conns, err := c.ListConnections(context.Background(), "machine-123")
	if err != nil {
		t.Fatalf("ListConnections error: %v", err)
	}
	if len(conns) != 2 {
		t.Fatalf("got %d connections, want 2", len(conns))
	}
	if conns[0].ID != "ca_abc" {
		t.Errorf("ID = %q, want ca_abc", conns[0].ID)
	}
	if conns[0].Toolkit != "gmail" {
		t.Errorf("Toolkit = %q, want gmail", conns[0].Toolkit)
	}
	if conns[0].Status != "active" {
		t.Errorf("Status = %q, want active", conns[0].Status)
	}
}

func TestListConnectionsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": []interface{}{}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	conns, err := c.ListConnections(context.Background(), "machine-123")
	if err != nil {
		t.Fatalf("ListConnections error: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("got %d connections, want 0", len(conns))
	}
}

func TestListConnectionsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "invalid key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_bad")
	_, err := c.ListConnections(context.Background(), "machine-123")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestCreateConnectLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v3/connected_accounts/link" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body["user_id"] != "machine-456" {
			t.Errorf("user_id = %v, want machine-456", body["user_id"])
		}
		if body["auth_config_id"] != "ac_gmail" {
			t.Errorf("auth_config_id = %v, want ac_gmail", body["auth_config_id"])
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"redirect_url": "https://accounts.google.com/o/oauth2/auth?...",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	resp, err := c.CreateConnectLink(context.Background(), "machine-456", "ac_gmail", "https://example.com/callback")
	if err != nil {
		t.Fatalf("CreateConnectLink error: %v", err)
	}
	if resp.URL == "" {
		t.Error("redirect URL is empty")
	}
}

func TestDeleteConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/v3/connected_accounts/ca_xyz" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	err := c.DeleteConnection(context.Background(), "ca_xyz")
	if err != nil {
		t.Fatalf("DeleteConnection error: %v", err)
	}
}

func TestDeleteAllConnections(t *testing.T) {
	listCalled := false
	deleteCalled := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/connected_accounts":
			listCalled = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []map[string]interface{}{
					{"id": "ca_1", "user_id": "machine-789", "status": "ACTIVE", "toolkit": map[string]interface{}{"slug": "gmail"}},
					{"id": "ca_2", "user_id": "machine-789", "status": "ACTIVE", "toolkit": map[string]interface{}{"slug": "slack"}},
				},
			})
		case r.Method == http.MethodDelete:
			deleteCalled++
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	err := c.DeleteAllConnections(context.Background(), "machine-789")
	if err != nil {
		t.Fatalf("DeleteAllConnections error: %v", err)
	}
	if !listCalled {
		t.Error("ListConnections was not called")
	}
	if deleteCalled != 2 {
		t.Errorf("DeleteConnection called %d times, want 2", deleteCalled)
	}
}

func TestListTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/tools" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("user_id") != "machine-abc" {
			t.Errorf("missing or wrong user_id query param: %s", r.URL.Query().Get("user_id"))
		}
		if r.URL.Query().Get("toolkit_slug") != "gmail" {
			t.Errorf("missing or wrong toolkit_slug query param: %s", r.URL.Query().Get("toolkit_slug"))
		}
		if r.Header.Get("x-api-key") != "ck_test" {
			t.Errorf("missing or wrong x-api-key header: %s", r.Header.Get("x-api-key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]interface{}{
				{
					"slug":             "GMAIL_SEND_EMAIL",
					"name":             "Send an email via Gmail",
					"description":      "Send an email via Gmail",
					"toolkit":          map[string]interface{}{"slug": "gmail", "name": "gmail"},
					"input_parameters": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"to": map[string]interface{}{"type": "string"}}},
				},
			},
			"total_items": 1,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	tools, err := c.ListTools(context.Background(), "machine-abc", "gmail")
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].Slug != "GMAIL_SEND_EMAIL" {
		t.Errorf("Slug = %q, want GMAIL_SEND_EMAIL", tools[0].Slug)
	}
	if tools[0].Name != "Send an email via Gmail" {
		t.Errorf("Name = %q, want 'Send an email via Gmail'", tools[0].Name)
	}
	if tools[0].Toolkit.Slug != "gmail" {
		t.Errorf("Toolkit.Slug = %q, want gmail", tools[0].Toolkit.Slug)
	}
	if tools[0].Description == "" {
		t.Error("Description is empty")
	}
	if tools[0].Parameters == nil {
		t.Error("Parameters is nil (should be decoded from input_parameters)")
	}
}

func TestListToolsAllToolkits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("toolkit_slug") != "" {
			t.Errorf("toolkit_slug query param should be absent when empty, got: %s", r.URL.Query().Get("toolkit_slug"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items":       []interface{}{},
			"total_items": 0,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	tools, err := c.ListTools(context.Background(), "machine-abc", "")
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("got %d tools, want 0", len(tools))
	}
}

func TestExecuteAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v3/tools/execute/GMAIL_SEND_EMAIL" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body["user_id"] != "machine-abc" {
			t.Errorf("user_id = %v, want machine-abc", body["user_id"])
		}
		if body["arguments"] == nil {
			t.Error("arguments key is missing from request body")
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":       map[string]interface{}{"message_id": "msg_123"},
			"successful": true,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	result, err := c.ExecuteAction(context.Background(), "GMAIL_SEND_EMAIL", "machine-abc", "gmail", map[string]interface{}{
		"to":      "user@example.com",
		"subject": "Hello",
	})
	if err != nil {
		t.Fatalf("ExecuteAction error: %v", err)
	}
	if !result.Successful {
		t.Error("expected Successful = true")
	}
	if result.Data == nil {
		t.Error("expected non-nil Data")
	}
}

func TestExecuteActionAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "invalid action parameters"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	_, err := c.ExecuteAction(context.Background(), "GMAIL_SEND_EMAIL", "machine-abc", "gmail", nil)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}
