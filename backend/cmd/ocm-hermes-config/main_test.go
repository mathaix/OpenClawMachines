package main

import (
	"strings"
	"testing"
)

func TestMergeHermesConfigReplacesManagedKeysAndPreservesUserKeys(t *testing.T) {
	existing := []byte(`
model:
  default: old/model
platforms:
  api_server:
    enabled: false
  telegram:
    enabled: true
  matrix:
    enabled: true
custom:
  keep: true
`)
	desired := []byte(`
model:
  default: openai/gpt-oss-120b
  provider: custom
platforms:
  api_server:
    enabled: true
    host: 127.0.0.1
  discord:
    enabled: true
dashboard:
  host: 127.0.0.1
  port: 9119
`)

	mergedBytes, err := mergeHermesConfig(existing, desired)
	if err != nil {
		t.Fatalf("mergeHermesConfig: %v", err)
	}
	merged := string(mergedBytes)
	for _, want := range []string{
		"default: openai/gpt-oss-120b",
		"provider: custom",
		"matrix:",
		"keep: true",
		"discord:",
		"port: 9119",
	} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged config missing %q:\n%s", want, merged)
		}
	}
	if strings.Contains(merged, "old/model") {
		t.Fatalf("old managed model was preserved:\n%s", merged)
	}
	if strings.Contains(merged, "telegram:") {
		t.Fatalf("stale managed telegram platform was preserved:\n%s", merged)
	}
}

func TestMergeDotEnvUsesManagedBlockAndRemovesStaleValues(t *testing.T) {
	existing := strings.Join([]string{
		"# user env",
		"USER_SETTING=yes",
		"TELEGRAM_BOT_TOKEN=old",
		"TELEGRAM_ALLOWED_USERS=111",
		envBlockBegin,
		"DISCORD_BOT_TOKEN=old",
		envBlockEnd,
		"OPENAI_API_KEY=old",
		"",
	}, "\n")
	desired := strings.Join([]string{
		"DISCORD_BOT_TOKEN=new",
		"API_SERVER_KEY=gw-token",
	}, "\n")

	merged := mergeDotEnv(existing, desired)
	for _, want := range []string{
		"USER_SETTING=yes",
		envBlockBegin,
		"API_SERVER_KEY=gw-token",
		"DISCORD_BOT_TOKEN=new",
		envBlockEnd,
	} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged env missing %q:\n%s", want, merged)
		}
	}
	for _, stale := range []string{"TELEGRAM_BOT_TOKEN=old", "TELEGRAM_ALLOWED_USERS=111", "OPENAI_API_KEY=old", "DISCORD_BOT_TOKEN=old"} {
		if strings.Contains(merged, stale) {
			t.Fatalf("merged env preserved stale value %q:\n%s", stale, merged)
		}
	}
}

func TestSubstituteOCMPlaceholders(t *testing.T) {
	got := substituteOCMPlaceholders("__OCM_API_PROXY_BASE_URL__/nebius/v1 uses __OCM_API_PROXY_KEY__", "192.168.100.1", "nonce")
	want := "http://192.168.100.1:4000/nebius/v1 uses nonce"
	if got != want {
		t.Fatalf("substitute = %q, want %q", got, want)
	}
}
