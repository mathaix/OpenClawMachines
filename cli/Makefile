# OCM CLI Makefile

.PHONY: build clean install test

# Version info from git
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y%m%dT%H%M%SZ)
VERSION    := $(GIT_COMMIT)-$(BUILD_TIME)

# ldflags targeting cli/internal/commands package variables
LDFLAGS := -X 'github.com/mathaix/openclawmachines/cli/internal/commands.Version=$(VERSION)' \
           -X 'github.com/mathaix/openclawmachines/cli/internal/commands.GitCommit=$(GIT_COMMIT)' \
           -X 'github.com/mathaix/openclawmachines/cli/internal/commands.BuildTime=$(BUILD_TIME)'

# Output binary
BIN := bin/ocm

# Build the CLI binary
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./main.go
	@echo "Built: $(BIN) ($(VERSION))"

# Remove build artifacts
clean:
	rm -rf bin/
	@echo "Cleaned CLI build artifacts"

# Install to $GOPATH/bin (falls back to /usr/local/bin)
install: build
	@DEST=$${GOPATH:-$$(go env GOPATH)}/bin; \
	if [ -z "$$DEST" ] || [ ! -d "$$DEST" ]; then \
		DEST=/usr/local/bin; \
	fi; \
	cp $(BIN) "$$DEST/ocm"; \
	echo "Installed: $$DEST/ocm ($(VERSION))"

# Run tests
test:
	go test ./...
