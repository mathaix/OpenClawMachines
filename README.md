# OpenClaw Machines

**Run AI agents in secure, isolated Firecracker microVMs on hardware you control.**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/mathaix/OpenClawMachines/actions/workflows/test.yml/badge.svg)](https://github.com/mathaix/OpenClawMachines/actions)
[![Stars](https://img.shields.io/github/stars/mathaix/OpenClawMachines?style=social)](https://github.com/mathaix/OpenClawMachines)

OpenClaw Machines is an **open-source platform for running AI agents in secure,
sandboxed [Firecracker](https://github.com/firecracker-microvm/firecracker)
microVMs**. Each [OpenClaw](https://github.com/openclaw/openclaw) agent runs in
its own KVM-backed microVM, so you can execute agent-generated and untrusted code
on infrastructure you control.

The Apache-2.0 public core includes a minimal control plane, host enrollment,
machine lifecycle, placement, worker agents, and runtime pieces needed to run
Firecracker sandboxes locally or in a self-hosted/operator-hosted deployment.
The `ocm` CLI lives in the separate
[`mathaix/ocm-cli`](https://github.com/mathaix/ocm-cli) Apache-2.0 repository.

The private overlay boundary is intentionally separate: billing, plan
enforcement, commercial admin, enterprise-only hosted flows, launch/pricing
material, and confidential infrastructure notes are not public-core scope.

## Why OpenClaw Machines

- **Real isolation, not containers.** One Firecracker microVM per agent, with a
  separate guest kernel and KVM hardware boundary.
- **Bring your own hosts.** Run the control plane and workers on your own
  KVM-enabled Linux machines.
- **Local or operator-hosted.** Start with one local host, then operate the same
  public core in a self-managed deployment.
- **Apache-2.0.** The public core and companion `ocm` CLI are permissively
  licensed for adoption, embedding, and contribution.
- **Built for agents.** Terminal, web chat, browser automation, per-VM routing,
  and OpenClaw runtime integration.

## How it compares

| | OpenClaw Machines | E2B | Daytona | Modal |
|---|:---:|:---:|:---:|:---:|
| Open source | Apache-2.0 | Apache-2.0 | AGPL-3.0 | Proprietary |
| Self-host on your own hosts | Yes | Yes | Yes | No |
| Firecracker microVM isolation | Yes | Yes | Yes | No self-host |
| Local one-box evaluation | Yes | Partial | Yes | No |
| Public minimal control plane | Yes | Yes | Yes | No |

## Project Docs

- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [`ocm` CLI project](https://github.com/mathaix/ocm-cli)
- [Local and BYO-host setup](docs/local-setup.md)
- [Control plane deployment profiles](docs/control-plane-profiles.md)
- [Self-hosted control plane prerequisites](docs/self-hosted-control-plane.md)
- [LLM operator runbook](llms/self-hosted-setup.txt)
- [Public docs inventory](docs/public-docs-inventory.md)

## Requirements

OpenClaw Machines runs Firecracker microVMs, which require KVM. You need a
KVM-enabled Linux host: bare metal, or a cloud VM with nested virtualization
enabled. It does not run on macOS, Windows/WSL, or a standard cloud VM without
nested virtualization.

Check your host:

```bash
make preflight
```

See [docs/local-setup.md](docs/local-setup.md) for local and BYO-host runtime
setup expectations. For a production-like operator deployment that keeps the
hosted Cloudflare/Firebase/Worker/KVM architecture, see
[docs/self-hosted-control-plane.md](docs/self-hosted-control-plane.md).

For CI and release safety boundaries, see
[`docs/ci-release.md`](docs/ci-release.md).
