# OpenClaw Machines Makefile
# Run `make help` to see available commands

.PHONY: help dev backend frontend test test-go test-frontend test-worker test-unit test-e2e test-playwright test-playwright-ui test-workflows typecheck test-gateway-e2e check-openclaw-version test-gateway-plugin-smoke test-openclaw-upgrade test-proxy-e2e test-integration test-integration-e2e test-integration-run test-runtime-selection-integration smoke-test build build-server build-agent build-workspace package-workspace version agent-upload rootfs build-rootfs build-openclaw upload-openclaw promote-openclaw build-upload-openclaw build-stage-openclaw setup-local-openclaw deploy deploy-all deploy-backend deploy-frontend deploy-worker validate set-snapshot snapshot-full snapshot-quick snapshot snapshot-vm-full secrets-in secrets-out logs-backend clean lint vet vuln-check shellcheck security-scan check scan-rootfs scan-backend upload-rootfs build-upload-rootfs list-rootfs show-rootfs-manifest rollback-rootfs build-worker-binary upload-worker-binary restart-workers deploy-fleet fleet-status create-worker-fleet migrate migrate-status commit commit-push ao ao-auto ao-dry-run ao-status ao-logs ao-clean build-kernel-browser-rootfs upload-kernel-browser-rootfs build-upload-kernel-browser-rootfs show-kernel-browser-rootfs-manifest

# Default target
help:
	@echo "OpenClaw Machines Development Commands"
	@echo "======================================="
	@echo ""
	@echo "Development:"
	@echo "  make dev          - Start backend and frontend (requires 2 terminals)"
	@echo "  make backend      - Start backend server (port 8080)"
	@echo "  make frontend     - Start frontend dev server (port 5173)"
	@echo "  make status       - Check if services are running"
	@echo ""
	@echo "Testing (runs anywhere):"
	@echo "  make test               - Run all tests (Go + frontend + worker)"
	@echo "  make test-go            - Run Go unit tests"
	@echo "  make test-unit          - Run Go unit tests only (alias, skips -short)"
	@echo "  make test-frontend      - Run frontend tests (Vitest)"
	@echo "  make test-worker        - Run Cloudflare Worker tests (Vitest)"
	@echo "  make test-e2e           - Run E2E smoke tests against production (curl)"
	@echo "  make test-playwright    - Run Playwright browser E2E tests"
	@echo "  make test-playwright-ui - Run Playwright tests with interactive UI"
	@echo "  make test-workflows     - Run workflow integration tests (requires Postgres)"
	@echo "  make test-gateway-plugin-smoke - Fast keyless gateway smoke (plugins/channels)"
	@echo "  make test-openclaw-upgrade - Run plugin/channel upgrade matrix checks"
	@echo "  make typecheck          - Run TypeScript type checking"
	@echo ""
	@echo "Testing (requires KVM host + root):"
	@echo "  make test-integration           - Run Firecracker integration tests"
	@echo "  make test-integration-e2e       - Run integration tests with Cloudflare tunnel"
	@echo "  make test-integration-run TEST=X - Run a single integration test by name"
	@echo "  make test-runtime-selection-integration - Run Firecracker runtime-selection init tests"
	@echo "  make smoke-test                 - Boot 1 VM + verify gateway (pre-upload gate)"
	@echo "  make test-rootfs               - Verify rootfs image (plugins, binaries, paths)"
	@echo ""
	@echo "Building:"
	@echo "  make build              - Build all binaries (server + agent + authproxy)"
	@echo "  make build-server       - Build backend server binary"
	@echo "  make build-agent        - Build agent binary for Linux"
	@echo "  make build-authproxy    - Build auth proxy binary for Linux"
	@echo "  make build-ocm-secrets  - Build ocm-secrets binary for Linux"
	@echo "  make update-openclaw    - Build OpenClaw artifact (OPENCLAW_VERSION=2026.x.y)"
	@echo "  make setup-local-openclaw - Rebuild local openclaw checkout used by gateway E2E (CLI + UI)"
	@echo "  make build-rootfs       - Build rootfs: binaries + ext4 image"
	@echo "  make build-openclaw     - Build OpenClaw runtime artifact (.tar.zst)"
	@echo "  make upload-rootfs      - Compress + upload rootfs to GCS + register in DB"
	@echo "  make register-rootfs    - Register rootfs release in DB (ROOTFS_RELEASE=version)"
	@echo "  make upload-openclaw    - Upload OpenClaw runtime artifact to GCS + register in DB unless staged"
	@echo "  make register-openclaw  - Register OpenClaw release in DB (OPENCLAW_RELEASE=v2026.x.y-rN)"
	@echo "  make promote-openclaw   - Promote a staged OpenClaw release to an explicit channel"
	@echo "  make build-upload-rootfs - Build rootfs then upload to GCS"
	@echo "  make build-upload-openclaw - Build OpenClaw runtime artifact then upload it"
	@echo "  make build-stage-openclaw - Build + stage OpenClaw artifact without promoting"
	@echo "  make agent-upload       - Build and upload agent to VM (VM=ocm)"
	@echo ""
	@echo "Deployment (GCP + Cloudflare):"
	@echo "  make validate         - Pre-deployment validation (git clean, snapshot exists)"
	@echo "  make deploy-all       - Deploy backend + frontend + worker (runs check + validate)"
	@echo "  make deploy-backend   - Deploy backend only (runs check + validate)"
	@echo "  make deploy-backend SKIP_CHECK=1 - Deploy without quality checks"
	@echo "  make deploy-frontend  - Deploy frontend only"
	@echo "  make deploy-worker    - Deploy Cloudflare Worker only"
	@echo "  make set-snapshot     - Show/set snapshot (NAME=xxx to set)"
	@echo "  make logs-backend     - Tail Cloud Run backend logs"
	@echo ""
	@echo "Snapshots (Cloud Build):"
	@echo "  make snapshot-full   - Full build: rootfs + agent + deploy"
	@echo "  make snapshot-quick  - Quick build: agent only + deploy"
	@echo ""
	@echo "Snapshots (Manual from VM):"
	@echo "  make snapshot VM=ocm         - Agent-only snapshot + deploy"
	@echo "  make snapshot-vm-full VM=ocm - Full rootfs + agent + deploy"
	@echo ""
	@echo "Secrets:"
	@echo "  make secrets-in   - Push .env to GCP Secret Manager (ENV_FILE=path)"
	@echo "  make secrets-out  - Pull GCP secrets to stdout (.env format)"
	@echo ""
	@echo "Quality & Security:"
	@echo "  make check          - Run all checks (lint + vet + vuln + shellcheck)"
	@echo "  make lint           - Run golangci-lint"
	@echo "  make vet            - Run go vet"
	@echo "  make vuln-check     - Run govulncheck (known vulnerabilities)"
	@echo "  make shellcheck     - Run shellcheck on shell scripts"
	@echo "  make security-scan  - Run trufflehog (secrets in git history)"
	@echo "  make scan-rootfs    - Scan rootfs Docker image for vulnerabilities (trivy)"
	@echo "  make scan-backend   - Scan backend Docker image for vulnerabilities (trivy)"
	@echo ""
	@echo "Rootfs Distribution (GCS):"
	@echo "  make publish-rootfs VM=ocm  - Build + upload rootfs to GCS (orchestrates from Mac)"
	@echo "  make list-rootfs            - List available rootfs versions in GCS"
	@echo "  make show-rootfs-manifest   - Show current rootfs manifest"
	@echo "  make show-browser-rootfs-manifest - Show current browser rootfs manifest"
	@echo "  make show-kernel-browser-rootfs-manifest - Show current Kernel browser rootfs manifest"
	@echo "  make rollback-rootfs VERSION=x - Rollback to a specific rootfs version"
	@echo ""
	@echo "Agent Distribution (GCS):"
	@echo "  make upload-agent           - Build + upload agent to GCS (self-update)"
	@echo "  make list-agent             - List available agent versions in GCS"
	@echo "  make show-agent-manifest    - Show current agent manifest"
	@echo "  make rollback-agent VERSION=x - Rollback to a specific agent version"
	@echo ""
	@echo "CLI Distribution (GCS):"
	@echo "  make upload-cli             - Build + upload CLI to GCS (multi-platform)"
	@echo ""
	@echo "Build All:"
	@echo "  make build-components       - Build + upload all components to GCS"
	@echo ""
	@echo "Debugging:"
	@echo "  make debug              - Run all diagnostics (hosts + logs + schema)"
	@echo "  make debug-hosts        - Compare DB host IPs vs GCE reality"
	@echo "  make debug-logs         - Show recent backend errors (last 1h)"
	@echo "  make debug-schema       - Check for missing DB tables/columns"
	@echo "  make debug-agent        - Agent health detail (VM=name, auto-detects)"
	@echo "  make debug-ssh          - Diagnose SSH connectivity (DNS, tunnel, certs)"
	@echo "  make debug-ssh MACHINE=x - Diagnose SSH for a specific machine"
	@echo ""
	@echo "3rd-Party Hosts (OVH, Hetzner, etc.):"
	@echo "  Named hosts: east (VA), west (OR) — use HOST=name or HOST_IP=x.x.x.x"
	@echo "  make ssh-east / ssh-west                 - SSH into named host"
	@echo "  make status-east / status-west           - Check named host health"
	@echo "  make logs-east / logs-west               - Tail named host agent logs"
	@echo "  make status-all                          - Check all hosts at once"
	@echo "  make ssh-host HOST=east                  - SSH into host (by name)"
	@echo "  make host-status HOST=east               - Check host health + versions"
	@echo "  make host-logs HOST=east                 - Tail agent logs (live)"
	@echo "  make provision-host HOST_IP=x.x.x.x     - Install system deps (new host)"
	@echo "  make enroll-host HOST_IP=x TOKEN=y       - Register host with control plane"
	@echo "  make deploy-agent-host HOST=east         - Build + deploy agent binary"
	@echo "  make setup-host HOST_IP=x TOKEN=y        - All-in-one: provision + enroll + deploy"
	@echo ""
	@echo "DBOS Worker Fleet (GCE Spot):"
	@echo "  make build-worker-binary    - Build backend binary for Linux"
	@echo "  make upload-worker-binary   - Build + upload worker binary to GCS"
	@echo "  make restart-workers        - Rolling restart of worker fleet"
	@echo "  make deploy-fleet           - Full deploy: build + upload + restart"
	@echo "  make fleet-status           - Check worker fleet instances and manifest"
	@echo "  make create-worker-fleet    - One-time: create health check + MIG"
	@echo ""
	@echo "Database:"
	@echo "  make migrate        - Apply pending SQL migrations (fetches DB URL from GCP)"
	@echo "  make migrate-status - Show current migration version"
	@echo ""
	@echo "Verification:"
	@echo "  make verify-deploy      - Check hosts are running latest agent/rootfs"
	@echo "  make verify-deploy-wait - Poll until all hosts updated (up to 5min)"
	@echo ""
	@echo "Agent Orchestrator (parallel Claude Code agents):"
	@echo "  make ao ISSUES=\"39 40\"  - Launch agents on specific issues"
	@echo "  make ao-auto            - Auto-pick unassigned code-only issues"
	@echo "  make ao-dry-run ISSUES= - Preview without launching"
	@echo "  make ao-status          - Check running agents"
	@echo "  make ao-logs ISSUE=39   - Tail log for an issue"
	@echo "  make ao-clean           - Clean up finished worktrees"
	@echo ""
	@echo "Utilities:"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make version      - Show version info"

# ============================================================================
# Development
# ============================================================================

dev:
	@echo "Start backend and frontend in separate terminals:"
	@echo "  Terminal 1: make backend"
	@echo "  Terminal 2: make frontend"

backend:
	cd backend && go run ./cmd/server/

frontend:
	cd frontend && npm run dev

status:
	@echo "Checking services..."
	@curl -s http://localhost:8080/health 2>/dev/null && echo " Backend OK" || echo "Backend: not running"
	@curl -s http://localhost:5173 >/dev/null 2>&1 && echo "Frontend: OK" || echo "Frontend: not running"

# ============================================================================
# Testing
# ============================================================================

test: test-go test-frontend test-worker

test-go:
	cd backend && go test ./...

test-unit:
	cd backend && go test -short ./...

test-frontend:
	cd frontend && npm run test

test-worker:
	cd worker && npm run test

# Gateway E2E test: OpenClaw gateway (subprocess) + metadata server + API proxy
# Requires: a local openclaw build reachable on PATH as `openclaw` whose
#   version matches the pin in versions.json.
#   Run `make setup-local-openclaw` once (and after any openclaw upgrade) to
#   install deps, build the CLI/runtime, and build the Control UI assets.
#   The target precheck below fails fast if the resolved binary doesn't
#   match versions.json; override with OPENCLAW_BIN=/path or
#   OPENCLAW_SKIP_VERSION_CHECK=1 for intentional cross-version runs.
# Requires at least one of: E2E_ANTHROPIC_API_KEY, E2E_ANTHROPIC_SUBSCRIPTION_KEY,
#   E2E_OPENAI_API_KEY, E2E_OPENAI_SUBSCRIPTION_KEY
# Keys are auto-pulled from GCP Secret Manager if not set in env.
test-gateway-e2e: check-openclaw-version
	@echo "=========================================="
	@echo "Gateway E2E Test (subprocess + real API calls)"
	@echo "=========================================="
	cd backend && \
		E2E_ANTHROPIC_API_KEY=$${E2E_ANTHROPIC_API_KEY:-$$(gcloud secrets versions access latest --secret=E2E_ANTHROPIC_API_KEY --project=$(GCP_PROJECT) 2>/dev/null)} \
		E2E_ANTHROPIC_SUBSCRIPTION_KEY=$${E2E_ANTHROPIC_SUBSCRIPTION_KEY:-$$(gcloud secrets versions access latest --secret=E2E_ANTHROPIC_SUBSCRIPTION_KEY --project=$(GCP_PROJECT) 2>/dev/null)} \
		E2E_OPENAI_API_KEY=$${E2E_OPENAI_API_KEY:-$$(gcloud secrets versions access latest --secret=E2E_OPENAI_API_KEY --project=$(GCP_PROJECT) 2>/dev/null)} \
		E2E_OPENAI_SUBSCRIPTION_KEY=$${E2E_OPENAI_SUBSCRIPTION_KEY:-$$(gcloud secrets versions access latest --secret=E2E_OPENAI_SUBSCRIPTION_KEY --project=$(GCP_PROJECT) 2>/dev/null)} \
		go test -v -tags "linux e2e" -timeout 5m ./internal/gatewaye2e/...

# Precheck: verify the openclaw binary that the e2e tests will use matches
# the version pin in versions.json. Skippable for intentional overrides.
check-openclaw-version:
	@bash scripts/check-openclaw-version.sh

# Fast keyless gateway smoke focused on plugin + channel config stability.
# Optional overrides:
#   OPENCLAW_BIN=/path/to/openclaw
#   E2E_REQUIRE_OPIK=1  (fail if opik plugin cannot be installed in test env)
test-gateway-plugin-smoke:
	@echo "=========================================="
	@echo "Gateway Plugin/Channel Smoke Test"
	@echo "=========================================="
	cd backend && \
		E2E_ALLOW_NO_LLM_KEYS=1 \
		OPENCLAW_BIN="$${OPENCLAW_BIN:-openclaw}" \
		E2E_REQUIRE_OPIK="$${E2E_REQUIRE_OPIK:-0}" \
		go test -v -tags "linux e2e" -timeout 5m \
		-count=1 \
		-run 'TestGatewayE2E_(Startup|ControlUI|WebSocketHandshake|WebSocketHandshake_NonUIClient|FileConfig|ConfigPush_AddChannel|ConfigPush_AddPlugin|ConfigPush_FullConfigPush|OpikTracing)' \
		./internal/gatewaye2e/...

# Validate OpenClaw upgrade compatibility for plugin/channel behavior.
# Usage:
#   make test-openclaw-upgrade                    # uses versions.json pinned version
#   make test-openclaw-upgrade VERSION=2026.3.28
#   make test-openclaw-upgrade VERSIONS="2026.3.28 2026.3.29"
test-openclaw-upgrade:
	@if [ -n "$(VERSIONS)" ]; then \
		bash scripts/test-openclaw-upgrade.sh $(VERSIONS); \
	elif [ -n "$(filter command line environment environment override,$(origin VERSION))" ] && [ -n "$(VERSION)" ]; then \
		bash scripts/test-openclaw-upgrade.sh $(VERSION); \
	else \
		bash scripts/test-openclaw-upgrade.sh; \
	fi

# Verify rootfs Docker image has expected plugins, binaries, and paths
test-rootfs:
	@echo "=========================================="
	@echo "Rootfs Image Verification"
	@echo "=========================================="
	bash scripts/test-rootfs.sh ocm-rootfs

# Proxy-only E2E test: real API calls through proxy handler (no Docker)
# Requires: ANTHROPIC_API_KEY (optional: OPENAI_API_KEY)
test-proxy-e2e:
	@echo "=========================================="
	@echo "Proxy E2E Test (real API calls, no Docker)"
	@echo "=========================================="
	cd backend && ANTHROPIC_API_KEY=$(ANTHROPIC_API_KEY) OPENAI_API_KEY=$(OPENAI_API_KEY) \
		go test -v -tags e2e -timeout 2m ./internal/apiproxy/...

test-workflows:
	@echo "=========================================="
	@echo "Workflow Integration Tests (requires Postgres)"
	@echo "=========================================="
	cd backend && go test -v -tags integration_db -timeout 5m ./internal/workflows/...

test-playwright:
	cd frontend && npx playwright test

test-playwright-ui:
	cd frontend && npx playwright test --ui

typecheck:
	cd frontend && npx tsc --noEmit

# E2E smoke tests against production
PROD_DOMAIN ?= openclawmachines.com
PROD_API    ?= api.$(PROD_DOMAIN)
E2E_PASS    := \033[0;32mPASS\033[0m
E2E_FAIL    := \033[0;31mFAIL\033[0m
test-e2e:
	@echo "=========================================="
	@echo "E2E Smoke Tests — $(PROD_DOMAIN)"
	@echo "=========================================="
	@FAILED=0; \
	\
	echo ""; \
	echo "--- Health Checks ---"; \
	\
	WORKER_VER=$$(curl -sf https://$(PROD_DOMAIN)/__version | python3 -c "import json,sys; print(json.load(sys.stdin)['version'])" 2>/dev/null); \
	if [ -n "$$WORKER_VER" ]; then \
		printf "  $(E2E_PASS) Worker health ($$WORKER_VER)\n"; \
	else \
		printf "  $(E2E_FAIL) Worker health — no response\n"; FAILED=1; \
	fi; \
	\
	BACKEND_VER=$$(curl -sf https://$(PROD_API)/health | python3 -c "import json,sys; print(json.load(sys.stdin)['version'])" 2>/dev/null); \
	if [ -n "$$BACKEND_VER" ]; then \
		printf "  $(E2E_PASS) Backend health ($$BACKEND_VER)\n"; \
	else \
		printf "  $(E2E_FAIL) Backend health — no response\n"; FAILED=1; \
	fi; \
	\
	echo ""; \
	echo "--- CORS: Subdomain Routing ---"; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" -X OPTIONS \
		https://e2etest.$(PROD_DOMAIN)/testmachine/health \
		-H "Origin: https://$(PROD_DOMAIN)" \
		-H "Access-Control-Request-Method: GET"); \
	if [ "$$STATUS" = "204" ]; then \
		printf "  $(E2E_PASS) CORS preflight: https:// origin → 204\n"; \
	else \
		printf "  $(E2E_FAIL) CORS preflight: https:// origin → $$STATUS (expected 204)\n"; FAILED=1; \
	fi; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" -X OPTIONS \
		https://e2etest.$(PROD_DOMAIN)/testmachine/health \
		-H "Origin: http://$(PROD_DOMAIN)" \
		-H "Access-Control-Request-Method: GET"); \
	if [ "$$STATUS" = "403" ]; then \
		printf "  $(E2E_PASS) CORS preflight: http:// origin → 403 (rejected)\n"; \
	else \
		printf "  $(E2E_FAIL) CORS preflight: http:// origin → $$STATUS (expected 403)\n"; FAILED=1; \
	fi; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" -X OPTIONS \
		https://e2etest.$(PROD_DOMAIN)/testmachine/health \
		-H "Origin: https://evil.com" \
		-H "Access-Control-Request-Method: GET"); \
	if [ "$$STATUS" = "403" ]; then \
		printf "  $(E2E_PASS) CORS preflight: external origin → 403 (rejected)\n"; \
	else \
		printf "  $(E2E_FAIL) CORS preflight: external origin → $$STATUS (expected 403)\n"; FAILED=1; \
	fi; \
	\
	echo ""; \
	echo "--- CORS: Error Responses ---"; \
	\
	CORS_HDR=$$(curl -sf -D - -o /dev/null \
		https://e2etest.$(PROD_DOMAIN)/testmachine/files \
		-H "Origin: https://$(PROD_DOMAIN)" 2>&1 | grep -ci "access-control-allow-origin"); \
	if [ "$$CORS_HDR" -ge 1 ]; then \
		printf "  $(E2E_PASS) 401 response includes CORS headers\n"; \
	else \
		printf "  $(E2E_FAIL) 401 response missing CORS headers\n"; FAILED=1; \
	fi; \
	\
	echo ""; \
	echo "--- Auth ---"; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" \
		https://$(PROD_API)/api/auth/me \
		-H "Authorization: Bearer invalid-token"); \
	if [ "$$STATUS" = "401" ]; then \
		printf "  $(E2E_PASS) Invalid JWT rejected → 401\n"; \
	else \
		printf "  $(E2E_FAIL) Invalid JWT → $$STATUS (expected 401)\n"; FAILED=1; \
	fi; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" \
		https://$(PROD_API)/api/auth/providers); \
	if [ "$$STATUS" = "200" ]; then \
		printf "  $(E2E_PASS) Auth providers endpoint → 200\n"; \
	else \
		printf "  $(E2E_FAIL) Auth providers endpoint → $$STATUS (expected 200)\n"; FAILED=1; \
	fi; \
	\
	echo ""; \
	echo "--- Static Routing ---"; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" https://$(PROD_DOMAIN)/); \
	if [ "$$STATUS" = "200" ]; then \
		printf "  $(E2E_PASS) Frontend serves → 200\n"; \
	else \
		printf "  $(E2E_FAIL) Frontend → $$STATUS (expected 200)\n"; FAILED=1; \
	fi; \
	\
	STATUS=$$(curl -sf -o /dev/null -w "%{http_code}" https://www.$(PROD_DOMAIN)/); \
	if [ "$$STATUS" = "200" ]; then \
		printf "  $(E2E_PASS) www redirect/serve → 200\n"; \
	else \
		printf "  $(E2E_FAIL) www → $$STATUS (expected 200)\n"; FAILED=1; \
	fi; \
	\
	echo ""; \
	echo "=========================================="; \
	if [ "$$FAILED" -eq 0 ]; then \
		printf "$(E2E_PASS) All E2E tests passed\n"; \
	else \
		printf "$(E2E_FAIL) Some E2E tests failed\n"; \
		exit 1; \
	fi

# Clean up stale resources from crashed/interrupted integration tests.
# Kills leftover processes, removes bridges/taps, frees port 80.
test-cleanup:
	@sudo scripts/test-cleanup.sh

# Run integration tests: functional service tests (no tunnel required)
# Requires: KVM, firecracker, root. Must run on a Linux host with nested virt.
# Note: full suite takes ~35min due to sequential VM boots (~60s gateway startup each)
# See docs/TESTING.md for details.
# Runs cleanup automatically before and after (even on failure).
test-integration:
	@echo "=========================================="
	@echo "Integration Tests (local, no tunnel)"
	@echo "=========================================="
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && sudo env "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$$PATH" \
		"GOCACHE=$$(/usr/local/go/bin/go env GOCACHE)" "GOPATH=$$(/usr/local/go/bin/go env GOPATH)" "HOME=$$HOME" \
		"TEST_OPENCLAW_MANIFEST_URI=$$TEST_OPENCLAW_MANIFEST_URI" \
		go test -v -tags integration -timeout 45m ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

# Run full E2E integration tests including Cloudflare tunnel
# Requires: CF_API_TOKEN, CF_ACCOUNT_ID, CF_ZONE_ID, cloudflared binary
test-integration-e2e:
	@echo "=========================================="
	@echo "E2E Integration Tests (with Cloudflare tunnel)"
	@echo "=========================================="
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && sudo env "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$$PATH" \
		"GOCACHE=$$(/usr/local/go/bin/go env GOCACHE)" "GOPATH=$$(/usr/local/go/bin/go env GOPATH)" "HOME=$$HOME" \
		go test -v -tags integration -timeout 20m \
		-run 'TestTunnel' ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

# Run a specific integration test by name
# Usage: make test-integration-run TEST=TestGateway_Health
test-integration-run:
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && sudo env "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$$PATH" \
		"GOCACHE=$$(/usr/local/go/bin/go env GOCACHE)" "GOPATH=$$(/usr/local/go/bin/go env GOPATH)" "HOME=$$HOME" \
		go test -v -tags integration -timeout 25m \
		-run '$(TEST)' ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

test-runtime-selection-integration:
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && sudo env "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$$PATH" \
		"GOCACHE=$$(/usr/local/go/bin/go env GOCACHE)" "GOPATH=$$(/usr/local/go/bin/go env GOPATH)" "HOME=$$HOME" \
		go test -v -tags integration -timeout 60m \
		-run 'TestInit_RuntimeSelection(AutoFallsBackToBaked|ArtifactMissingBinaryFailsPreBoot|ArtifactUsesStagedRuntime|ArtifactServesGatewayChatCompletions|ArtifactUpgradeSwitchesVersions|ArtifactRollbackRestoresPreviousVersionAfterFailedUpgrade)' ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

# Smoke test: boot 1 VM with local rootfs, verify gateway health.
# Also runs the artifact runtime smoke test (GCS download → ext4 image → VM boot)
# to catch packaging and sizing issues before upload.
# Use after build-rootfs to validate before uploading to GCS.
smoke-test:
	@echo "=========================================="
	@echo "Smoke Test — boot VM + verify gateway + artifact runtime"
	@echo "=========================================="
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && sudo env "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$$PATH" \
		"GOCACHE=$$(/usr/local/go/bin/go env GOCACHE)" "GOPATH=$$(/usr/local/go/bin/go env GOPATH)" "HOME=$$HOME" \
		go test -v -tags integration -timeout 15m \
		-run 'TestGatewaySuite|TestSmokeArtifactRuntime|TestSmokeOcmptyd' ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

# Quick artifact test: extract locally, verify openclaw --version and gateway starts
# No VM needed — runs on the host in seconds
test-openclaw-artifact:
	@echo "=========================================="
	@echo "Testing OpenClaw artifact locally"
	@echo "=========================================="
	@ARTIFACT=$$(ls -t /var/lib/ocm/openclaw-artifacts/openclaw-*.tar.zst 2>/dev/null | head -1); \
	if [ -z "$$ARTIFACT" ]; then echo "ERROR: no artifact found. Run: make build-openclaw"; exit 1; fi; \
	echo "Artifact: $$ARTIFACT"; \
	TMP=$$(mktemp -d); trap "rm -rf $$TMP" EXIT; \
	tar --zstd -xf "$$ARTIFACT" -C "$$TMP" && \
	echo "1. Extracted OK" && \
	VERSION=$$($$TMP/bin/openclaw --version 2>&1) && \
	echo "2. Version: $$VERSION" && \
	OPENCLAW_BUNDLED_PLUGINS_DIR="$$TMP/node_modules/openclaw/dist/extensions" \
	timeout 15 $$TMP/bin/openclaw gateway --port 19999 --bind loopback --allow-unconfigured > "$$TMP/gw.log" 2>&1 || true; \
	if grep -q 'listening on' "$$TMP/gw.log"; then \
		echo "3. Gateway started OK"; \
		echo "========================================"; \
		echo "PASS"; \
	else \
		echo "3. Gateway FAILED to start:"; \
		cat "$$TMP/gw.log" | tail -10; \
		echo "========================================"; \
		echo "FAIL"; exit 1; \
	fi

# Smoke test: boot 1 VM with real GCS artifact, verify gateway starts
# Catches packaging issues (hard links, broken deps) that synthetic test fixtures miss
smoke-test-artifact:
	@echo "=========================================="
	@echo "Smoke Test — real artifact from GCS"
	@echo "=========================================="
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && sudo env "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$$PATH" \
		"GOCACHE=$$(/usr/local/go/bin/go env GOCACHE)" "GOPATH=$$(/usr/local/go/bin/go env GOPATH)" "HOME=$$HOME" \
		go test -v -tags integration -timeout 15m \
		-run 'TestSmokeArtifactRuntime' ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

# ============================================================================
# Building
# ============================================================================

# Version info from git
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y%m%dT%H%M%SZ)
VERSION := $(GIT_COMMIT)-$(BUILD_TIME)
LDFLAGS := -X 'github.com/mathaix/openclawmachines/backend/pkg/version.Version=$(VERSION)' \
           -X 'github.com/mathaix/openclawmachines/backend/pkg/version.GitCommit=$(GIT_COMMIT)' \
           -X 'github.com/mathaix/openclawmachines/backend/pkg/version.BuildTime=$(BUILD_TIME)'

# Build all binaries
build: build-server build-agent build-authproxy build-ocmptyd
	@echo "Built: backend/server, backend/agent-linux, backend/authproxy, backend/ocmptyd"

# Build server binary (for local dev or Cloud Run)
build-server:
	cd backend && go build -ldflags "$(LDFLAGS)" -o server ./cmd/server/
	@echo "Built: backend/server ($(VERSION))"

# Build agent binary for Linux (for GCP VMs)
build-agent:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-w -s $(LDFLAGS)" -o agent-linux ./cmd/agent/
	@echo "Built: backend/agent-linux ($(VERSION))"

# Build auth proxy binary for Linux (for MicroVM rootfs)
build-authproxy:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-w -s $(LDFLAGS)" -o authproxy ./cmd/authproxy/
	@echo "Built: backend/authproxy ($(VERSION))"

# Build dedicated PTY daemon for Linux (for MicroVM rootfs)
build-ocmptyd:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-w -s $(LDFLAGS)" -o ocmptyd ./cmd/ocmptyd/
	@echo "Built: backend/ocmptyd ($(VERSION))"

# Build OpenClaw artifact with a specific version.
# Usage: make update-openclaw OPENCLAW_VERSION=2026.4.2
update-openclaw:
	@test -n "$(OPENCLAW_VERSION)" || { echo "Usage: make update-openclaw OPENCLAW_VERSION=2026.4.2"; exit 1; }
	OPENCLAW_VERSION=$(OPENCLAW_VERSION) $(MAKE) build-openclaw

# Rebuild the local openclaw checkout used by gateway E2E tests.
# Assumes /usr/local/bin/openclaw is a symlink into $(OPENCLAW_LOCAL_DIR).
# Installs deps, builds the CLI/runtime bundle, then builds the Control UI
# bundle (required by TestGatewayE2E_ControlUI; serves /).
OPENCLAW_LOCAL_DIR ?= $(HOME)/openclaw
setup-local-openclaw:
	@test -d "$(OPENCLAW_LOCAL_DIR)" || { echo "openclaw checkout not found at $(OPENCLAW_LOCAL_DIR) (override with OPENCLAW_LOCAL_DIR=...)"; exit 1; }
	cd "$(OPENCLAW_LOCAL_DIR)" && pnpm install --frozen-lockfile
	cd "$(OPENCLAW_LOCAL_DIR)" && pnpm build
	cd "$(OPENCLAW_LOCAL_DIR)" && pnpm ui:build
	@/usr/local/bin/openclaw --version

# Build rootfs: compile binaries, create ext4 image
# Requires: Docker, mkfs.ext4, bsdtar, sudo, pnpm
build-ocm-secrets:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-w -s" -o ocm-secrets ./cmd/ocm-secrets/
	@echo "Built: backend/ocm-secrets"

build-rootfs: build-agent build-authproxy build-ocmptyd build-ocm-secrets
	sudo cp backend/authproxy /usr/local/bin/authproxy && sudo chmod +x /usr/local/bin/authproxy
	sudo cp backend/ocmptyd /usr/local/bin/ocmptyd && sudo chmod +x /usr/local/bin/ocmptyd
	sudo cp backend/ocm-secrets /usr/local/bin/ocm-secrets && sudo chmod +x /usr/local/bin/ocm-secrets
	sudo OCM_SNAPSHOT=$(GIT_COMMIT) bash scripts/build-rootfs.sh
	@echo ""
	@echo "Rootfs built: /var/lib/ocm/images/rootfs.ext4"

build-openclaw: build-opik-plugin
	bash scripts/build-openclaw-runtime.sh
	@echo ""
	@echo "OpenClaw runtime artifact built: /var/lib/ocm/openclaw-artifacts"

# Build workspace server
build-workspace:
	cd workspace/server && npm ci && npm run build
	@echo "Built: workspace/server"

# Package workspace for upload to VM
PACKAGE_DIR ?= $(shell if [ -d /workspace ]; then echo /workspace; else echo /tmp; fi)
package-workspace:
	tar -czf $(PACKAGE_DIR)/workspace.tar.gz -C . rootfs/ scripts/init-openclaw.sh scripts/build-rootfs.sh scripts/ocm-metadata scripts/ocm-test-llm scripts/ocm-env Makefile
	@echo "Created: $(PACKAGE_DIR)/workspace.tar.gz"

# Print version info
version:
	@echo "VERSION=$(VERSION)"
	@echo "GIT_COMMIT=$(GIT_COMMIT)"
	@echo "BUILD_TIME=$(BUILD_TIME)"

# Upload agent to a VM (usage: make agent-upload VM=ocm)
VM ?= ocm
ZONE ?= us-central1-b
agent-upload: build-agent
	@echo "Uploading agent to $(VM)..."
	gcloud compute scp backend/agent-linux $(VM):/tmp/agent --zone=$(ZONE)
	gcloud compute ssh $(VM) --zone=$(ZONE) --command='\
		sudo systemctl stop ocm-agent || true && \
		sudo mv /tmp/agent /usr/local/bin/agent && \
		sudo chmod +x /usr/local/bin/agent && \
		/usr/local/bin/agent --version && \
		sudo systemctl start ocm-agent'
	@echo "Agent uploaded and restarted on $(VM)"

# ============================================================================
# Quality & Security
# ============================================================================

# Run all checks (used as pre-deploy gate)
# Override with SKIP_CHECK=1 to bypass during deploy
check: vet lint vuln-check shellcheck

GOBIN := $(shell /usr/local/go/bin/go env GOPATH)/bin

# Go linter (auto-installs if missing)
lint:
	@test -x $(GOBIN)/golangci-lint || { echo "Installing golangci-lint..."; go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; }
	cd backend && $(GOBIN)/golangci-lint run --timeout 5m ./...

# Go vet (built-in)
vet:
	cd backend && go vet ./...

# Known vulnerability check (auto-installs if missing)
vuln-check:
	@test -x $(GOBIN)/govulncheck || { echo "Installing govulncheck..."; go install golang.org/x/vuln/cmd/govulncheck@latest; }
	cd backend && $(GOBIN)/govulncheck ./...

# Shell script linting (install: apt-get install shellcheck / brew install shellcheck)
shellcheck:
	@command -v shellcheck >/dev/null 2>&1 || { echo "ERROR: shellcheck not installed. Install via: apt-get install shellcheck"; exit 1; }
	shellcheck --severity=error scripts/*.sh

# Secrets scanner — checks git history for leaked secrets
# (install: brew install trufflehog / go install github.com/trufflesecurity/trufflehog/v3@latest)
security-scan:
	@command -v trufflehog >/dev/null 2>&1 || { echo "ERROR: trufflehog not installed. See: https://github.com/trufflesecurity/trufflehog#installation"; exit 1; }
	trufflehog git file://. --only-verified

# Scan rootfs Docker image for OS and dependency vulnerabilities
# Builds the image first, then scans with Trivy (install: brew install trivy)
scan-rootfs:
	@command -v trivy >/dev/null 2>&1 || { echo "ERROR: trivy not installed. Install via: brew install trivy"; exit 1; }
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not installed"; exit 1; }
	@echo "Building rootfs image..."
	docker build -t ocm-rootfs -f rootfs/Dockerfile.openclaw rootfs/
	@echo ""
	@echo "Scanning rootfs image for vulnerabilities (HIGH + CRITICAL)..."
	trivy image --severity HIGH,CRITICAL ocm-rootfs

# Scan backend Docker image for vulnerabilities
scan-backend:
	@command -v trivy >/dev/null 2>&1 || { echo "ERROR: trivy not installed. Install via: brew install trivy"; exit 1; }
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not installed"; exit 1; }
	@echo "Building backend image..."
	docker build -t ocm-backend -f backend/Dockerfile backend/
	@echo ""
	@echo "Scanning backend image for vulnerabilities (HIGH + CRITICAL)..."
	trivy image --severity HIGH,CRITICAL ocm-backend

# ============================================================================
# Deployment
# ============================================================================

# Read snapshot from .snapshot file
SNAPSHOT ?= $(shell cat .snapshot 2>/dev/null || echo "ocm-snapshot-initial")

# GCP configuration
GCP_PROJECT ?= clarateach
GCP_REGION ?= us-central1

# Secrets referenced by deploy-cloud-run.sh --set-secrets
DEPLOY_SECRETS := OCM_DATABASE_URL OCM_FC_AGENT_TOKEN OCM_WORKSPACE_TOKEN_SECRET OCM_JWT_SECRET \
                  OCM_SECRET_ENCRYPTION_KEY \
                  OCM_GOOGLE_CLIENT_ID OCM_GOOGLE_CLIENT_SECRET OCM_GITHUB_CLIENT_ID OCM_GITHUB_CLIENT_SECRET \
                  OCM_CLOUDFLARE_API_TOKEN OCM_CLOUDFLARE_ACCOUNT_ID OCM_CLOUDFLARE_ZONE_ID \
                  OCM_CLOUDFLARE_KV_NAMESPACE_ID OCM_CF_SERVICE_TOKEN_ID OCM_CF_SERVICE_TOKEN_SECRET \
                  OCM_CLOUDFLARE_ACCESS_TOKEN OCM_GCS_SERVICE_ACCOUNT_KEY OPIK_OBSERVE_KEY \
                  COMPOSIO_CONSUMER_KEY

# Ensure all secrets exist in GCP Secret Manager and Cloud Run SA can access them
ensure-secrets:
	@SA=$$(gcloud projects describe $(GCP_PROJECT) --format='value(projectNumber)')-compute@developer.gserviceaccount.com; \
	for name in $(DEPLOY_SECRETS); do \
		if ! gcloud secrets describe "$$name" --project=$(GCP_PROJECT) >/dev/null 2>&1; then \
			echo "Creating secret $$name (placeholder)..."; \
			printf "placeholder" | gcloud secrets create "$$name" --project=$(GCP_PROJECT) --data-file=- >/dev/null 2>&1; \
		fi; \
		gcloud secrets add-iam-policy-binding "$$name" \
			--project=$(GCP_PROJECT) \
			--member="serviceAccount:$$SA" \
			--role="roles/secretmanager.secretAccessor" \
			--condition=None \
			--quiet >/dev/null 2>&1 && \
		echo "  $$name — OK" || echo "  $$name — binding already exists"; \
	done

# Pre-deployment validation (runs quality checks first unless SKIP_CHECK=1)
validate: $(if $(SKIP_CHECK),,check) ensure-secrets
	@echo "=========================================="
	@echo "Pre-deployment Validation"
	@echo "=========================================="
	@echo "Version:  $(VERSION)"
	@echo "Commit:   $(GIT_COMMIT)"
	@echo "Snapshot: $(SNAPSHOT)"
	@echo "=========================================="
	@echo ""
	@echo "1. Checking git status..."
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "ERROR: Uncommitted changes detected!"; \
		echo "Please commit all changes before deploying."; \
		git status --short; \
		exit 1; \
	else \
		echo "   OK - Working directory clean"; \
	fi
	@echo ""
	@echo "2. Checking snapshot exists..."
	@if gcloud compute snapshots describe $(SNAPSHOT) --format='value(name)' >/dev/null 2>&1; then \
		echo "   OK - Snapshot '$(SNAPSHOT)' exists"; \
	else \
		echo "ERROR: Snapshot '$(SNAPSHOT)' not found!"; \
		echo "Available snapshots:"; \
		gcloud compute snapshots list --format='value(name)' | grep -E 'ocm' | head -5; \
		exit 1; \
	fi
	@echo ""
	@echo "=========================================="
	@echo "Validation passed. Ready to deploy."
	@echo "=========================================="

# Deploy backend, frontend, and worker
deploy-all: validate deploy-worker
	@echo "Deploying backend and frontend with SNAPSHOT=$(SNAPSHOT)..."
	VIA_MAKE=1 FC_SNAPSHOT_NAME=$(SNAPSHOT) ./scripts/deploy-cloud-run.sh --all
	@echo ""
	@echo "Deployment complete."

# Deploy backend only (runs migrations first)
deploy-backend: validate migrate
	@echo "Deploying backend with SNAPSHOT=$(SNAPSHOT)..."
	VIA_MAKE=1 FC_SNAPSHOT_NAME=$(SNAPSHOT) ./scripts/deploy-cloud-run.sh --backend
	@echo ""
	@echo "Backend deployed."

# Deploy frontend only (no snapshot needed)
deploy-frontend:
	@echo "Deploying frontend..."
	VIA_MAKE=1 ./scripts/deploy-cloud-run.sh --frontend
	@echo ""
	@echo "Frontend deployed."

# Deploy Cloudflare Worker (injects git version)
deploy-worker:
	@echo "Deploying Cloudflare Worker ($(VERSION))..."
	cd worker && npx wrangler deploy --var VERSION:$(VERSION)
	@echo ""
	@echo "Worker deployed ($(VERSION)). Verify: curl -s https://openclawmachines.com/__version"

# Alias
deploy: deploy-all

# ============================================================================
# Database Migrations
# ============================================================================

# Apply pending SQL migrations (fetches DATABASE_URL from GCP Secret Manager)
migrate:
	@echo "Running database migrations..."
	scripts/run-migrations.sh --from-secret

# Show current migration version
migrate-status:
	@DATABASE_URL=$$(gcloud secrets versions access latest --secret=OCM_DATABASE_URL --project=$(GCP_PROJECT)) && \
	psql "$$DATABASE_URL" -c "SELECT version, filename, applied_at FROM schema_migrations ORDER BY version DESC LIMIT 10;" 2>/dev/null || \
	echo "No migrations table found. Run 'make migrate' first."

# ============================================================================
# DBOS Worker Fleet (GCE Spot Instances)
# ============================================================================

GCP_ZONE ?= us-central1-b

# Build worker binary for Linux
build-worker-binary:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-w -s $(LDFLAGS)" -o server-linux ./cmd/server/
	@echo "Built: backend/server-linux ($(VERSION))"

# Upload worker binary to GCS
upload-worker-binary: build-worker-binary
	@echo "Uploading worker binary to GCS..."
	scripts/upload-worker-binary.sh backend/server-linux

# Rolling restart workers (picks up new binary on boot)
restart-workers:
	@echo "Rolling restart of worker fleet..."
	gcloud compute instance-groups managed rolling-action restart ocm-workers \
		--project=$(GCP_PROJECT) --zone=$(GCP_ZONE)
	@echo "Workers restarting. Monitor: gcloud compute instance-groups managed list-instances ocm-workers --zone=$(GCP_ZONE)"

# Full worker fleet deploy: build + upload + restart
deploy-fleet: upload-worker-binary restart-workers
	@echo "Worker fleet deployed."

# Check worker fleet status
fleet-status:
	@echo "Worker fleet instances:"
	@gcloud compute instance-groups managed list-instances ocm-workers \
		--project=$(GCP_PROJECT) --zone=$(GCP_ZONE) 2>/dev/null || echo "  MIG 'ocm-workers' not found. Create with: make create-worker-fleet"
	@echo ""
	@echo "Worker manifest:"
	@gsutil cat gs://$(GCS_BUCKET)/worker/manifest.json 2>/dev/null | python3 -m json.tool || echo "  No manifest found. Upload with: make upload-worker-binary"

# One-time: create worker fleet infrastructure
create-worker-fleet:
	@echo "Creating worker fleet infrastructure..."
	@echo ""
	@echo "Step 1: Health check"
	gcloud compute health-checks create http ocm-worker-health \
		--project=$(GCP_PROJECT) \
		--port=8080 \
		--request-path=/healthz \
		--check-interval=30s \
		--timeout=10s \
		--healthy-threshold=1 \
		--unhealthy-threshold=3
	@echo ""
	@echo "Step 2: Instance template"
	@echo "  NOTE: You must set worker-manifest and database-url metadata manually."
	@echo "  See docs/designs/longjob_worker.md for the full gcloud command."
	@echo ""
	@echo "Step 3: Managed instance group"
	@echo "  gcloud compute instance-groups managed create ocm-workers \\"
	@echo "    --project=$(GCP_PROJECT) --zone=$(GCP_ZONE) \\"
	@echo "    --template=ocm-worker-v1 --size=2 \\"
	@echo "    --health-check=ocm-worker-health --initial-delay=60"

# Tail Cloud Run backend logs
logs-backend:
	gcloud run services logs tail ocm-backend --region=$(GCP_REGION)

# ============================================================================
# Snapshots (Cloud Build)
# ============================================================================

GIT_REPO ?= # TODO: set after repo creation
GIT_BRANCH ?= $(shell git branch --show-current 2>/dev/null || echo "main")
CB_REGION ?= us-west2

# Full snapshot build: rootfs + agent + deploy
snapshot-full:
	@echo "Starting full snapshot build via Cloud Build..."
	@echo "Branch: $(GIT_BRANCH)"
	gcloud builds submit $(GIT_REPO) \
		--region=$(CB_REGION) \
		--revision=$(GIT_BRANCH) \
		--config=cloudbuild.yaml \
		--substitutions=_MODE=full,_DEPLOY=true
	@echo ""
	@echo "Update .snapshot: make set-snapshot NAME=<snapshot-name-from-output>"

# Quick snapshot build: agent only
snapshot-quick:
	@echo "Starting quick snapshot build via Cloud Build..."
	@echo "Branch: $(GIT_BRANCH)"
	gcloud builds submit $(GIT_REPO) \
		--region=$(CB_REGION) \
		--revision=$(GIT_BRANCH) \
		--config=cloudbuild.yaml \
		--substitutions=_MODE=quick,_DEPLOY=true
	@echo ""
	@echo "Update .snapshot: make set-snapshot NAME=<snapshot-name-from-output>"

# Set snapshot name
set-snapshot:
ifndef NAME
	@echo "Current snapshot: $(SNAPSHOT)"
	@echo ""
	@echo "Usage: make set-snapshot NAME=<snapshot-name>"
else
	@echo "$(NAME)" > .snapshot
	@echo "Snapshot set to: $(NAME)"
endif

# ============================================================================
# Manual Snapshots (from VM)
# ============================================================================

snapshot:
	./scripts/create-snapshot.sh --vm=$(VM) --deploy

snapshot-vm-full:
	./scripts/create-snapshot.sh --vm=$(VM) --full --deploy

# ============================================================================
# Secrets
# ============================================================================

ENV_FILE ?= .env

KNOWN_SECRETS := DATABASE_URL FC_AGENT_TOKEN WORKSPACE_TOKEN_SECRET JWT_SECRET \
                 SECRET_ENCRYPTION_KEY \
                 ANTHROPIC_API_KEY GOOGLE_API_KEY OPENAI_API_KEY LITELLM_MASTER_KEY \
                 CLOUDFLARE_API_TOKEN CLOUDFLARE_ACCOUNT_ID CLOUDFLARE_ZONE_ID \
                 CLOUDFLARE_KV_NAMESPACE_ID CF_ACCESS_AUD \
                 CF_SERVICE_TOKEN_ID CF_SERVICE_TOKEN_SECRET

secrets-in:
	@if [ ! -f "$(ENV_FILE)" ]; then \
		echo "ERROR: $(ENV_FILE) not found"; \
		exit 1; \
	fi
	@echo "Pushing secrets from $(ENV_FILE) to GCP Secret Manager (project: $(GCP_PROJECT))..."
	@known="$(KNOWN_SECRETS)"; \
	while IFS= read -r line || [ -n "$$line" ]; do \
		case "$$line" in \
			''|\#*) continue ;; \
		esac; \
		key=$${line%%=*}; \
		value=$${line#*=}; \
		if [ -z "$$key" ]; then continue; fi; \
		if ! echo " $$known " | grep -q " $$key "; then \
			echo "  Skipped: $$key (not in KNOWN_SECRETS)"; \
			continue; \
		fi; \
		if gcloud secrets describe "$$key" --project=$(GCP_PROJECT) >/dev/null 2>&1; then \
			printf '%s' "$$value" | gcloud secrets versions add "$$key" \
				--data-file=- --project=$(GCP_PROJECT) --quiet; \
			echo "  Updated: $$key"; \
		else \
			printf '%s' "$$value" | gcloud secrets create "$$key" \
				--data-file=- --project=$(GCP_PROJECT) --quiet; \
			echo "  Created: $$key"; \
		fi; \
	done < "$(ENV_FILE)"
	@echo "Done."

secrets-out:
	@for name in $(KNOWN_SECRETS); do \
		value=$$(gcloud secrets versions access latest --secret="$$name" --project=$(GCP_PROJECT) 2>/dev/null) && \
		echo "$$name=$$value"; \
	done

# ============================================================================
# Utilities
# ============================================================================

clean:
	rm -f backend/server backend/agent-linux backend/authproxy backend/ocmptyd backend/ocm-secrets
	rm -f backend/*.db
	@echo "Cleaned build artifacts"

# ============================================================================
# Debugging
# ============================================================================

debug:
	bash scripts/debug-platform.sh all

debug-hosts:
	bash scripts/debug-platform.sh hosts

debug-logs:
	bash scripts/debug-platform.sh logs

debug-schema:
	bash scripts/debug-platform.sh schema

debug-agent:
	bash scripts/debug-platform.sh agent $(if $(filter-out ocm,$(VM)),$(VM),)

debug-ssh:
	@ocm machines ssh-debug $(if $(MACHINE),$(MACHINE),)

verify-deploy:
	bash scripts/verify-deploy.sh

verify-deploy-wait:
	bash scripts/verify-deploy.sh --wait

# ============================================================================
# Quick Aliases
# ============================================================================

# ============================================================================
# Rootfs Distribution (GCS)
# ============================================================================

# Publish rootfs to GCS — full pipeline orchestrated from Mac
# 1. Cross-compile agent, authproxy, ocmptyd, ocm-secrets locally
# 2. SCP binaries + workspace files to VM
# 3. Build rootfs on VM (Docker + ext4)
# 4. Upload compressed rootfs + manifest to GCS
# Usage: make publish-rootfs VM=ocm
publish-rootfs: build-agent build-authproxy build-ocmptyd build-ocm-secrets
	@echo "=========================================="
	@echo "Publishing rootfs to GCS via $(VM)"
	@echo "=========================================="
	@echo ""
	@echo "[1/4] Uploading binaries to $(VM) (parallel)..."
	gcloud compute scp backend/agent-linux "mathewma@$(VM):/tmp/agent" --zone=$(ZONE) &
	gcloud compute scp backend/authproxy "mathewma@$(VM):/tmp/authproxy" --zone=$(ZONE) &
	gcloud compute scp backend/ocmptyd "mathewma@$(VM):/tmp/ocmptyd" --zone=$(ZONE) &
	gcloud compute scp backend/ocm-secrets "mathewma@$(VM):/tmp/ocm-secrets" --zone=$(ZONE) &
	wait
	@echo ""
	@echo "[2/4] Packaging and uploading workspace files..."
	tar -czf /tmp/ocm-workspace-upload.tar.gz \
		rootfs/ \
		scripts/init-openclaw.sh scripts/build-rootfs.sh scripts/upload-rootfs.sh \
		scripts/ocm-metadata scripts/ocm-test-llm scripts/ocm-env scripts/ocm-status \
		scripts/ocm-migrate scripts/cf-ssh-check scripts/migrations/ \
		Makefile
	gcloud compute scp /tmp/ocm-workspace-upload.tar.gz "mathewma@$(VM):/tmp/" --zone=$(ZONE)
	rm -f /tmp/ocm-workspace-upload.tar.gz
	@echo ""
	@echo "[3/4] Building rootfs on $(VM)..."
	gcloud compute ssh "mathewma@$(VM)" --zone=$(ZONE) --command="\
		cd /tmp && \
		tar -xzf ocm-workspace-upload.tar.gz && \
		sudo cp /tmp/authproxy /usr/local/bin/authproxy && sudo chmod +x /usr/local/bin/authproxy && \
		sudo cp /tmp/ocmptyd /usr/local/bin/ocmptyd && sudo chmod +x /usr/local/bin/ocmptyd && \
		sudo cp /tmp/ocm-secrets /usr/local/bin/ocm-secrets && sudo chmod +x /usr/local/bin/ocm-secrets && \
		sudo OCM_SNAPSHOT=$(GIT_COMMIT) bash scripts/build-rootfs.sh"
	@echo ""
	@echo "[4/4] Uploading rootfs to GCS..."
	gcloud compute ssh "mathewma@$(VM)" --zone=$(ZONE) --command="\
		cd /tmp && \
		GCS_BUCKET=$(GCS_BUCKET) bash scripts/upload-rootfs.sh"
	@echo ""
	@echo "=========================================="
	@echo "Rootfs published to gs://$(GCS_BUCKET)/rootfs/"
	@echo "=========================================="
	@echo ""
	@echo "Verify: make show-rootfs-manifest"
	@echo "Agents will pull the new rootfs on next VM boot."

# Compress and upload rootfs to GCS (run on VM only)
upload-rootfs:
	@echo "Uploading rootfs to GCS..."
	bash scripts/upload-rootfs.sh
	@$(MAKE) register-rootfs

register-rootfs:
	@echo "Registering rootfs release in database..."
	bash scripts/register-rootfs-release.sh $(ROOTFS_RELEASE)

upload-openclaw:
	@echo "Uploading OpenClaw runtime artifact to GCS..."
	@set -e; \
	VERSION_FILE="$$(mktemp)"; \
	trap 'rm -f "$$VERSION_FILE"' EXIT; \
	ARTIFACT_PATH="$(if $(OPENCLAW_VERSION),/var/lib/ocm/openclaw-artifacts/openclaw-v$(OPENCLAW_VERSION)-linux-amd64.tar.zst,)"; \
	if [ -n "$$ARTIFACT_PATH" ]; then \
		OPENCLAW_SKIP_CHANNEL_MANIFEST="$(OPENCLAW_SKIP_CHANNEL_MANIFEST)" OPENCLAW_RELEASE_VERSION_FILE="$$VERSION_FILE" OPENCLAW_ARTIFACT_PATH="$$ARTIFACT_PATH" bash scripts/upload-openclaw.sh; \
	else \
		OPENCLAW_SKIP_CHANNEL_MANIFEST="$(OPENCLAW_SKIP_CHANNEL_MANIFEST)" OPENCLAW_RELEASE_VERSION_FILE="$$VERSION_FILE" bash scripts/upload-openclaw.sh; \
	fi; \
	UPLOADED_RELEASE="$$(cat "$$VERSION_FILE")"; \
	if [ "$(OPENCLAW_SKIP_CHANNEL_MANIFEST)" = "1" ]; then \
		echo "Staged OpenClaw release $$UPLOADED_RELEASE; skipping DB registration until promotion."; \
	else \
		$(MAKE) register-openclaw OPENCLAW_RELEASE="$$UPLOADED_RELEASE"; \
	fi

register-openclaw:
	@echo "Registering OpenClaw release in database..."
	bash scripts/register-openclaw-release.sh $(OPENCLAW_RELEASE)

promote-openclaw:
	@test -n "$(OPENCLAW_RELEASE)" || { echo "Usage: make promote-openclaw OPENCLAW_RELEASE=v2026.x.y-rN OPENCLAW_CHANNEL=rc"; exit 1; }
	@test -n "$(OPENCLAW_CHANNEL)" || { echo "OPENCLAW_CHANNEL is required; use rc/dev/stable explicitly."; exit 1; }
	OPENCLAW_CHANNEL="$(OPENCLAW_CHANNEL)" bash scripts/promote-openclaw-release.sh $(OPENCLAW_RELEASE)

# Build rootfs then upload to GCS.
# New VMs pull the latest rootfs from GCS at start time — no agent restart needed.
# Only upload the agent separately (make upload-agent) when agent code changes.
build-upload-rootfs: build-rootfs smoke-test
	@$(MAKE) upload-rootfs

build-opik-plugin:
	@echo "Building opik-openclaw plugin..."
	bash scripts/build-opik-plugin.sh

# Rebuild plugins/composio-plugin.tgz from a local peer checkout of the
# Composio OpenClaw plugin repo. Not wired as a default dependency of
# build-openclaw because the source repo isn't on npm and may not exist on
# every dev machine; run explicitly when the plugin source changes. The
# resulting .tgz is committed to this repo and consumed by
# build-openclaw-runtime.sh.
build-composio-plugin:
	@echo "Building composio plugin..."
	bash scripts/build-composio-plugin.sh

build-upload-openclaw: build-openclaw
	@$(MAKE) upload-openclaw

build-stage-openclaw: build-openclaw
	@$(MAKE) upload-openclaw OPENCLAW_SKIP_CHANNEL_MANIFEST=1

# Browser companion VM rootfs (Alpine + Chromium for CDP access)
build-browser-rootfs:
	sudo bash scripts/build-browser-rootfs.sh
	@echo ""
	@echo "Browser rootfs built: /var/lib/ocm/images/browser-rootfs.ext4"

upload-browser-rootfs:
	@echo "Uploading browser rootfs to GCS..."
	bash scripts/upload-browser-rootfs.sh

build-upload-browser-rootfs: build-browser-rootfs upload-browser-rootfs

# Kernel Images based browser VM rootfs (headful Chromium + CDP + live view)
build-kernel-browser-rootfs:
	sudo -E KERNEL_IMAGES_DIR="$(KERNEL_IMAGES_DIR)" KERNEL_BROWSER_IMAGE="$(KERNEL_BROWSER_IMAGE)" bash scripts/build-kernel-browser-rootfs.sh
	@echo ""
	@echo "Kernel browser rootfs built: /var/lib/ocm/images/kernel-browser-rootfs.ext4"

upload-kernel-browser-rootfs:
	@echo "Uploading Kernel browser rootfs to GCS..."
	bash scripts/upload-kernel-browser-rootfs.sh

build-upload-kernel-browser-rootfs: build-kernel-browser-rootfs upload-kernel-browser-rootfs

# Firecracker-level E2E test for the kernel-browser rootfs.
# Pulls the current rootfs from GCS (matches the live manifest), boots it
# in a real Firecracker microVM, then probes CDP and Neko endpoints. Catches
# bugs Docker can't reproduce (init script, kernel cmdline, mount layout).
test-kernel-browser-rootfs-e2e:
	sudo -E bash scripts/test-browser-vm-firecracker-e2e.sh

# Docker-level smoke test for the kernel-browser rootfs (no Firecracker).
test-kernel-browser-rootfs-docker:
	bash scripts/test-browser-vm-e2e.sh

# List available rootfs versions in GCS
GCS_BUCKET ?= openclawmachines
list-rootfs:
	@echo "Available rootfs versions:"
	gsutil ls "gs://$(GCS_BUCKET)/rootfs/manifest-*.json" 2>/dev/null | sort || echo "  (none found)"

# Show current rootfs manifest
show-rootfs-manifest:
	@gsutil cat "gs://$(GCS_BUCKET)/rootfs/manifest.json" 2>/dev/null | python3 -m json.tool || echo "No manifest found"

# Show current browser rootfs manifest
show-browser-rootfs-manifest:
	@gsutil cat "gs://$(GCS_BUCKET)/browser-rootfs/manifest.json" 2>/dev/null | python3 -m json.tool || echo "No manifest found"

show-kernel-browser-rootfs-manifest:
	@gsutil cat "gs://$(GCS_BUCKET)/kernel-browser-rootfs/manifest.json" 2>/dev/null | python3 -m json.tool || echo "No manifest found"

# Rollback rootfs to a specific version
# Usage: make rollback-rootfs VERSION=abc1234-20260224T120000Z
rollback-rootfs:
ifndef VERSION
	@echo "Usage: make rollback-rootfs VERSION=<version>"
	@echo ""
	@echo "Available versions:"
	@gsutil ls "gs://$(GCS_BUCKET)/rootfs/manifest-*.json" 2>/dev/null | sort || echo "  (none found)"
else
	@echo "Rolling back to version: $(VERSION)"
	gsutil cp "gs://$(GCS_BUCKET)/rootfs/manifest-$(VERSION).json" "gs://$(GCS_BUCKET)/rootfs/manifest.json"
	@echo "Rollback complete. Restart agents to pick up the change."
endif

# ============================================================================
# Agent Distribution (GCS)
# ============================================================================

# Upload agent binary to GCS for self-update
upload-agent: build-agent
	bash scripts/upload-agent.sh

# List available agent versions in GCS
list-agent:
	@echo "Available agent versions:"
	@gsutil ls "gs://$(GCS_BUCKET)/agent/manifest-*.json" 2>/dev/null | sort || echo "  (none found)"

# Show current agent manifest
show-agent-manifest:
	@gsutil cat "gs://$(GCS_BUCKET)/agent/manifest.json" 2>/dev/null | python3 -m json.tool || echo "No manifest found"

# Rollback agent to a specific version
# Usage: make rollback-agent VERSION=abc1234-20260224T120000Z
rollback-agent:
ifndef VERSION
	@echo "Usage: make rollback-agent VERSION=<version>"
	@echo ""
	@echo "Available versions:"
	@gsutil ls "gs://$(GCS_BUCKET)/agent/manifest-*.json" 2>/dev/null | sort || echo "  (none found)"
else
	@echo "Rolling back to version: $(VERSION)"
	gsutil cp "gs://$(GCS_BUCKET)/agent/manifest-$(VERSION).json" "gs://$(GCS_BUCKET)/agent/manifest.json"
	@echo "Rollback complete. Agents will pick up the change on next check cycle."
endif

# ============================================================================
# CLI Distribution (GCS)
# ============================================================================

# Upload CLI to GCS (multi-platform: linux/amd64, darwin/amd64, darwin/arm64)
upload-cli:
	bash scripts/upload-cli.sh

# ============================================================================
# Build All Components
# ============================================================================

# Build all components and upload to GCS
# Uploads run in parallel after build completes.
# NOTE: upload-agent triggers agent self-update → restarts all hosts → kills running VMs.
# Only include it when agent code actually changed.
build-components: build-rootfs build-openclaw
	@$(MAKE) -j3 upload-rootfs upload-openclaw upload-cli
	@echo "All components built and uploaded to GCS"
	@echo "NOTE: Agent not uploaded. Run 'make upload-agent' separately if agent code changed."

.PHONY: debug debug-hosts debug-logs debug-schema debug-agent debug-ssh verify-deploy verify-deploy-wait b f t upload-agent list-agent show-agent-manifest rollback-agent upload-cli build-components build-browser-rootfs upload-browser-rootfs build-upload-browser-rootfs show-browser-rootfs-manifest build-kernel-browser-rootfs upload-kernel-browser-rootfs build-upload-kernel-browser-rootfs show-kernel-browser-rootfs-manifest test-kernel-browser-rootfs-e2e test-kernel-browser-rootfs-docker provision-host enroll-host deploy-agent-host ssh-host host-status host-logs setup-host
b: backend
f: frontend
t: test

# ============================================================================
# 3rd-Party Host Management (OVH, Hetzner, etc.)
# ============================================================================

# Connection defaults — override via env or command line
HOST_USER ?= ubuntu
HOST_KEY  ?= ~/.ssh/ovh_cloud
SSH_OPTS  := -i $(HOST_KEY) -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10

# Named host registry — add new hosts here
# Format: HOST_<NAME>_IP
HOST_EAST_IP  := 15.204.241.166
HOST_WEST_IP  := 15.204.104.54
HOST_WEST2_IP := 15.204.104.201
ALL_HOSTS     := east west west2

# Resolve HOST= name to HOST_IP (supports both HOST=east and HOST_IP=x.x.x.x)
HOST ?=
HOST_IP ?=
ifdef HOST
  HOST_UPPER := $(shell echo $(HOST) | tr '[:lower:]' '[:upper:]')
  HOST_IP := $(HOST_$(HOST_UPPER)_IP)
endif

# Backend URL for enrollment (production default)
BACKEND_URL ?= https://api.openclawmachines.com

# SSH helper (used by all host-* targets)
define host-ssh
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make $@ HOST=east  (or HOST_IP=x.x.x.x)"; exit 1; fi
	ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP) $(1)
endef

# Step 1: Provision host — install all system dependencies
# Downloads kernel from GCS locally and SCPs it (non-GCP hosts lack gsutil auth)
# Usage: make provision-host HOST_IP=15.204.241.166
provision-host:
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make provision-host HOST=east  (or HOST_IP=x.x.x.x)"; exit 1; fi
	@echo "=========================================="
	@echo "Provisioning $(HOST_USER)@$(HOST_IP)"
	@echo "=========================================="
	scp $(SSH_OPTS) scripts/provision-host.sh $(HOST_USER)@$(HOST_IP):~/provision-host.sh
	ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP) "sudo bash ~/provision-host.sh"
	@echo "==> Delivering kernel to host..."
	@if [ ! -f /tmp/ocm-vmlinux ]; then \
		echo "Downloading kernel from GCS..."; \
		gsutil cp gs://openclawmachines/vmlinux /tmp/ocm-vmlinux; \
	fi
	scp $(SSH_OPTS) /tmp/ocm-vmlinux $(HOST_USER)@$(HOST_IP):/tmp/vmlinux
	ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP) "\
		sudo mkdir -p /var/lib/ocm/images && \
		sudo mv /tmp/vmlinux /var/lib/ocm/images/vmlinux && \
		sudo ln -sf /var/lib/ocm/images/vmlinux /var/lib/ocm/vmlinux && \
		echo 'Kernel installed at /var/lib/ocm/vmlinux'"

# Step 2: Enroll host — register with control plane + configure tunnel
# Usage: make enroll-host HOST_IP=15.204.241.166 TOKEN=<enrollment-token>
TOKEN ?=
enroll-host:
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make $@ HOST=east"; exit 1; fi
	@if [ -z "$(TOKEN)" ]; then echo "ERROR: TOKEN required. Create one via: curl -X POST $(BACKEND_URL)/api/admin/hosts/enrollment-tokens"; exit 1; fi
	@echo "=========================================="
	@echo "Enrolling $(HOST_USER)@$(HOST_IP)"
	@echo "=========================================="
	ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP) \
		"curl -sL $(BACKEND_URL)/api/agent/install | sudo bash -s -- $(TOKEN)"

# Step 3: Update agent binary on host (for manual pushes outside self-update)
# Usage: make deploy-agent-host HOST_IP=15.204.241.166
deploy-agent-host: build-agent
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make $@ HOST=east"; exit 1; fi
	@echo "=========================================="
	@echo "Deploying agent to $(HOST_USER)@$(HOST_IP)"
	@echo "=========================================="
	scp $(SSH_OPTS) backend/agent-linux $(HOST_USER)@$(HOST_IP):/tmp/ocm-agent
	ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP) "\
		sudo systemctl stop ocm-agent 2>/dev/null || true && \
		sudo mv /tmp/ocm-agent /usr/local/bin/ocm-agent && \
		sudo chmod +x /usr/local/bin/ocm-agent && \
		/usr/local/bin/ocm-agent --version && \
		sudo systemctl start ocm-agent"
	@echo "Agent deployed and restarted on $(HOST_IP)"

# All-in-one: provision + enroll (enrollment now downloads agent + starts it)
# Usage: make setup-host HOST_IP=15.204.241.166 TOKEN=<enrollment-token>
setup-host: provision-host enroll-host
	@echo ""
	@echo "=========================================="
	@echo "Host setup complete: $(HOST_IP)"
	@echo "=========================================="
	@echo "  Agent should now be sending heartbeats."
	@echo "  Check: make host-status HOST_IP=$(HOST_IP)"

# SSH into host
# Usage: make ssh-host HOST=east  (or ssh-east)
ssh-host:
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make $@ HOST=east"; exit 1; fi
	ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP)

# Check host status
# Usage: make host-status HOST=east  (or status-east)
host-status:
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make $@ HOST=east"; exit 1; fi
	@echo "=========================================="
	@echo "Host Status: $(HOST_IP)"
	@echo "=========================================="
	@ssh $(SSH_OPTS) $(HOST_USER)@$(HOST_IP) "\
		echo '--- System ---'; \
		uname -r; \
		echo ''; \
		echo '--- KVM ---'; \
		[ -e /dev/kvm ] && echo 'available' || echo 'NOT FOUND'; \
		echo ''; \
		echo '--- Firecracker ---'; \
		/usr/local/bin/firecracker --version 2>/dev/null || echo 'not installed'; \
		echo ''; \
		echo '--- cloudflared ---'; \
		/usr/local/bin/cloudflared --version 2>/dev/null || echo 'not installed'; \
		echo ''; \
		echo '--- Agent ---'; \
		/usr/local/bin/ocm-agent --version 2>/dev/null || echo 'not installed'; \
		echo ''; \
		echo '--- Agent Service ---'; \
		sudo systemctl is-active ocm-agent 2>/dev/null || echo 'not running'; \
		echo ''; \
		echo '--- Kernel ---'; \
		[ -f /var/lib/ocm/vmlinux ] && echo 'present' || echo 'NOT FOUND'; \
		echo ''; \
		echo '--- Disk ---'; \
		df -h / | tail -1; \
		echo ''; \
		echo '--- Memory ---'; \
		free -h | head -2"

# Tail agent logs on host
# Usage: make host-logs HOST=east
host-logs:
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: HOST or HOST_IP required. Usage: make $@ HOST=east"; exit 1; fi
	ssh $(SSH_OPTS) -t $(HOST_USER)@$(HOST_IP) \
		"sudo journalctl -u ocm-agent -f --no-pager"

# ---- Named host shortcuts ----
# Quick access without typing HOST=east/west every time
ssh-east:
	@$(MAKE) ssh-host HOST=east
ssh-west:
	@$(MAKE) ssh-host HOST=west
ssh-west2:
	@$(MAKE) ssh-host HOST=west2
status-east:
	@$(MAKE) host-status HOST=east
status-west:
	@$(MAKE) host-status HOST=west
status-west2:
	@$(MAKE) host-status HOST=west2
logs-east:
	@$(MAKE) host-logs HOST=east
logs-west:
	@$(MAKE) host-logs HOST=west
logs-west2:
	@$(MAKE) host-logs HOST=west2

# Check all hosts at once
status-all:
	@for host in $(ALL_HOSTS); do \
		echo ""; \
		$(MAKE) --no-print-directory host-status HOST=$$host; \
	done

.PHONY: ssh-east ssh-west ssh-west2 status-east status-west status-west2 logs-east logs-west logs-west2 status-all

# Git helpers
# Usage: make commit MSG="your message"
# Usage: make commit-push MSG="your message"
commit:
	@if [ -z "$(MSG)" ]; then echo "ERROR: MSG required. Usage: make commit MSG=\"your message\""; exit 1; fi
	git add -A && git commit -m "$(MSG)"

commit-push: commit
	git push

# Agent Orchestrator (AO) — parallel Claude Code agents on GitHub issues
# Usage: make ao ISSUES="39 40 53"
# Usage: make ao-auto                    (picks unassigned code-only issues)
# Usage: make ao-status                  (check running agents)
# Usage: make ao-logs ISSUE=39           (tail log for specific issue)
# Usage: make ao-clean                   (remove finished worktrees + pids)
ao:
	@if [ -z "$(ISSUES)" ]; then echo "ERROR: ISSUES required. Usage: make ao ISSUES=\"39 40 53\""; exit 1; fi
	./scripts/ao.sh $(ISSUES)

ao-auto:
	./scripts/ao.sh --label code-only --limit $(or $(LIMIT),3)

ao-dry-run:
	@if [ -z "$(ISSUES)" ]; then echo "ERROR: ISSUES required."; exit 1; fi
	./scripts/ao.sh --dry-run $(ISSUES)

ao-status:
	@echo "Running agents:"
	@for f in .claude/ao/pids/*.pid; do \
		[ -f "$$f" ] || continue; \
		pid=$$(cat "$$f"); \
		issue=$$(basename "$$f" .pid); \
		if kill -0 "$$pid" 2>/dev/null; then \
			echo "  $$issue — PID $$pid (running)"; \
		else \
			echo "  $$issue — PID $$pid (finished)"; \
		fi; \
	done
	@echo ""
	@echo "Worktrees:"
	@git worktree list | grep -E "ao/" || echo "  (none)"

ao-logs:
	@if [ -z "$(ISSUE)" ]; then echo "ERROR: ISSUE required. Usage: make ao-logs ISSUE=39"; exit 1; fi
	@ls -t .claude/ao/logs/issue-$(ISSUE)-*.log 2>/dev/null | head -1 | xargs tail -f

ao-clean:
	@echo "Cleaning up AO worktrees and pids..."
	@for wt in $$(git worktree list --porcelain | grep -oP 'worktree .*/ao/issue-\d+' | awk '{print $$2}'); do \
		echo "  Removing worktree: $$wt"; \
		git worktree remove "$$wt" --force 2>/dev/null || true; \
	done
	@rm -f .claude/ao/pids/*.pid
	@echo "Done."
