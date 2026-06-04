# US Firecracker Host Pricing Sheet

Last updated: 2026-03-08

## Scope

This sheet is US-focused and only includes providers and host SKUs that are plausible Firecracker hosts.

It excludes:

- Hetzner Cloud, because nested virtualization is not supported
- AWS bare metal exact list pricing, because the public static AWS pages were not machine-readable enough in this pass
- OVH `Game` hosts for production OCM use, because the cheapest US `Game-LE-1` has no private bandwidth and only 250 Mbps public bandwidth

## OCM sizing assumptions

The fit counts below are based on the current OCM defaults and scheduling model:

- standard microVM: `2 vCPU`, `2048 MB` RAM
- current host scheduling model allows `2x` CPU oversubscription
- per-VM local storage budget: about `3 GB` rootfs headroom plus `5 GB` data volume

Relevant code:

- default VM size: [`backend/internal/config/config.go`](/Users/mantiz/openclawmachines/backend/internal/config/config.go#L158)
- host CPU oversubscription comment: [`backend/internal/api/server.go`](/Users/mantiz/openclawmachines/backend/internal/api/server.go#L1452)
- per-VM disk sizing assumptions: [`backend/internal/provisioner/provisioner.go`](/Users/mantiz/openclawmachines/backend/internal/provisioner/provisioner.go#L73)

## Fit formula

Under the current OCM model:

- `cpu_fit = host_threads`
- `memory_fit = floor(host_ram_gb / 2)`
- `estimated_microvm_fit = min(cpu_fit, memory_fit)`

For every row below, CPU is the limiting factor. Disk is not the limiting factor on the listed SKUs.

## Best-value summary

| Rank | Provider | SKU | Monthly | Estimated fit | Cost per standard microVM |
|---|---|---|---:|---:|---:|
| 1 | OVHcloud US | Advance-1 2024 | $90 | 12 | $7.50 |
| 2 | OVHcloud US | Advance-2 2024 | $130 | 16 | $8.13 |
| 3 | OVHcloud US | Advance-3 2024 | $197 | 24 | $8.21 |
| 4 | OVHcloud US | Scale-i1 2024 | $420 | 32 | $13.13 |
| 5 | Vultr | AMD EPYC 7443P | $725 | 48 | $15.10 |
| 6 | Vultr | Intel E3-1270 | $120 | 8 | $15.00 |
| 7 | Vultr | Intel E-2286G | $185 | 12 | $15.42 |

## Pricing sheet

| Provider | SKU | Monthly | One-time setup | Cores | Threads | RAM | Storage | Network | Estimated fit | Cost / microVM | Notes |
|---|---|---:|---:|---:|---:|---:|---|---|---:|---:|---|
| OVHcloud US | Advance-1 2024 | $90 | $0 | 6 | 12 | 32 GB | 2 x 960 GB to 4 x 7.68 TB NVMe | Public 1-5 Gbps, Private 25 Gbps | 12 | $7.50 | Best small-host value if you want the cheapest production-capable US option |
| OVHcloud US | Advance-2 2024 | $130 | $0 | 8 | 16 | 64 GB | 2 x 960 GB to 4 x 7.68 TB NVMe | Public 1-5 Gbps, Private 25 Gbps | 16 | $8.13 | Strong baseline OCM host |
| OVHcloud US | Advance-3 2024 | $197 | $0 | 12 | 24 | 64 GB | SSD NVMe | Public 1-5 Gbps, Private 25 Gbps | 24 | $8.21 | Cheapest per-fit step up from 16 to 24 microVMs |
| OVHcloud US | Scale-i1 2024 | $420 | $0 | 16 | 32 | 128 GB | SSD NVMe | Public 1-25 Gbps, Private 50 Gbps | 32 | $13.13 | Better network and higher-end platform; no longer the cheapest density play |
| Vultr | Intel E3-1270 | $120 | $0 | 4 | 8 | 32 GB | 2 x 240 GB SSD | 10 Gbps, 5 TB bandwidth | 8 | $15.00 | Lowest Vultr entry price |
| Vultr | Intel E-2286G | $185 | $0 | 6 | 12 | 32 GB | 2 x 960 GB SSD | 10 Gbps, 5 TB bandwidth | 12 | $15.42 | Better storage than E3-1270, same fit class as OVH Advance-1 |
| Vultr | Intel E-2388G | $350 | $0 | 8 | 16 | 128 GB | 2 x 1.92 TB NVMe | 10 Gbps, 10 TB bandwidth | 16 | $21.88 | Memory-rich but still CPU-limited under current OCM sizing |
| Vultr | AMD EPYC 7443P | $725 | $0 | 24 | 48 | 256 GB | 2 x 480 GB SSD + 2 x 1.92 TB NVMe | 25 Gbps, 10 TB bandwidth | 48 | $15.10 | Best Vultr density value among the larger boxes |
| Vultr | AMD EPYC 9254 | $825 | $0 | 24 | 48 | 384 GB | 2 x 480 GB SSD + 2 x 1.92 TB NVMe | 25 Gbps, 10 TB bandwidth | 48 | $17.19 | Extra RAM does not increase fit for the current 2 GB VM profile |
| Vultr | AMD EPYC 9354P | $1,450 | $0 | 32 | 64 | 768 GB | 2 x 480 GB SSD + 4 x 6.4 TB NVMe | 25 Gbps, 10 TB bandwidth | 64 | $22.66 | Premium headroom; more expensive per standard microVM than smaller boxes |

## Reading the results

### Cheapest production-capable US option

`OVH Advance-1 2024` at roughly `$7.50` per standard microVM per month.

### Best baseline OCM host

`OVH Advance-2 2024`

- enough RAM and storage headroom
- 16 standard microVMs
- still near the bottom of the cost curve

### Best API-driven bare metal option

`Vultr AMD EPYC 7443P`

- 48 standard microVMs
- better cloud-style operations than traditional dedicated servers
- materially more expensive than OVH on a per-microVM basis

## Important caveats

1. These fit counts use current OCM defaults. If you move the default microVM to `4 GB` RAM, memory-limited fit is cut in half.
2. This sheet does not model network egress cost beyond the included transfer/bandwidth on each host.
3. This sheet does not model spare capacity for control-plane overhead, noisy neighbors, or browser companion VMs.
4. OVH 2026 server refreshes are also listed on current pricing pages, but the currently visible 2024 SKUs have the better recurring economics.
5. AWS bare metal is still a strong technical option, but I did not include exact static pricing here because the public AWS pages were not easy to extract reliably in this pass.

## Sources

- Vultr Bare Metal product page: https://www.vultr.com/products/bare-metal/
- Vultr pricing page: https://www.vultr.com/pricing/
- OVHcloud US full dedicated range: https://us.ovhcloud.com/bare-metal/prices/
- OVHcloud US Advance range: https://us.ovhcloud.com/bare-metal/advance/
- OVHcloud US Scale range: https://us.ovhcloud.com/bare-metal/scale/
- OVHcloud US region availability: https://us.ovhcloud.com/bare-metal/regions-availability/
