# OpenClaw Machines public-core Makefile

.PHONY: help dev local-env local-postgres local-migrate local-backend local-frontend local-status local-stop preflight backend frontend status check check-go check-scripts test test-go test-unit test-frontend typecheck test-worker test-integration test-integration-local-e2e test-integration-tunnel-e2e test-integration-e2e test-integration-run integration-kvm build build-server build-agent build-authproxy build-ocm-secrets build-ocmptyd install-vm-binaries build-rootfs test-rootfs build-openclaw build-ocm-integrations-plugin setup-local-openclaw clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
INTEGRATION_PATH ?= /usr/sbin:/sbin:/usr/local/sbin:$(PATH)
INTEGRATION_SUDO ?= sudo -E env PATH="$(INTEGRATION_PATH)"

help:
	@echo "OpenClaw Machines Public-Core Commands"
	@echo "======================================"
	@echo ""
	@echo "Development:"
	@echo "  make preflight      - Check local/BYO-host prerequisites"
	@echo "  make local-env      - Create local dev env files"
	@echo "  make local-postgres - Start local Docker Postgres"
	@echo "  make local-backend  - Run migrations and start local backend"
	@echo "  make local-frontend - Start local frontend"
	@echo "  make backend        - Start backend server on port 8080"
	@echo "  make frontend       - Start frontend dev server on port 5173"
	@echo "  make dev            - Print local dev startup commands"
	@echo "  make status         - Check local backend/frontend health"
	@echo ""
	@echo "Testing:"
	@echo "  make check          - Run formatting, vet, and script syntax checks"
	@echo "  make test           - Run Go, frontend, and Worker tests"
	@echo "  make test-go        - Run Go tests"
	@echo "  make test-unit      - Run short Go tests"
	@echo "  make test-frontend  - Run frontend tests"
	@echo "  make typecheck      - Run frontend typecheck"
	@echo "  make test-worker    - Run Cloudflare Worker tests"
	@echo "  make integration-kvm - Run KVM/Firecracker integration tests"
	@echo "  make test-integration-e2e - Run local Worker+Firecracker e2e, plus tunnel smoke when CF creds exist"
	@echo "  make test-integration-tunnel-e2e - Run real Cloudflare tunnel e2e"
	@echo ""
	@echo "Build:"
	@echo "  make build          - Build backend server, agent, authproxy, and ocm-secrets"
	@echo "  make build-rootfs   - Build local Firecracker rootfs image"
	@echo "  make test-rootfs    - Verify built rootfs contents"
	@echo "  make build-openclaw - Build local OpenClaw runtime artifact"
	@echo "  make build-ocm-integrations-plugin - Build legacy OCM integrations fallback plugin"
	@echo "  make clean          - Remove local build artifacts"

dev:
	@echo "Start backend and frontend in separate terminals:"
	@echo "  make local-postgres"
	@echo "  make local-backend"
	@echo "  make local-frontend"

local-env:
	@bash scripts/local-dev.sh env

local-postgres:
	@bash scripts/local-dev.sh postgres

local-migrate:
	@bash scripts/local-dev.sh migrate

local-backend:
	@bash scripts/local-dev.sh backend

local-frontend:
	@bash scripts/local-dev.sh frontend

local-status:
	@bash scripts/local-dev.sh status

local-stop:
	@bash scripts/local-dev.sh stop

preflight:
	@bash scripts/preflight.sh

backend:
	cd backend && go run ./cmd/server/

frontend:
	cd frontend && npm run dev

status:
	@curl -s http://localhost:8080/health >/dev/null 2>&1 && echo "Backend: OK" || echo "Backend: not running"
	@curl -s http://localhost:5173 >/dev/null 2>&1 && echo "Frontend: OK" || echo "Frontend: not running"

check: check-go check-scripts

check-go:
	@cd backend && files="$$(find . -name '*.go' -not -path './tmp/*')" && unformatted="$$(gofmt -l $$files)" && test -z "$$unformatted" || (echo "$$unformatted"; exit 1)
	cd backend && go vet ./...

check-scripts:
	@find scripts ci -type f -name '*.sh' -print0 2>/dev/null | xargs -0 -r bash -n
	@if command -v shellcheck >/dev/null 2>&1; then \
		find ci -type f -name '*.sh' -print0 2>/dev/null | xargs -0 -r shellcheck; \
	else \
		echo "shellcheck not installed; skipping ci/*.sh lint"; \
	fi

test: test-go test-frontend test-worker

test-go:
	cd backend && go test ./...

test-unit:
	cd backend && go test -short ./...

test-frontend:
	cd frontend && npm run test

typecheck:
	cd frontend && npm run typecheck

test-worker:
	cd worker && npm run test

test-integration:
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && $(INTEGRATION_SUDO) go test -v -tags integration -timeout 45m ./internal/integration/... || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

test-integration-local-e2e: test-worker
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && $(INTEGRATION_SUDO) go test -v -tags integration -timeout 30m ./internal/integration/... -run 'Test(DataplaneSuite|GatewaySuite|E2E_FullWorkflow)$$' || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

test-integration-tunnel-e2e:
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && $(INTEGRATION_SUDO) go test -v -tags integration -timeout 20m ./internal/integration/... -run 'TestTunnel' || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

test-integration-e2e: test-integration-local-e2e
	@if [ -n "$$CF_API_TOKEN" ] && [ -n "$$CF_ACCOUNT_ID" ] && [ -n "$$CF_ZONE_ID" ]; then \
		$(MAKE) test-integration-tunnel-e2e; \
	else \
		echo "Skipping Cloudflare tunnel e2e: CF_API_TOKEN, CF_ACCOUNT_ID, and CF_ZONE_ID are not all set."; \
	fi

test-integration-run:
	@test -n "$(TEST)" || (echo "Usage: make test-integration-run TEST=TestName"; exit 1)
	@sudo scripts/test-cleanup.sh
	@sudo scripts/test-xfs-setup.sh
	@rc=0; cd backend && $(INTEGRATION_SUDO) go test -v -tags integration -timeout 25m ./internal/integration/... -run "$(TEST)" || rc=$$?; \
		sudo "$(CURDIR)/scripts/test-cleanup.sh"; exit $$rc

integration-kvm: test-integration

build: build-server build-agent build-authproxy build-ocm-secrets

build-server:
	cd backend && go build -ldflags "-X github.com/mathaix/openclawmachines/backend/pkg/version.Version=$(VERSION) -X github.com/mathaix/openclawmachines/backend/pkg/version.Commit=$(GIT_COMMIT)" -o server ./cmd/server

build-agent:
	cd backend && GOOS=linux GOARCH=amd64 go build -ldflags "-X github.com/mathaix/openclawmachines/backend/pkg/version.Version=$(VERSION) -X github.com/mathaix/openclawmachines/backend/pkg/version.Commit=$(GIT_COMMIT)" -o agent-linux ./cmd/agent

build-authproxy:
	cd backend && GOOS=linux GOARCH=amd64 go build -ldflags "-X github.com/mathaix/openclawmachines/backend/pkg/version.Version=$(VERSION) -X github.com/mathaix/openclawmachines/backend/pkg/version.Commit=$(GIT_COMMIT)" -o authproxy-linux ./cmd/authproxy

build-ocm-secrets:
	cd backend && GOOS=linux GOARCH=amd64 go build -o ocm-secrets-linux ./cmd/ocm-secrets

build-ocmptyd:
	cd backend && GOOS=linux GOARCH=amd64 go build -o ocmptyd-linux ./cmd/ocmptyd

# Install the in-VM binaries where scripts/build-rootfs.sh expects them
install-vm-binaries: build-authproxy build-ocm-secrets build-ocmptyd
	sudo install -m 0755 backend/authproxy-linux /usr/local/bin/authproxy
	sudo install -m 0755 backend/ocm-secrets-linux /usr/local/bin/ocm-secrets
	sudo install -m 0755 backend/ocmptyd-linux /usr/local/bin/ocmptyd

build-rootfs:
	@bash scripts/build-rootfs.sh

test-rootfs:
	@bash scripts/test-rootfs.sh

build-openclaw:
	@bash scripts/build-openclaw-runtime.sh

build-ocm-integrations-plugin:
	@bash scripts/build-ocm-integrations-plugin.sh

setup-local-openclaw:
	@bash scripts/check-openclaw-version.sh

clean:
	@rm -f backend/server backend/agent-linux backend/authproxy-linux backend/ocm-secrets-linux
	@rm -rf dist
