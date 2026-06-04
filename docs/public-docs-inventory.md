# Public Docs Inventory

Date: 2026-06-04 UTC

This inventory tracks documentation readiness for the Apache-2.0 public core.
It is intentionally an inventory, not a scrub: documents listed as rewrite or
private candidates should stay in place until maintainers review them.

## Public-Core Boundary

Public-core scope:

- Companion CLI and developer tooling, maintained in `mathaix/ocm-cli`.
- Firecracker microVM runtime, worker agents, rootfs, and host enrollment.
- Minimum control plane needed for local, BYO-host, or operator-hosted
  deployments.
- Public security, testing, release, and contribution workflows for the core.

Private overlay scope:

- Billing, plan enforcement, commercial admin, pricing, launch, sales, and
  hosted-product packaging.
- Enterprise-only hosted flows and confidential infrastructure notes.
- Private customer, partner, go-to-market, or competitive material.
- Internal worker coordination notes that are not project documentation.

## Public Ready

These documents are acceptable as public-core entry points after this patch.

| Path | Notes |
| --- | --- |
| `README.md` | Public-core overview, Apache-2.0 framing, and links to OSS hygiene docs. |
| `LICENSE` | Apache-2.0 license text. |
| `CONTRIBUTING.md` | Contribution workflow, DCO, testing expectations, and private-overlay boundary. |
| `SECURITY.md` | Private vulnerability reporting guidance for the public core. |
| `CODE_OF_CONDUCT.md` | Baseline conduct and reporting expectations. |
| `docs/public-docs-inventory.md` | This readiness inventory. |

## Needs Rewrite Before Public Release

These documents contain useful core material, but currently mix public-core
concepts with hosted-product assumptions, cloud-specific defaults, stale
implementation notes, or internal planning context. They should be rewritten or
split before being presented as public docs.

| Path or pattern | Rewrite needed |
| --- | --- |
| `docs/architecture.md`, `docs/sequence-diagrams.md`, `docs/auth-sequence-diagrams.md` | Separate public self-host/operator architecture from managed-edge assumptions, billing entities, and specific private provider choices. |
| `docs/configuration.md`, `docs/config-lifecycle.md`, `docs/configuration_architecture.md`, `docs/vm-config-lifecycle.md`, `docs/openclaw-config-source-proposal.md` | Clarify public config surfaces and remove hosted credential-injection assumptions unless they are documented as optional operator integrations. |
| `docs/routing.md`, `docs/tunnel-architecture.md`, `docs/terminal_connectivity.md`, `docs/auth-flow.md`, `docs/unified-auth-rearchitecture.md` | Rewrite around public authentication and routing options; keep Cloudflare/Firebase/Okta specifics as optional examples only. |
| `docs/host-enrollment.md`, `docs/ovh-host-setup.md`, `docs/ovh-vps-direct-provisioning.md`, `docs/vm-provisioning.md`, `docs/provisioning_logic.md`, `docs/placement-logic-analysis.md` | Keep BYO-host and provider-neutral material; split out managed GCP provisioning and private infrastructure notes. |
| `docs/rootfs-design.md`, `docs/build-process.md`, `docs/openclaw-version-management.md`, `docs/RECOVERY_AND_PERSISTENCE.md` | Convert from internal release/runbook notes into operator-facing build, upgrade, and recovery docs. |
| `docs/cli-integrations.md`, `docs/CLI-UIfeedback.md`, `docs/cli-design-prototype.html` | Treat as source material for the separate `mathaix/ocm-cli` project; do not present as main-repo CLI docs. |
| `docs/TESTING.md`, `docs/testing-strategy.md`, `docs/debugging-tips.md`, `docs/runbook.md` | Keep public checks, KVM requirements, and troubleshooting; remove production-only or private-deployment assumptions. |
| `docs/hardening.md`, `docs/SECURITY_HARDENING.md`, `docs/security-review.md`, `docs/authorization_bug.md`, `docs/pr16_review.md` | Preserve public security lessons, but rewrite issue-specific or sensitive review notes into generalized guidance. |
| `docs/designs/*.md`, `docs/design/*.md`, `docs/plans/*.md`, `docs/bugs/*.md`, `docs/debug/*.md`, `docs/research/*.md` | Treat as source material. Promote only docs that describe public-core behavior without commercial, private, or stale assumptions. |
| `docs/docs.html`, `docs/architecture-diagram.html`, `docs/agent-anatomy.html`, `docs/memory-setup.html` | Review generated/static artifacts for accuracy and public-core scope before linking. |

## Should Stay Private Or Internal

These documents should not be promoted as public-core docs unless they are
substantially rewritten to remove private-overlay scope. Keep them for review;
do not bulk-delete them in this issue.

| Path or pattern | Reason |
| --- | --- |
| `docs/public-core-split-status.md` | Internal readiness and worker-coordination status; useful during the split, not end-user documentation. |
| `docs/us_firecracker_host_pricing.md`, `docs/us_firecracker_host_pricing.csv`, `docs/cost-analysis.md` | Pricing and margin analysis belongs to private commercial planning unless rewritten as neutral operator cost examples. |
| `docs/designs/account-credentials-and-billing.md`, `docs/designs/vm-billing-flow.md`, `docs/designs/platform-models-nebius.md`, billing-related `docs/superpowers/**` files | Billing, usage metering, managed model services, and plan enforcement are private-overlay concerns. |
| `docs/opportunities.md`, `docs/propertymanagement.md`, `docs/generalcontractor.md`, `docs/property-management-usecase.html`, `docs/physical-therapy-usecase.html`, `docs/slides/ai-employees-deck.md` | Go-to-market, vertical-market, pricing, and pitch material are not public-core project docs. |
| `docs/proposals/white-label-partners.md`, white-label references in design docs | Partner, branding, and commercial packaging material belongs outside the public core. |
| `docs/GatewayDashboardDesign.md`, `docs/CurrentFeatureWorkflow.md`, `docs/IntegrationsHubDesign.md` | Product/UI planning should be rewritten before publication and checked for hosted/private assumptions. |
| `docs/superpowers/**` | Most files describe private feature planning, managed integrations, billing, or internal product work. Re-promote individual files only after review. |
| Raw review logs such as `docs/plans/*raw.txt` | Internal review artifacts; not suitable as public docs. |

## Review Rules

- Do not delete large documentation sets as part of inventory work.
- Before promoting a document, verify that it describes public-core behavior and
  does not require billing, commercial hosted plans, or private-overlay services.
- When a doc has both public and private content, split it: keep the public-core
  operator guidance in `docs/`, and move or rewrite the private-overlay material
  outside the public-core docs set.
- Prefer neutral terms such as "operator-hosted" or "self-hosted" for public
  core. Use "managed hosted product" only when explicitly describing private
  overlay scope.
