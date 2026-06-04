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

The public core includes the CLI, a minimal control plane, host enrollment,
machine lifecycle, placement, worker agents, and runtime pieces needed to run
Firecracker sandboxes locally or in a self-hosted deployment.

## Why OpenClaw Machines

- **Real isolation, not containers.** One Firecracker microVM per agent, with a
  separate guest kernel and KVM hardware boundary.
- **Bring your own hosts.** Run the control plane and workers on your own
  KVM-enabled Linux machines.
- **Local or hosted.** Start with one local host, then operate the same core as a
  hosted/self-managed deployment.
- **Apache-2.0.** The public core and `ocm` CLI are permissively licensed for
  adoption, embedding, and contribution.
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

## Requirements

OpenClaw Machines runs Firecracker microVMs, which require KVM. You need a
KVM-enabled Linux host: bare metal, or a cloud VM with nested virtualization
enabled. It does not run on macOS, Windows/WSL, or a standard cloud VM without
nested virtualization.

Check your host:

```bash
make preflight
