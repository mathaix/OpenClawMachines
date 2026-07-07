package api

import "testing"

func TestGoogleWorkspaceScopesForPermissionLevels(t *testing.T) {
	scopes, err := googleWorkspaceScopesForPermissionLevels(map[string]string{
		"gmail":    "read_write",
		"drive":    "read",
		"calendar": "off",
		"docs":     "read_write",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"openid",
		"email",
		"profile",
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/drive.metadata.readonly",
		"https://www.googleapis.com/auth/drive.file",
	} {
		if !containsString(scopes, want) {
			t.Fatalf("scopes missing %q: %v", want, scopes)
		}
	}
	for _, unwanted := range []string{
		"https://www.googleapis.com/auth/calendar.events.readonly",
		"https://www.googleapis.com/auth/calendar.events",
	} {
		if containsString(scopes, unwanted) {
			t.Fatalf("scopes unexpectedly included %q: %v", unwanted, scopes)
		}
	}
}

func TestGoogleWorkspaceScopesForPermissionLevelsRejectsUnknownValues(t *testing.T) {
	if _, err := googleWorkspaceScopesForPermissionLevels(map[string]string{"gmail": "delete_everything"}); err == nil {
		t.Fatal("expected invalid permission level to fail")
	}
	if _, err := googleWorkspaceScopesForPermissionLevels(map[string]string{"unknown": "read"}); err == nil {
		t.Fatal("expected unknown permission service to fail")
	}
}

func TestOAuthProviderEnvPrefix(t *testing.T) {
	cases := map[string]string{
		"google-workspace": "GOOGLE_WORKSPACE",
		"slack":            "SLACK",
		"apollo.io":        "APOLLO_IO",
		"":                 "",
	}
	for slug, want := range cases {
		if got := oauthProviderEnvPrefix(slug); got != want {
			t.Errorf("oauthProviderEnvPrefix(%q) = %q, want %q", slug, got, want)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestOAuthClientCredentialsForProvider(t *testing.T) {
	srv := &Server{oauthClientID: "global-id", oauthClientSecret: "global-secret"}

	// No per-provider env → falls back to the platform-wide client.
	if id, secret := srv.oauthClientCredentialsForProvider("google-workspace"); id != "global-id" || secret != "global-secret" {
		t.Fatalf("fallback creds = %q/%q, want global", id, secret)
	}

	// Per-provider env present → used instead of the global client.
	t.Setenv("SLACK_OAUTH_CLIENT_ID", "slack-id")
	t.Setenv("SLACK_OAUTH_CLIENT_SECRET", "slack-secret")
	if id, secret := srv.oauthClientCredentialsForProvider("slack"); id != "slack-id" || secret != "slack-secret" {
		t.Fatalf("per-provider creds = %q/%q, want slack", id, secret)
	}
	if !srv.workspaceIntegrationOAuthConfiguredForProvider("slack") {
		t.Fatal("slack should be configured via per-provider env")
	}

	// Partial per-provider env (id only) → falls back to global to avoid a
	// mismatched id/secret pair.
	t.Setenv("NOTION_OAUTH_CLIENT_ID", "notion-id")
	if id, secret := srv.oauthClientCredentialsForProvider("notion"); id != "global-id" || secret != "global-secret" {
		t.Fatalf("partial env creds = %q/%q, want global fallback", id, secret)
	}
}
