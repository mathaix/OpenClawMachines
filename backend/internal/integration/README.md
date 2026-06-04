# Linux VM Integration Tests

This package contains integration tests that require a Linux host with KVM support (e.g., GCP VM with nested virtualization). These tests spin up real Firecracker MicroVMs and verify that every service inside the VM actually works — terminal, gateway, logging, and optionally through real Cloudflare tunnels.

## Prerequisites

1. **KVM support**: `/dev/kvm` must exist and be accessible
2. **Firecracker binary**: Must be in PATH
3. **Root privileges**: Required for bridge/TAP/iptables operations
4. **Kernel image**: At `/var/lib/ocm/vmlinux` (auto-downloaded from GCS if missing)
5. **Rootfs image**: At `/var/lib/ocm/images/rootfs.ext4` (auto-downloaded from GCS if missing)
6. **gsutil**: Required for auto-download (from google-cloud-sdk)

For Cloudflare tunnel tests, additionally:
7. **cloudflared binary**: Must be in PATH
8. **CF_API_TOKEN**: Cloudflare API token with tunnel + DNS permissions
9. **CF_ACCOUNT_ID**: Cloudflare account ID
10. **CF_ZONE_ID**: Cloudflare zone ID for the domain

## Running Tests

From the repository root:

```bash
# Run all integration tests (local, no tunnel)
make test-integration

# Run full E2E with Cloudflare tunnel
export CF_API_TOKEN="..."
export CF_ACCOUNT_ID="..."
export CF_ZONE_ID="..."
make test-integration-e2e

# Run a specific test
make test-integration-run TEST=TestGateway_Health

# Or run directly with go test
cd backend && sudo -E go test -v -tags integration -timeout 15m ./internal/integration/...
```

## Test Files

### `linux_vm_test.go` — Core VM Tests
- `TestPrerequisites_*` — Verifies KVM, firecracker, kernel, rootfs
- `TestBridge_*` — Bridge setup, TAP lifecycle, NAT rules
- `TestMetadata_*` — Metadata server register/unregister
- `TestOrchestrator_*` — VM create/destroy, list, health
- `TestAgent_*` — API health, auth, VM CRUD, progress SSE
- `TestProxy_*` — Health proxy, auth, terminal WebSocket
- `TestE2E_FullWorkflow` — Create → terminal test → destroy

### `gateway_test.go` — Gateway Functional Tests
- `TestGateway_HealthDirect` — Direct HTTP health check on bridge network (port 3000)
- `TestGateway_HealthViaProxy` — Health check routed through proxy
- `TestGateway_HealthEndpointStatus` — Verify proxy health endpoint reports gateway=true
- `TestGateway_WebSocket` — WebSocket connection to gateway through proxy

### `logging_test.go` — Log & Progress SSE Tests
- `TestLogging_SSE` — Subscribe to log stream, assert init sequence entries present
- `TestLogging_SSEContentType` — Verify SSE response headers (text/event-stream, no-cache)
- `TestLogging_ProgressFullSequence` — Subscribe to progress before VM creation, verify all steps emitted (allocating → rootfs → network → booting)

### `init_test.go` — Init Script Validation
- `TestInit_MetadataFetch` — Verify openclaw.json created, OCM_MACHINE_ID env var set
- `TestInit_GatewayRestart` — Kill gateway, verify supervisor restarts it within 15s
- `TestInit_RuntimeSelectionAutoFallsBackToBaked` — Boot with `runtime_source=auto`, verify guest logs fallback to baked OpenClaw when no artifact binary is staged
- `TestInit_RuntimeSelectionArtifactMissingBinaryEntersCrashLoop` — Boot with `runtime_source=artifact` and missing `OPENCLAW_BIN`, verify guest enters crash-loop protection with actionable logs

### `tunnel_test.go` — Cloudflare Tunnel E2E Tests
- `TestTunnel_Lifecycle` — Create/configure/DNS/verify/delete tunnel (no VM needed)
- `TestTunnel_HealthE2E` — Health check through real Cloudflare tunnel
- `TestTunnel_TerminalE2E` — Terminal WebSocket echo test through tunnel
- `TestTunnel_GatewayE2E` — Gateway health check through tunnel

## Configuration

Tests can be configured via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `TEST_KERNEL_PATH` | `/var/lib/ocm/vmlinux` | Path to kernel image |
| `TEST_IMAGES_DIR` | `/var/lib/ocm/images` | Directory containing rootfs.ext4 |
| `TEST_STATE_DIR` | `/tmp/ocm-test-{random}` | State directory for test VMs |
| `TEST_SOCKET_DIR` | `{state_dir}/sockets` | Socket directory for Firecracker |
| `TEST_BRIDGE_NAME` | `ocm-test-{random}` | Bridge name (auto-suffixed for isolation) |
| `TEST_AGENT_TOKEN` | `test-token-{random}` | Agent API token |
| `CF_API_TOKEN` | (none) | Cloudflare API token (tunnel tests only) |
| `CF_ACCOUNT_ID` | (none) | Cloudflare account ID (tunnel tests only) |
| `CF_ZONE_ID` | (none) | Cloudflare zone ID (tunnel tests only) |

## Auto-Download

If kernel or rootfs images are missing, tests will automatically download them from GCS:
- `gs://openclawmachines/vmlinux`
- `gs://openclawmachines/rootfs.ext4`

This requires `gsutil` to be installed and authenticated.

## Test Isolation

- Each test run uses unique bridge names and directories
- Resources are cleaned up via `t.Cleanup()` even on test failure
- Tests use the subnet `192.168.200.0/24` for VM networking
- Tunnel tests use `sync.Once` to share a single tunnel across E2E tests, cleaned up after

## Timeouts

- VM creation: 120 seconds
- Gateway readiness: 60 seconds
- WebSocket tests: 10 seconds
- API calls: 30 seconds
- Tunnel readiness: 60 seconds
- Overall test timeout: 15 minutes (local), 20 minutes (with tunnel)

## Troubleshooting

### "Skipping: /dev/kvm not available"
You need a machine with KVM support. On GCP, enable nested virtualization when creating the VM.

### "Skipping: firecracker binary not found"
Install firecracker: https://github.com/firecracker-microvm/firecracker/releases

### "Skipping: requires root privileges"
Run tests with sudo: `sudo -E go test ...`

### "Skipping: CF_API_TOKEN not set"
Set Cloudflare credentials to run tunnel tests. These tests create real tunnels and DNS routes.

### "gsutil cp failed"
Ensure you're authenticated with GCP: `gcloud auth application-default login`

### VM creation timeout
Check that the kernel and rootfs are compatible. View Firecracker logs in the state directory.

### Gateway not coming up
Check `/var/log/openclaw-gateway.log` inside the VM via the terminal WebSocket. The stale lock fix (rm lock/pid files before start) should prevent lock-related startup failures.
