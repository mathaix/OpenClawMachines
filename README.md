# OpenClaw Machines

**Run as many isolated OpenClaw agents as you need, on hardware you own.**

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

OpenClaw Machines lets you take your own Linux servers and turn them into a pool
of secure, on-demand sandboxes. Each sandbox is a real Firecracker microVM (its
own kernel, hardware-isolated via KVM) that runs one AI agent. The platform is
the **control plane** that creates those VMs, keeps track of them, routes traffic
to them, and tears them down — so you can run many untrusted agents safely on
infrastructure you own.

Think: a mini-cloud for AI agents, that you self-host.

### Architecture

```mermaid
flowchart TB
    User["Browser / ocm CLI"]

    subgraph Edge["Cloudflare edge (optional in local mode)"]
        Worker["Worker<br/>auth check + KV route lookup"]
        Tunnel["Tunnel<br/>secure path into your host"]
    end

    subgraph ControlPlane["Control plane (your infra)"]
        API["Go backend API<br/>accounts · machines · hosts · placement"]
        Proxy["LLM proxy<br/>keys + per-machine usage"]
        DB[("Postgres")]
        API --- DB
        API --- Proxy
    end

    subgraph Host["Your Linux host (KVM)"]
        WorkerAgent["Worker agent<br/>boots / stops microVMs"]
        subgraph Machine["microVM — a &quot;Machine&quot;"]
            Agent["OpenClaw agent"]
            Term["terminal (ttyd :7681)"]
            Browser["headless browser"]
        end
    end

    User -->|create / manage| API
    API -->|place + boot| WorkerAgent
    WorkerAgent -->|Firecracker| Machine
    User -->|access| Worker --> Tunnel --> WorkerAgent --> Machine
    Agent -->|model calls| Proxy
```

### The building blocks

1. **Control plane (the Go backend) — the brain.** Holds the database of
   accounts, machines, hosts, and config. Exposes the API the UI/CLI call.
   Decides which host a new VM should land on (placement) and orchestrates its
   lifecycle.
2. **Hosts + worker agents — your Linux boxes.** You "enroll" a host (run an
   install script), and a worker agent on that box talks to the control plane and
   actually boots/stops Firecracker microVMs when told to.
3. **The microVM (a "Machine") — one isolated VM per agent.** Inside it runs:
   - the OpenClaw agent,
   - a terminal (ttyd on port 7681),
   - browser automation (a headless browser the agent can drive),
   - and the runtime that wires it all together.
4. **Routing / data plane — how a user reaches a running VM** from a browser,
   securely.

### What happens when you create a machine

1. You ask the control plane to create a machine (via the UI or the `ocm` CLI).
2. The control plane picks a host with capacity (**placement**) and tells that
   host's worker agent to boot a microVM from a prepared root filesystem image.
3. The VM comes up, the agent inside registers, and config/credentials get
   injected.
4. A **route** is published so the VM is reachable at its own hostname
   (e.g. `m-<name>.yourdomain.com`).
5. When you're done, the control plane stops/destroys the VM and frees the
   capacity.

### How you actually reach a running VM

```text
Your browser
   → Cloudflare Worker        (checks your auth token, looks up which host/VM via KV)
   → Cloudflare Tunnel        (secure path into your private host)
   → Worker agent on the host (authorizes with a proxy token)
   → the microVM              (terminal, web chat, or browser)
```

So you get a web chat with the agent, a live terminal, or a browser view — each
scoped to one isolated VM, with auth enforced at the edge.

### Supporting features that make it useful

- **LLM proxy.** The platform proxies the agent's model calls (Nebius,
  OpenRouter, Anthropic, etc.), so it can centralize keys, support BYO-keys, and
  track token usage per machine/account.
- **Credentials & secrets.** Per-machine encrypted secrets and provider
  credentials, injected into the VM.
- **Backups / snapshots.** Capture and restore a machine's state.
- **Per-VM routing & access.** Each VM gets its own hostname and isolated access
  path.
- **Capacity & placement policies.** Spread agents across your fleet and respect
  limits.

## Tech stack

OpenClaw Machines is built in five layers — from the browser down to the
hardware-isolated sandbox.

```mermaid
flowchart TB
    subgraph L1["1 · Client"]
        FE["Web UI<br/>React 18 · Vite · TypeScript<br/>xterm.js · Monaco · Recharts · Radix UI"]
        CLI["ocm CLI<br/>Go (separate repo)"]
    end

    subgraph L2["2 · Edge / data plane — Cloudflare"]
        W["Worker (JS)<br/>auth (HS256 JWT) + request routing"]
        KV["Workers KV<br/>hostname → host/VM route map"]
        TUN["Tunnel (cloudflared)<br/>ingress into private hosts"]
        W --- KV
    end

    subgraph L3["3 · Control plane — Go 1.25"]
        API["API<br/>go-chi router"]
        WF["Durable workflows<br/>DBOS"]
        PROXY["LLM proxy<br/>Nebius · OpenRouter · Anthropic · Google"]
        AUTH["Auth<br/>Firebase · Cloudflare Access · dev"]
        DB[("PostgreSQL<br/>pgx · SQL migrations")]
        API --- WF
        API --- PROXY
        API --- AUTH
        API --- DB
    end

    subgraph L4["4 · Host — your Linux box"]
        WA["Worker agent (Go)"]
        ORCH["Firecracker orchestrator<br/>firecracker-go-sdk"]
        WA --- ORCH
    end

    subgraph L5["5 · Sandbox — one per agent"]
        VM["Firecracker microVM (KVM)<br/>own kernel · XFS state · ttyd :7681 · headless browser<br/>runs the OpenClaw agent"]
    end

    L1 --> L2 --> L3
    L3 --> L4 --> L5
```

### The stack, layer by layer

1. **Client.** A **React 18 + TypeScript** single-page app built with **Vite**,
   using **xterm.js** for the live terminal, **Monaco** for code, **Recharts**
   for usage views, and **Radix UI** primitives. The companion **`ocm` CLI** is a
   Go app (in the separate [`mathaix/ocm-cli`](https://github.com/mathaix/ocm-cli)
   repo).

2. **Edge / data plane (Cloudflare).** A **Cloudflare Worker** (JavaScript)
   authenticates each request (HS256 JWT) and looks up which host/VM it belongs to
   in **Workers KV**, then forwards it through a **Cloudflare Tunnel**
   (`cloudflared`) into your private host. This layer is what makes per-VM access
   secure and is optional in local mode.

3. **Control plane (Go 1.25).** The backend serves the API with the **go-chi**
   router, talks to **PostgreSQL** via **pgx** (with SQL migrations), runs
   long-lived operations as **DBOS** durable workflows, and proxies model traffic
   through the built-in **LLM proxy** (Nebius, OpenRouter, Anthropic, Google).
   Auth pluggably supports **Firebase**, **Cloudflare Access**, or a dev mode.
   Ships as a few Go binaries: `server`, `agent`, `authproxy`, `ocm-secrets`.

4. **Host (your Linux box).** A **worker agent** (Go) runs on each enrolled host
   and drives the **Firecracker orchestrator** (via `firecracker-go-sdk`) to boot,
   stop, and reap microVMs on command from the control plane.

5. **Sandbox (one per agent).** Each agent gets its own **Firecracker microVM**
   on **KVM** — its own guest kernel, **XFS**-backed state storage, a **ttyd**
   terminal on port 7681, and a headless browser the agent can drive. The
   **OpenClaw** runtime runs inside.

**Cross-cutting:** request tracing via **Opik** and **OpenTelemetry**, encrypted
per-machine secrets, and built-in backups/snapshots.

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
