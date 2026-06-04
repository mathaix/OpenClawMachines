# Browser Behavior Analysis: `v2026.4.8` vs `v2026.4.14`

## Summary

This analysis started from the report:

- Browser works with `v2026.4.8`
- Browser does not work with `v2026.4.14`
- The same newer agent build can still run a machine provisioned with `v2026.4.8`
- `v2026.4.14` works when local browser pairing is used

The last two points are important because they rule out the systemd runtime-owner refactor and most agent-side browser transport changes as the primary cause.

The strongest current conclusion is:

- This does **not** look like an agent/systemd regression.
- This also does **not** look like a fundamental browser VM transport regression.
- It looks like a **behavior change in the OpenClaw app/runtime path** between `v2026.4.8` and `v2026.4.14`, specifically around how "browser" is enabled and configured.

## What Changed

Between April 8 and April 14, 2026, the browser feature model changed substantially.

### `v2026.4.8` behavior

The Browser tab was a built-in machine capability toggle:

- `listMachineCapabilities`
- `enableMachineCapability`
- `disableMachineCapability`
- `pushMachineConfig`

Reference from the April 8 code:

- [frontend/src/pages/machine-tabs/BrowserTab.tsx](/home/mantiz/OpenClawMachines/frontend/src/pages/machine-tabs/BrowserTab.tsx:1) in commit `3200b41` showed the old built-in-browser toggle flow.

The user-facing model was:

- Turn browser on for this machine
- Push config
- Use the built-in managed browser path

### `v2026.4.14` behavior

The Browser tab is no longer a built-in toggle. It is a browser-VM pairing flow:

- `listBrowserVMs`
- `createBrowserVM`
- `startBrowserVM`
- `pairBrowser`
- `unpairBrowser`
- `getBrowserVM`

Current code:

- [frontend/src/pages/machine-tabs/BrowserTab.tsx](/home/mantiz/OpenClawMachines/frontend/src/pages/machine-tabs/BrowserTab.tsx:21)

The user-facing model is now:

- Create or select a standalone browser VM
- Ensure it is running on the same host
- Pair it with the machine
- Inject browser config from that pairing

## Config Assembly Change

The browser config contract changed on April 9, 2026 in commit `efd975d`:

> feat(config): derive browser block from pairing instead of capability toggle

Relevant current code:

- [backend/internal/configassembly/assembler.go](/home/mantiz/OpenClawMachines/backend/internal/configassembly/assembler.go:479)
- [backend/internal/api/machine_config.go](/home/mantiz/OpenClawMachines/backend/internal/api/machine_config.go:691)

Current behavior:

- Browser config is emitted only when a paired browser VM exists and is runnable.
- `machine.BrowserVMID` is resolved to a browser VM IP.
- If there is no paired/running browser VM, the browser block is omitted.

This differs from the old `v2026.4.8` behavior, where the browser capability itself was the main switch.

## Why The Agent/Systemd Theory Does Not Fit

The following user observations materially weaken the systemd/agent theory:

1. A machine provisioned with `v2026.4.8` still works on the newer agent.
2. `v2026.4.14` works when local browser pairing is used.

If the main issue were the runtime-owner/systemd refactor, we would expect failures to follow the agent/runtime lifecycle regardless of the OpenClaw version. That is not what the report shows.

Instead, the observed pattern matches an app-level behavior difference:

- `v2026.4.8` can still use the older built-in browser flow.
- `v2026.4.14` succeeds once the newer explicit pairing flow is satisfied.

## Most Likely Interpretation

The most likely interpretation is that `v2026.4.14` is not broken in a generic sense. Rather:

- `v2026.4.14` expects explicit local browser pairing
- `v2026.4.8` still works through the older built-in browser path

So the apparent regression may actually be:

- a product/runtime behavior transition
- a missing migration of user expectations/UI semantics
- or a missing compatibility layer for the old built-in browser flow

## Remaining Uncertainty

There is still one important open question:

- Is the problematic `v2026.4.14` case failing because no local browser pairing exists, or because some specific config/runtime delivery step is still inconsistent even after pairing?

The user reported that `v2026.4.14` works with local browser pairing, which strongly suggests the first explanation. But this should still be verified against logs if a failing machine is available.

## Less Likely Secondary Candidates

I also reviewed guest/runtime packaging changes between April 8 and April 14:

- artifact-only runtime path changes
- stricter plugin directory checks in [scripts/init-openclaw.sh](/home/mantiz/OpenClawMachines/scripts/init-openclaw.sh:351)
- `ocmptyd` replacing `agent --pty-server`
- rootfs bind-mount changes
- Python 3 added for gateway pinned writes

These are real changes, but they are less likely to explain the browser-only symptom because:

- they would usually affect broader machine startup/runtime behavior
- they do not align as well with the reported fact that `v2026.4.14` works once local browser pairing is used

## Working Conclusion

Current working conclusion:

- The reported `v2026.4.8` vs `v2026.4.14` difference is best explained by the browser feature moving from a built-in machine capability to a browser-VM pairing model.
- The newer agent/systemd changes are probably not the root cause.
- The key code change is the April 9, 2026 browser config transition from capability-driven to pairing-driven behavior.

## Useful References

- [frontend/src/pages/machine-tabs/BrowserTab.tsx](/home/mantiz/OpenClawMachines/frontend/src/pages/machine-tabs/BrowserTab.tsx:21)
- [backend/internal/configassembly/assembler.go](/home/mantiz/OpenClawMachines/backend/internal/configassembly/assembler.go:479)
- [backend/internal/api/machine_config.go](/home/mantiz/OpenClawMachines/backend/internal/api/machine_config.go:691)
- [scripts/init-openclaw.sh](/home/mantiz/OpenClawMachines/scripts/init-openclaw.sh:351)

## Suggested Next Verification

If this needs to move from analysis to proof, the next checks should be:

1. Compare a failing `v2026.4.14` machine and a working `v2026.4.14` machine for `browser_vm_id` presence and pairing state.
2. Compare assembled config output for both machines and confirm whether the `browser` block is present.
3. Check whether the user expectation is still "built-in browser toggle" while the runtime now requires explicit browser VM pairing.
