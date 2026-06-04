# Local and BYO-Host Setup

OpenClaw Machines public core can run on infrastructure you control. The local
runtime path is a Linux Firecracker host plus optional local control-plane
services. It does not require private OpenClaw Machines hosted credentials.

## Preflight

Run the host readiness check from the repository root:

```bash
make preflight
```

The check is read-only. It does not install packages, create network devices,
write under `/var/lib/ocm`, or contact hosted services. It fails for runtime
blockers and prints warnings for optional setup.

Preflight checks:

- developer tools: `go`, `node`, `npm`, `make`, `git`, and common helpers
- Linux/KVM support: supported architecture, `/dev/kvm`, and access hints
- Firecracker runtime: `firecracker`, `ip`, `iptables`, `sysctl`, `systemd`
  expectations, kernel image, rootfs image, and state directory hints
- build helpers: `docker`, `bsdtar`, `zstd`, `gsutil`, and `cloudflared`
- config hints: `.env`, `DATABASE_URL`, `FC_AGENT_TOKEN`,
  `SECRET_ENCRYPTION_KEY`, and local auth mode

## Host Requirements

Firecracker requires a Linux host with KVM. Use bare metal, or a cloud VM where
nested virtualization is enabled. macOS, Windows/WSL, and standard cloud VMs
without nested virtualization cannot run the Firecracker runtime.

Expected host assets:

- `firecracker` on `PATH`
- KVM available at `/dev/kvm`
- kernel image at `KERNEL_PATH`, defaulting to `/var/lib/ocm/vmlinux`
- rootfs image at `${IMAGES_DIR}/rootfs.ext4`, defaulting to
  `/var/lib/ocm/images/rootfs.ext4`, unless `ROOTFS_GCS_MANIFEST` is configured
- state directory at `STATE_DIR`, defaulting to `/var/lib/ocm/vms`
- systemd available when `VM_RUNTIME_OWNER=systemd-unit` (the default)

For a non-systemd experiment, set `VM_RUNTIME_OWNER=direct`. Production-like BYO
hosts should use the default systemd owner.

## Local Config

Create a local env file or export values in your shell:

```bash
cp .env.example .env
```

Minimum local hints:

```bash
DATABASE_URL=postgres://user:pass@localhost:5432/ocm?sslmode=disable
FC_AGENT_TOKEN=<local-agent-token>
SECRET_ENCRYPTION_KEY=<32-byte-value-from-openssl-rand-hex-16>
AUTH_MODE=dev
OCM_ALLOW_DEV_AUTH=1
DEV_USER_EMAIL=dev@localhost
```

`AUTH_MODE=dev` is for trusted local development only and intentionally requires
`OCM_ALLOW_DEV_AUTH=1`. Do not expose this mode publicly. For a self-hosted
control plane that should behave like the hosted control plane, use
`CONTROL_PLANE_PROFILE=operator` with Cloudflare Tunnel, Worker/KV, Firebase or
Cloudflare Access, and an operator-controlled domain. See
`docs/self-hosted-control-plane.md`.

## Runtime Images

If you are using embedded local images, place:

- kernel: `/var/lib/ocm/vmlinux` or set `KERNEL_PATH`
- rootfs: `/var/lib/ocm/images/rootfs.ext4` or set `IMAGES_DIR`

To build a rootfs locally, install Docker, `mkfs.ext4`, and `bsdtar`, then run:

```bash
make build-rootfs
```

If you configure `ROOTFS_GCS_MANIFEST`, the agent stages the rootfs from that
manifest instead of requiring `${IMAGES_DIR}/rootfs.ext4`. Use credentials and
storage that you operate.

## What Preflight Does Not Require

`make preflight` does not require Cloudflare, GCP, Firebase, hosted database, or
hosted OpenClaw Machines credentials. Those are deployment choices. The local
runtime blockers are Linux, KVM, Firecracker, kernel/rootfs assets, and the host
networking tools needed to create bridges, TAP devices, and isolation rules.
