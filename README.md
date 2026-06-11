# OpenClaw Machines

**Run as many isolated OpenClaw agents as you need, on hardware you own.**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/mathaix/OpenClawMachines/actions/workflows/test.yml/badge.svg)](https://github.com/mathaix/OpenClawMachines/actions)
[![Stars](https://img.shields.io/github/stars/mathaix/OpenClawMachines?style=social)](https://github.com/mathaix/OpenClawMachines)

OpenClaw Machines is an **open-source platform for running AI agents in secure,
sandboxed [Firecracker](https://github.com/firecracker-microvm/firecracker)
microVMs**. Each [OpenClaw](https://github.com/openclaw/openclaw) agent runs in
its own KVM-backed microVM — its own kernel, not a container — so you can
execute agent-generated and untrusted code on infrastructure you control.

Think: a mini-cloud for AI agents, that you self-host.

## Why OpenClaw Machines

- **Real isolation, not containers.** One Firecracker microVM per agent, with a
  separate guest kernel and KVM hardware boundary.
- **Bring your own hosts.** Run the control plane and workers on your own
  KVM-enabled Linux machines.
- **Local or operator-hosted.** Start with one local host, then operate the same
  public core in a self-managed deployment.
- **Apache-2.0.** The public core and companion
  [`ocm` CLI](https://github.com/mathaix/ocm-cli) are permissively licensed for
  adoption, embedding, and contribution.
- **Built for agents.** Terminal, web chat, browser automation, per-VM routing,
  and OpenClaw runtime integration.

## Ways to run OpenClaw

If you run OpenClaw today, you have a few options:

1. **Local hardware** — run it on your own laptop or desktop.
2. **A VPS** (e.g. Hostinger, DigitalOcean) — rent a virtual server and run it
   there.
3. **A managed service** (e.g. KiloClaw) — spin up a hosted OpenClaw instance and
   pay per instance.

OpenClaw Machines adds a **fourth option**: rent a **bare-metal server** from a
provider like **OVHcloud** or **Hetzner**, point OpenClaw Machines at it, and
spin up **as many isolated OpenClaw instances as you need** — for one flat server
cost.

| Feature | Local hardware | VPS (Hostinger) | Managed (KiloClaw) | **OpenClaw Machines** |
|---|---|---|---|---|
| Setup effort | Low | Medium | Lowest | Medium (provision + enroll host) |
| Per-agent isolation | Process-level | Shared-kernel / container | Per instance (managed) | **Hardware — Firecracker microVM** |
| Run many agents | Limited by your box | Limited by VPS size | Yes — but pay for each | **Yes — as many as the server fits** |
| Multi-user / teams | No | Manual | Varies | **Yes — built-in accounts & teams** |
| Cost model | Your own hardware | Pay per VPS | Pay per instance | **Pay per server (flat)** |
| Cost at scale | Doesn't scale | Rises with size | Highest (linear per agent) | **Lowest per agent** |
| Hardware control | Full (but limited) | Virtualized, shared | None | **Full — dedicated bare metal** |
| Data & keys stay yours | Yes | Mostly | No (their infra) | **Yes — your hardware** |
| Backups / snapshots | Manual | Provider snapshots | Managed | **Built-in** |
| Ops / maintenance | You | You | None | You (self-hosted control plane) |

In short: the **managed** route is easiest but priced per agent; **local** and
**VPS** are cheap to start but don't isolate or scale well. **OpenClaw Machines**
trades a little more setup for the best economics and isolation once you're
running more than a couple of agents — one server, many hardware-isolated agents,
all yours.

## How it works

OpenClaw Machines turns your Linux servers into a pool of secure, on-demand
sandboxes:

- **Control plane (Go backend) — the brain.** Accounts, machines, hosts, and
  config; decides which host each new VM lands on and orchestrates its
  lifecycle.
- **Hosts + worker agents — your Linux boxes.** You enroll a host with one
  install command; its worker agent boots and stops Firecracker microVMs when
  told to.
- **Machines — one isolated microVM per agent.** Each runs the OpenClaw agent, a
  live terminal, and the runtime that wires it together.
- **Browser VMs — separate microVMs for browser automation.** Headful Chromium
  with a live view, paired 1:1 with a machine and driven over CDP.
- **Data plane — how you reach a running VM.** Each machine gets its own
  hostname (`m-<name>.yourdomain.com`) and its own Cloudflare Tunnel terminating
  inside the VM, with auth enforced at the edge and again in the VM.

When you create a machine, the control plane picks a host with capacity, that
host's agent boots a microVM from a prepared root filesystem, config and
credentials are injected, and a route is published so the VM is reachable at its
own hostname.

For the full design — data plane, routing, tunnels, lifecycle — see
**[docs/architecture.md](docs/architecture.md)**. For what it's built with, see
**[docs/tech-stack.md](docs/tech-stack.md)**.

## Get up and running

The **[Getting Started guide](docs/getting-started.md)** is split into three
stages, and each stage ends with something working:

| Stage | What you get | What you need |
|---|---|---|
| **1 · Local evaluation** | The full stack + a real Firecracker machine on one box, using prebuilt artifacts | One KVM-enabled Linux box |
| **2 · Cloudflare + a dedicated host** | A real deployment: machines at their own hostnames, hosts enrolled remotely | A Cloudflare account + domain, a cloud or bare-metal host |
| **3 · The full workflow** | Day-to-day usage: create and use machines, browser VMs, lifecycle, upgrades | Stages 1–2 |

**Start here → [docs/getting-started.md](docs/getting-started.md)**

### Requirements

OpenClaw Machines runs Firecracker microVMs, which require KVM. You need a
KVM-enabled **Linux** host: bare metal, or a cloud VM with nested virtualization
(e.g. GCP n2). It does not run on macOS, Windows/WSL, or a standard cloud VM
without nested virtualization. Check your host:

```bash
make preflight
```

## Documentation

- [Getting Started](docs/getting-started.md) — the three-stage setup guide
- [Architecture](docs/architecture.md) — data plane, routing, tunnels, lifecycle
- [Tech stack](docs/tech-stack.md) — the five layers, from browser to sandbox
- [Control plane profiles](docs/control-plane-profiles.md) — `local` / `operator` / `hosted`
- [Self-hosted control plane](docs/self-hosted-control-plane.md) — Cloudflare + auth prerequisites
- [Host enrollment](docs/host-enrollment.md) — the enrollment path in depth
- [CI and release lanes](docs/ci-release.md) — safety boundaries for CI
- [`ocm` CLI project](https://github.com/mathaix/ocm-cli)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), the [security policy](SECURITY.md), and
the [code of conduct](CODE_OF_CONDUCT.md).

## License

[Apache-2.0](LICENSE)
