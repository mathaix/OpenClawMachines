# Public Docs Inventory

Date: 2026-06-04 UTC

This file records the documentation set kept in the Apache-2.0 public core
after the split from the private hosted overlay.

## Public-Core Docs Kept

| Path | Scope |
| --- | --- |
| `README.md` | Public-core overview and link to the separate `ocm` CLI repository. |
| `CONTRIBUTING.md` | Contribution workflow, testing expectations, and public/private boundary. |
| `SECURITY.md` | Vulnerability reporting for the public core. |
| `CODE_OF_CONDUCT.md` | Community conduct and reporting expectations. |
| `docs/getting-started.md` | Three-stage setup guide: local evaluation, Cloudflare + dedicated host, full workflow. |
| `docs/user-guide.md` | End-user guide for using a machine: model setup, chat, terminal, browser VM, files, logs, traces, backups, troubleshooting. |
| `docs/workspace-integrations-mcp.md` | Workspace-scoped integrations and native MCP behavior, agent tool flow, policy, operator requirements, and troubleshooting. |
| `docs/tech-stack.md` | The five implementation layers, from browser to sandbox (moved out of `README.md`). |
| `docs/local-setup.md` | Local and BYO-host setup expectations. |
| `docs/getting-started.md` | Three-stage guide: local evaluation, Cloudflare + dedicated host, full workflow. |
| `docs/building.md` | Component-by-component build manual and the GCS artifact bucket layout. |
| `docs/tech-stack.md` | The five-layer stack, client to sandbox (moved out of the README). |
| `docs/cli.md` | Relationship to the separate `mathaix/ocm-cli` Apache-2.0 project. |
| `docs/ci-release.md` | CI and release safety boundaries for public-core changes. |
| `docs/kvm-integration-ci.md` | Maintainer-gated KVM integration lane for public PRs and `main` pushes. |
| `docs/control-plane-profiles.md` | Local, operator, and hosted profile semantics without private defaults. |
| `docs/self-hosted-control-plane.md` | Production-like self-hosted prerequisites and validation flow. |
| `docs/self-hosted.env.example` | Redacted self-hosted operator environment template. |
| `docs/overlay-boundary.md` | Public-core vs private-overlay ownership rule. |
| `llms/self-hosted-setup.txt` | Copy-pasteable LLM operator runbook for self-hosted setup. |

## Removed From Public Core

The scrub removed internal planning docs, raw review logs, product prototypes,
go-to-market material, pricing and billing analysis, private deployment
runbooks, and stale CLI design notes that belong in `mathaix/ocm-cli` or a
private overlay repository.

Examples of removed categories:

- `docs/design/**`, `docs/designs/**`, `docs/plans/**`, `docs/superpowers/**`,
  `docs/bugs/**`, `docs/debug/**`, and `docs/research/**`.
- Commercial, pricing, partner, vertical-market, launch, and pitch material.
- Production deployment assumptions, private hosted infrastructure notes, and
  provider-specific runbooks that are not generic operator guidance.
- Frontend blog/docs/prototype content that was marketing or hosted-product
  packaging rather than public-core operation.

## Review Rules

- New public docs must describe local, BYO-host, or operator-hosted public-core
  behavior.
- Do not include billing, plan enforcement, commercial hosted admin, customer
  management, launch funnels, private infrastructure, or private runbooks.
- If a shared primitive is needed by both repos, document the neutral primitive
  here and keep commercial policy or UI in the private overlay.
- CLI behavior belongs in `mathaix/ocm-cli`; this repo should link to it rather
  than duplicate CLI implementation docs.
