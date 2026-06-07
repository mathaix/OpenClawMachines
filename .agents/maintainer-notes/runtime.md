# Runtime Maintainer Decisions

Use this note during reviews that touch machine start/upgrade, route setup,
agent create-VM requests, metadata, guest init, rootfs, runtime artifacts, or
token issuance.

## VM Signing Keys

- Every VM needs a per-machine signing key. This is independent of whether a
  Cloudflare tunnel exists.
- The key must be persisted in `machines.signing_key` before it is used to boot
  or upgrade a VM. Later token issuance reads the database, not only the in-memory
  `store.Machine` value.
- If route-state persistence fails, machine start/upgrade should fail loudly
  instead of booting a VM that cannot later receive machine tokens.
- Empty tunnel IDs are not real tunnel IDs. Store them as `NULL`, while still
  preserving the signing key.

## Local And Operator Runtime

- No-tunnel local/operator VMs are valid. Agent and guest init paths must not
  require `tunnel_token` or `vm_hostname` unless the feature being started
  specifically needs Cloudflare.
- Cloudflared supervision should be skipped when no tunnel token is present.
- Gateway/proxy token verification remains required in local/operator paths.

## Runtime Artifacts

- OpenClaw runtime selection and rootfs/root runtime manifests are deployment
  inputs. Do not guess versions, model IDs, artifact names, or bucket paths.
- Runtime/rootfs changes may require rebuilding or restaging the rootfs image,
  not only changing host-side code.
- The staged base rootfs used by the agent can differ from the source image.
  Verify which artifact a test host actually boots.

## Review Standard

Runtime changes need focused tests near the changed code and, when feasible,
KVM proof for guest-init, Firecracker, networking, persistence, or rootfs
behavior. If KVM proof is unavailable, state the blocker and run the closest
backend/agent/metadata proof.
