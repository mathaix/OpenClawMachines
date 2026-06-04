# Cost Comparison: GCP vs Hetzner for OpenClaw Machines (Feb 2026 snapshot)

Goal: pick the lowest-cost host type that still meets OpenClaw Machines’ needs (Firecracker hosts running multiple microVMs). Prices here are public list prices as of Feb 2026 and should be refreshed before committing to a rollout.

## Assumptions (scenario A)
- 8–10 microVMs per host (typical: 2 vCPU / 4 GB each)
- Host needs ~8 vCPU / 32 GB RAM, ~400 GB SSD, ~2 TB outbound transfer per month
- On‑demand pricing, no commitments, single region

## Price snapshot (public rates)

| Provider & SKU | vCPU / RAM | Storage | Egress | Monthly (est.) |
| --- | --- | --- | --- | --- |
| **GCP e2-standard-8 (on‑demand)** | 8 / 32 GB | 400 GB PD Balanced @ ~$0.10/GB‑mo | 2 TB @ ~$0.12/GB | **~$476** |
|  |  |  |  | Breakdown: compute ~$195, storage ~$40, egress ~$240 |
| **Hetzner AX52‑NVMe** | 8C / 64 GB | 2×512 GB NVMe (RAID1 optional) | 20 TB included (extra ~€1/TB) | **~€47 ≈ $51** |
|  |  |  |  | Add €10 buffer for backups/IPs → **~$56** |

> Note: Currency conversion assumed €1 = $1.08. GCP rates are typical US regions; egress varies by destination. Always verify the live price sheet.

## Unit economics (scenario A)
- **GCP:** ~$476 / host ÷ 8 microVMs ≈ **$59.5 per microVM per month**
- **Hetzner:** ~$56 / host ÷ 8 microVMs ≈ **$7 per microVM per month**

Even after adding 20–30% buffer for backups, monitoring, and IPs, Hetzner remains ~6–8× cheaper per microVM than GCP on on‑demand rates.

## Sensitivity
- **Egress‑heavy workloads:** GCP egress dominates quickly; Hetzner includes 20 TB, so light/medium traffic is effectively free.
- **Burst CPU:** If CPU steal becomes an issue on shared cloud, consider Hetzner dedicated (AX series) or GCP CUDs/spot for price relief.
- **Compliance/regions:** If you need specific regions/compliance (HIPAA/FedRAMP), GCP may be required despite higher cost.

## Recommendations
1) Default to Hetzner (AX‑NVMe) for non‑regulated EU/US workloads; target 8–10 microVMs per host, monitor CPU steal and I/O.  
2) Keep a GCP footprint for compliance/latency needs; use committed use discounts or spot if allowed.  
3) Refresh prices quarterly and before any large scale‑up; recalc with real egress and host density data.  
4) Automate a simple calculator (inputs: host type, density, egress) to keep per‑microVM cost current.  

## Action items
- Verify current list prices (compute, PD, egress) before rollout.
- Decide standard host SKUs (Hetzner AX52‑NVMe for cost; GCP e2-standard‑8 for compliance).
- Add a CI check or spreadsheet to recompute per‑microVM cost when host density or traffic assumptions change.

## Overcommit / Ballooning Impact
- On Hetzner AX52‑NVMe (8C/64 GB), modest CPU overcommit (≈1.5×) and RAM headroom let you raise density from 8 → 12–14 microVMs (2 vCPU / 4 GB). COGS drops to ~$4.0–$4.7 per microVM → margins improve to ~69–87% on current pricing.
- On GCP e2-standard-8 (8 vCPU / 32 GB), RAM is the limiter; even with CPU overcommit, fitting 9–10 small VMs brings COGS to ~$48–$53 per VM—still above Starter ($15) and Pro ($30) pricing. GCP remains non-viable for these price points unless paired with higher prices and/or CUD/spot plus higher density.

## Pricing vs. COGS (using current site pricing)
Assumes 8 microVMs/host (scenario A above). COGS excludes corporate overhead/support; change host density to re-run.

| Plan (monthly) | Price | COGS/microVM | Gross Margin | Notes |
| --- | --- | --- | --- | --- |
| Starter (2 vCPU / 2 GB) | $15 | **GCP:** $59.5 | **‑298%** | Not viable on GCP without CUDs/spot & higher density |
|  |  | **Hetzner:** $7 | **53%** | Viable on Hetzner at 8/host |
| Pro (4 vCPU / 4 GB) | $30 | **GCP:** $59.5 | **‑98%** | Not viable on GCP without discounts & density |
|  |  | **Hetzner:** $7 | **77%** | Strong margin on Hetzner |

Annual equivalents (effective monthly: Starter $10, Pro $20):
- Starter annual: margin ≈ 30% on Hetzner; negative on GCP.
- Pro annual: margin ≈ 65% on Hetzner; negative on GCP.

Sensitivity (higher density lowers COGS):
- At 10 microVMs/host: Hetzner COGS ≈ $5.6 → Starter margin ~44% (monthly), Pro ~81% (monthly).
- GCP only becomes positive if you combine higher density + CUD/spot + higher pricing; otherwise keep GCP for compliance/latency premium plans.
