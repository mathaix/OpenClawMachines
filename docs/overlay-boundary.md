# Overlay Boundary

OpenClaw Machines public core is the Apache-2.0 project. It should be useful for local development, BYO-host deployments, and operator-hosted installations without relying on Mathaix hosted commercial services.

## Public Core

Public core owns:

- Local setup, operator-run control-plane paths, and API surfaces consumed by the companion `ocm` CLI.
- Firecracker and microVM runtime code needed to run machines.
- Provider-neutral account, machine, credential, observability, and usage telemetry primitives.
- Documentation for self-hosting, local development, security posture, and extension points.
- Neutral usage and cost telemetry when it helps operators understand resource or model consumption without enforcing a commercial hosted plan.

## Private Overlay

The private overlay owns:

- Public pricing pages, plan cards, launch/reservation funnels, and commercial marketing assets.
- Hosted billing provider integration, invoices, subscriptions, trials, coupons, payment methods, and tax handling.
- Plan enforcement, commercial entitlements, quotas, seat packaging, and upgrade/downgrade flows.
- Hosted SaaS admin, partner, reseller, white-label, customer management, and enterprise-only surfaces.
- Hosted infrastructure details, secrets, runbooks, and deployment assumptions that are specific to Mathaix-operated services.

## Rule Of Thumb

If a feature is necessary to run OpenClaw Machines yourself, keep it in public core. If a feature sells, meters, gates, brands, or administers the Mathaix-hosted service, keep it in the private overlay. When a shared primitive is needed by both, keep the primitive provider-neutral in public core and put the commercial policy or UI in the overlay.
