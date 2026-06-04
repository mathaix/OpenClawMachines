# Interactive VNC Browser Experience

**Date:** 2026-04-10
**Branch:** `browser`
**Status:** Draft

## Summary

Add a pure VNC experience for browser VMs so users can watch and take over the same browser session the agent is using. The browser VM runs a real headed desktop session with Chromium. The agent controls Chromium through CDP/Playwright, while the user views and interacts with the same desktop through VNC.

This is not a replacement for CDP. CDP remains the automation interface. VNC becomes the human-visible and human-interactive interface.

## Goals

1. Let users see what the agent is doing in the browser.
2. Let users complete login, SSO, 2FA, CAPTCHA, passkey, consent, and other human-only steps.
3. Keep Firebase as the product authentication source of truth.
4. Avoid a custom screenshot-streaming protocol for the primary interactive browser experience.
5. Keep browser state persistent across agent steps by using a shared Chromium profile.
6. Prevent the agent and user from fighting over the browser during human takeover.

## Non-Goals

1. Do not expose raw VNC publicly.
2. Do not ask the agent to handle user passwords, 2FA codes, passkeys, or CAPTCHAs.
3. Do not make Cloudflare Access the long-term source of product authorization for Firebase users.
4. Do not build a bespoke remote desktop protocol unless noVNC cannot meet requirements.
5. Do not require users to install a native VNC client.

## Architecture

```text
User browser
  Clara web app
    Embedded VNC client
      |
      | WebSocket, authenticated by Clara/Firebase session
      v
Clara VNC gateway
  Authz: account, machine, browser VM, session token
  WebSocket/Web-native VNC routing
      |
      | private network only
      v
Browser VM
  VNC server: KasmVNC preferred; TigerVNC/noVNC fallback
  Desktop: Openbox, XFCE, or similarly lightweight session
  Chromium: headed mode, persistent profile
  CDP: localhost/private port for agent automation
```

The agent and user share one browser instance:

```text
Browser VM
  Desktop session
    Chromium
      Agent controls via Playwright/CDP
      User views/clicks/types via VNC
```

## Recommended Implementation

Use **KasmVNC-style web-native VNC** for v1 if licensing and rootfs packaging are acceptable:

- KasmVNC runs the VNC server and browser-facing web transport together.
- It is optimized for browser access rather than strict classic VNC compatibility.
- It supports WebP/dynamic quality behavior that should perform better than vanilla RFB over WebSockets on high-latency links.
- It reduces moving parts compared with a separate VNC server, websockify bridge, and noVNC client.
- It has a stronger path to multi-user session management than classic single-client VNC.
- Audio is possible through the KasmVNC helper/client path if product requirements need it.

The Clara product should still put its own session gateway in front of KasmVNC. Do not expose the KasmVNC web server directly as the product auth boundary. The Clara gateway should mint short-lived session tokens, enforce Firebase/account authorization, and route to the correct browser VM.

Use **noVNC + websockify-style bridging** as the compatibility fallback:

- `noVNC` runs in the Clara frontend as the HTML5 VNC client.
- A Clara-controlled gateway terminates the user's authenticated WebSocket.
- The gateway verifies the Firebase-authenticated user can access the account, machine, and browser VM.
- The gateway bridges the WebSocket stream to the VNC server's private TCP port.
- The VNC server only listens on localhost inside the VM or on the host-private bridge network.

Cloudflare Tunnel can still be used as infrastructure plumbing, but the long-term product path should not depend on Cloudflare Access as the primary VNC authorization layer.

## Protocol Options

| Option | Protocol | Performance | Audio | Control model | Operational complexity | Fit |
|--------|----------|-------------|-------|---------------|------------------------|-----|
| noVNC + websockify + TigerVNC/Xvnc | RFB over WebSockets | Medium; can feel laggy on high-latency links | No native VNC audio | Usually single active user input | Higher; three components | Compatibility fallback |
| KasmVNC-style web-native VNC | Modified RFB/web-native transport | High; WebP and dynamic quality reduce bandwidth and latency | Possible through helper/client support | Better multi-user/API control model | Lower; server includes web access path | Preferred v1 |
| Neko-style virtual browser | WebRTC media/data channels over UDP | Very high; smooth video path when network allows UDP | Native media support | Built for multi-participant viewing/control | Medium; packaged app, but needs signaling/STUN/TURN and image adaptation | Strong alternative if "pure VNC" is not required |
| Custom WebRTC remote browser streaming | WebRTC media/data channels over UDP | Very high; lowest-latency video path when network allows UDP | Native media support | Product-defined multi-participant model | High; signaling, STUN/TURN, capture, input, auth, and lifecycle | Later option |
| Kernel Images | CDP plus noVNC or WebRTC live view | High in WebRTC mode; VNC fallback available | WebRTC audio currently needs validation/fix | Read/write live browser view, CDP automation, recordings | Medium; open-source image/runtime but must adapt to Clara auth and Firecracker/rootfs | Strongest Clara-owned browser-runtime candidate |
| Browserbase | Hosted browser automation platform with Live View | High; provider-managed | Provider-managed browser session features | Interactive embedded live view for human-in-the-loop | Low for app integration; high vendor dependency | Fastest hosted path |
| Browserless | Self-hosted or hosted browser automation platform | Medium/high depending deployment | Screencast/recording support; live interaction depends feature tier/path | Live debugger and hybrid automation paths | Medium; Docker/platform integration and licensing | Best platform shortcut if self-hosting is preferred |

Decision for v1 if the requirement is literally VNC: prefer KasmVNC-style web-native VNC. Keep noVNC as the fallback if KasmVNC packaging, licensing, or isolation creates problems.

Decision for v1 if the requirement is the best interactive remote browser UX: evaluate Neko in parallel with KasmVNC. Neko is not VNC, but it is closer to a purpose-built shared browser product and may produce a smoother user experience, especially for media/audio and multiple viewers.

## Neko Option

Neko is a self-hosted virtual browser/desktop that runs as a containerized environment and streams the session through WebRTC. It is closer to a hosted watch-party/collaborative browser model than a traditional VNC stack.

Advantages:

- Better perceived latency and motion quality than RFB-over-WebSocket VNC.
- Native audio path through WebRTC.
- Built-in multi-participant viewing and control concepts.
- Existing web UI, room/session model, and browser-focused deployment pattern.
- Apache-2.0 licensed project.

Tradeoffs:

- It is not pure VNC, so it does not satisfy a strict "VNC server plus VNC client" requirement.
- WebRTC requires signaling and reliable STUN/TURN planning, especially for restrictive networks.
- The default packaging assumes Docker/container images; the current browser VM design uses Firecracker/rootfs artifacts, so we would either run Neko inside the browser VM or build a Neko-compatible browser rootfs.
- Product auth still needs to be Clara/Firebase. Neko room passwords or query-string auto-login must not become the product authorization boundary.
- Agent integration must be validated: Chromium must expose CDP/remote debugging in the same browser session users see through Neko.

Recommended Neko spike:

1. Build or run a Neko Chromium image with remote debugging enabled.
2. Verify Playwright/CDP can attach to the same Chromium session being streamed.
3. Verify user input through Neko updates the same browser context the agent observes.
4. Test restrictive-network behavior with and without TURN.
5. Measure CPU, memory, bandwidth, and latency against KasmVNC at the same resolution.
6. Confirm the auth model can be wrapped by Clara session tokens without exposing Neko's room password as a durable secret.

Decision gate: choose Neko over KasmVNC only if the WebRTC smoothness/audio/multi-user benefits outweigh the additional networking and packaging work.

## Browser Automation Platform Options

Browserbase and Browserless are not VNC products. They are browser automation platforms that already expose Playwright/Puppeteer-compatible browser sessions and provide live viewing or human-in-the-loop features.

### Browserbase

Browserbase is the fastest hosted path if Clara is willing to outsource the browser runtime. It provides browser sessions, Playwright/Puppeteer connectivity, and Live View links that can be embedded in an application. Live View supports watching, clicking, typing, and scrolling in real time, which directly maps to the "agent pauses, user logs in, agent resumes" workflow.

Advantages:

- Minimal infrastructure for v1.
- Built-in hosted browser lifecycle.
- Embedded Live View supports human takeover.
- Good fit for proving the user-login handoff quickly.
- Avoids building VNC, WebRTC, STUN/TURN, or browser rootfs plumbing before validating demand.

Tradeoffs:

- Browser runtime is outside the Clara Firecracker/browser VM model.
- User browser profile/session data lives in a vendor-managed environment.
- Cost scales with browser minutes, concurrency, proxy use, and plan limits.
- Deep network assumptions change: the browser is no longer on the same host/private bridge as the Clara machine unless a tunnel/proxy design is added.
- Product behavior depends on Browserbase APIs and session lifetime semantics.

Use Browserbase for:

- Fast product spike.
- Early human-in-the-loop UX validation.
- Workflows where the browser does not need to be colocated with the user's Clara machine.

Do not use Browserbase as the default if the browser must run inside Clara-owned Firecracker infrastructure, share private host networking with machines, or keep profile data entirely on Clara-managed storage.

### Browserless

Browserless is a browser automation server that can be self-hosted in Docker or used as a hosted service. It provides Playwright/Puppeteer-compatible endpoints, queueing/concurrency controls, debugging tools, and live/hybrid automation features depending on deployment and plan.

The value proposition is operational: Browserless packages Chrome/Firefox/WebKit dependencies, fonts, security patches, health checks, crash handling, concurrency, queueing, and cloud/CI hardening so product code can focus on automation instead of browser infrastructure.

Advantages:

- Closer to self-hosting than Browserbase.
- Standard Playwright/Puppeteer clients can connect to it.
- Existing operational model for browser pools, concurrency, health checks, and debugging.
- Could replace a large amount of custom browser orchestration if Clara accepts Docker/container-based browser workers.
- Multi-browser support is possible through Browserless images/endpoints, not only Chromium.

Tradeoffs:

- It is a browser automation service, not a full remote desktop/VNC layer.
- The root deployment model is Docker/browser workers, while Clara's current browser VM design is Firecracker/rootfs-based.
- Licensing/commercial use terms are a hard gate. The public repo lists SSPL-1.0 or Browserless Commercial License, and proprietary commercial/CI use requires a commercial license.
- Human-in-the-loop features may depend on hosted/cloud or specific versions/features.
- Same colocated-network question as Browserbase unless deployed per host alongside Clara machines.

Use Browserless for:

- Self-hosted browser automation pool experiments.
- Replacing custom Playwright/CDP browser lifecycle management.
- A platform shortcut where Docker workers are acceptable.

Do not use Browserless as the default if the primary requirement is a complete remote desktop experience, strict Firecracker isolation per browser VM, or VNC-compatible user access.

### Platform Decision Gate

Run these spikes before finalizing the runtime:

1. **Kernel Images spike:** build the Clara fork, attach Playwright over CDP, verify WebRTC/noVNC live view, and test recording.
2. **Browserbase spike:** create session, attach Playwright, embed Live View, pause for user login, resume automation.
3. **Browserless spike:** self-host, attach Playwright, verify live/hybrid human intervention, measure packaging and licensing fit.
4. **KasmVNC/Neko spike:** run inside the existing browser VM model, verify CDP and user interaction share the same Chromium session.

Choose a hosted platform only if speed-to-market and operational simplicity matter more than owning the browser runtime. Choose KasmVNC/Neko if Clara needs browser sessions to live inside the same infrastructure, account boundary, network model, and billing model as machines.

## Kernel Images Option

Kernel Images is an open-source browser-runtime project for browser automations and web agents. It is not just a generic browser pool: it already combines the core pieces Clara needs for human-in-the-loop browser automation:

- Clara source fork: `https://github.com/mathaix/ocm-kernel-images`
- Upstream reference: `https://github.com/kernel/kernel-images`

- Headful Chromium image.
- CDP endpoint for Playwright/Puppeteer/Browser Use style automation.
- Remote GUI/live view with read/write control.
- noVNC mode for VNC-compatible viewing.
- WebRTC mode for faster live view.
- Replay recording server for browser session video capture.
- Docker execution path.
- Unikraft unikernel execution path.
- Apache-2.0 license.

This makes Kernel Images the best candidate to study before building a custom browser rootfs from scratch. It is especially relevant because its WebRTC implementation is adapted from Neko, but the project is shaped around browser automations and web agents rather than generic watch-party browsing.

Advantages:

- Closest open-source match to Clara's desired architecture: CDP automation plus live human control.
- Supports both noVNC and WebRTC live view, so Clara can test the protocol tradeoff without swapping the whole browser runtime.
- Replay capture is already part of the runtime model.
- Browser sessions can disconnect and reconnect over CDP while the browser keeps running.
- Unikraft notes are directly relevant to fast suspend/resume and snapshot-style browser sessions.

Tradeoffs:

- The repo's primary runnable paths are Docker and Unikraft; Clara currently uses Firecracker rootfs artifacts.
- WebRTC mode still needs STUN/TURN planning, and hosted Unikraft notes call out TURN requirements when UDP cannot be directly exposed.
- Audio in the WebRTC implementation is documented as currently non-functional and must not be assumed for v1.
- The Unikraft example requires substantially more memory than the current 1 GB browser VM sizing.
- Live view URLs must not be public bearer URLs in Clara. Clara must put Firebase-authenticated session authorization in front.
- The runtime must be audited before embedding: process supervision, sandboxing, profile storage, update cadence, and exposed ports.

Recommended Kernel Images spike:

1. Run `images/chromium-headful` in Docker with `ENABLE_WEBRTC=true`.
2. Connect Playwright over CDP to port `9222`.
3. Open the live view and verify read/write user control.
4. Confirm user clicks and typing update the same Chromium session Playwright observes.
5. Run the same test with `ENABLE_WEBRTC=false` to compare noVNC against WebRTC.
6. Enable replay capture and verify recording output.
7. Measure CPU, memory, startup time, bandwidth, and latency.
8. Decide whether to adapt the image into Clara's browser rootfs or run it as a sidecar/containerized browser worker.

Decision gate: if Kernel Images works with Clara auth wrapping and can be adapted to Firecracker/rootfs without too much surgery, prefer it over building a KasmVNC/Neko stack manually.

### Clara Firecracker Rootfs Build

Clara keeps this runtime as a separate browser artifact, distinct from the existing `browser-rootfs.ext4` image:

```text
/var/lib/ocm/images/browser-rootfs.ext4          # current Alpine/headless Chromium CDP image
/var/lib/ocm/images/kernel-browser-rootfs.ext4   # Kernel Images based CDP + live view image
```

Build from the Clara fork:

```bash
git clone https://github.com/mathaix/ocm-kernel-images.git ../ocm-kernel-images
KERNEL_IMAGES_DIR=../ocm-kernel-images make build-kernel-browser-rootfs
```

Or build from an already-created Docker image:

```bash
KERNEL_BROWSER_IMAGE=ocm-kernel-browser-rootfs make build-kernel-browser-rootfs
```

Upload the separate artifact:

```bash
make upload-kernel-browser-rootfs
make show-kernel-browser-rootfs-manifest
```

To test the Kernel image without changing the legacy browser artifact, point the host/agent browser rootfs manifest at the new prefix:

```text
BROWSER_ROOTFS_GCS_MANIFEST=gs://openclawmachines/kernel-browser-rootfs/manifest.json
```

The build exports the Kernel Images Docker filesystem into an ext4 disk and injects `scripts/init-kernel-browser.sh` as `/sbin/overlay-init`. The init script configures Firecracker guest networking, mounts required filesystems, defaults `ENABLE_WEBRTC=true` and `WITH_KERNEL_IMAGES_API=true`, then delegates to Kernel Images' `/wrapper.sh`.

## Cloudflare Access Option

Cloudflare Access browser-rendered VNC is useful for internal testing or an early prototype:

```text
User -> Cloudflare Access -> Cloudflare browser-rendered VNC -> VNC server
```

This gives a fast no-client VNC URL, but it introduces a second auth gate outside Firebase. If Firebase users sign in with Google, GitHub, Microsoft, or another OIDC provider, Cloudflare Access can use the same upstream provider and users may experience SSO. It is still a separate session and policy system.

Use Cloudflare Access for:

- Internal admin access.
- Prototype validation.
- Emergency break-glass access.
- Hosted VNC pages that do not need Clara-native account/machine authorization.

Do not use Cloudflare Access as the primary product UX if the decision requires Clara-specific checks such as account membership, machine ownership, billing state, plan limits, or session ownership.

## Authentication And Authorization

Firebase remains the source of truth for Clara product access.

Product flow:

1. User logs into Clara with Firebase.
2. User opens a machine's Browser tab.
3. Frontend requests a short-lived VNC session from the API.
4. API verifies:
   - Firebase identity is valid.
   - User belongs to the account.
   - User can access the target machine.
   - Machine is paired with the target browser VM.
   - Browser VM is running and on the same host as the machine.
   - Account billing/plan state allows interactive browser use.
5. API returns a short-lived VNC session token or signed WebSocket URL.
6. The embedded VNC client connects to the Clara VNC gateway using that token.
7. Gateway revalidates the token and opens the private VNC TCP connection.

Session tokens should be scoped to one browser VM, one user, one account, and one short lifetime. They should not be reusable across browser VMs.

## User Interaction Model

The UI should expose explicit control state:

| State | Meaning |
|-------|---------|
| `agent_running` | Agent owns browser input. User can watch. |
| `waiting_for_user` | Agent paused because human action is needed. User can interact. |
| `user_controlling` | User has active input control. Agent must not click or type. |
| `agent_resuming` | User is done; agent is regaining control. |

Rules:

- Agent automation must pause before asking the user to log in or solve a human-only step.
- While `user_controlling`, the agent must not send mouse or keyboard events.
- The user can manually release control when finished.
- The system may auto-release control after inactivity, but only if no sensitive prompt is active.
- The agent resumes using the same Chromium profile and page state.

## Login And Sensitive Steps

The expected login flow:

1. Agent navigates to a website.
2. Website requests login, SSO, 2FA, CAPTCHA, passkey, or consent.
3. Agent emits a "waiting for user" browser event.
4. UI opens or highlights the VNC view.
5. User completes the step inside the remote browser.
6. User clicks "Done" or the agent detects that the expected page/session state is available.
7. Agent resumes via CDP.

Security rule: the agent should not read, store, or synthesize user credentials. The user enters credentials directly into the remote browser over VNC.

## Browser VM Runtime

Each interactive browser VM should include:

- VNC server: KasmVNC preferred; TigerVNC/Xvnc fallback.
- Desktop: Openbox for minimal footprint, or XFCE/LXQt if user ergonomics matter more.
- Chromium: headed mode.
- Chromium profile: persistent directory on the browser VM data volume.
- CDP endpoint: private endpoint for the paired machine/agent only.
- VNC endpoint: private endpoint for the Clara VNC gateway only.

Suggested ports:

| Service | Port | Exposure |
|---------|------|----------|
| CDP | `9222` | Paired machine/agent only |
| VNC/web-native VNC | `5900`, KasmVNC HTTPS/WebSocket port, or per-session display port | Clara VNC gateway only |
| Clara VNC gateway | HTTPS/WebSocket | Clara app/API edge |

## Networking

The VNC server must not be reachable from the public internet.

Allowed paths:

```text
Agent machine -> Browser VM CDP port
Clara VNC gateway -> Browser VM VNC port
```

Denied paths:

```text
Public internet -> Browser VM VNC port
Other machines -> Browser VM VNC port
Unpaired machines -> Browser VM CDP port
```

Firewall rules should distinguish CDP pairing from VNC viewing. Pairing a machine to a browser VM should allow CDP, not necessarily VNC. VNC access should be controlled by Clara's VNC gateway session authorization.

## API Surface

Add session-oriented APIs on top of the existing browser VM lifecycle:

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/api/accounts/{accountId}/browser-vms/{browserVmId}/vnc-sessions` | Create short-lived VNC session |
| `DELETE` | `/api/accounts/{accountId}/browser-vms/{browserVmId}/vnc-sessions/{sessionId}` | End VNC session |
| `GET` | `/api/accounts/{accountId}/browser-vms/{browserVmId}/vnc-sessions/{sessionId}` | Session status |

Create response:

```json
{
  "session_id": "uuid",
  "browser_vm_id": "uuid",
  "websocket_url": "wss://app.example.com/api/vnc/session-token",
  "expires_at": "2026-04-10T12:34:56Z",
  "control_state": "agent_running"
}
```

The WebSocket URL should not encode long-lived credentials. Prefer an opaque, short-lived token stored server-side.

## Frontend UX

Machine Browser tab:

- Show paired browser VM status.
- Show VNC viewer when a browser VM is running.
- Show clear control state: agent running, waiting for user, user controlling.
- Provide "Take control", "Release control", and "Done" actions.
- Show connection health and reconnect affordance.
- Keep the VNC viewer embedded in the app, not opened as an unrelated public URL.

Global Browser VMs page:

- Keep lifecycle controls: create, start, stop, delete.
- Show whether interactive VNC is available.
- Link to the paired machine's Browser tab for user-facing interaction.

## Agent Contract

The agent needs a small contract with the UI/control plane:

```text
browser.wait_for_user(reason, url, browser_vm_id)
browser.user_control_started(browser_vm_id)
browser.user_control_finished(browser_vm_id)
browser.resume(browser_vm_id)
```

When the agent calls `browser.wait_for_user`, the control plane sets the session state to `waiting_for_user`. The UI can then foreground the VNC viewer and notify the user.

The agent must treat `waiting_for_user` and `user_controlling` as input locks. It may observe page state through CDP if safe, but it must not drive the mouse, keyboard, or form input until control is released.

## Persistence

Use a persistent Chromium profile directory per browser VM:

```text
/var/lib/openclaw/browser-vms/{browser_vm_id}/chromium-profile
```

This allows:

- Login cookies to survive agent resume.
- The user to complete auth once per website/session when allowed by the site.
- Agent retries to continue from the same browser state.

Browser profile data should be treated as sensitive account data. It must be deleted when the browser VM is destroyed, unless the product explicitly supports browser profile retention.

## Observability

Track:

- VNC session created/ended.
- User took control/released control.
- Agent paused/resumed.
- VNC connection failures.
- VNC bytes in/out if needed for cost visibility.
- Browser VM CDP ready/error state.
- Login wait duration.

Do not log VNC framebuffer contents, keystrokes, clipboard contents, passwords, or page text from sensitive sessions.

## Security Considerations

- Generate per-session VNC credentials or use a gateway-side token so no shared VNC password reaches the frontend.
- Scope session tokens tightly and expire them quickly.
- Revoke active VNC sessions when browser VM stops, account access changes, or the paired machine is unpaired.
- Disable or tightly control clipboard sync for v1.
- Avoid browser password manager prompts saving credentials into shared profiles unless explicitly intended.
- Never expose VNC directly through host firewall or public DNS.
- Treat screenshots and recordings as sensitive if added later.

## Open Questions

1. Should v1 use Openbox for minimal resource use or XFCE for better human ergonomics?
2. Should VNC access be allowed only from the paired machine page, or also from the global Browser VMs page?
3. Should browser profiles persist across browser VM stops, or only during a single active session?
4. Should the first version disable clipboard sync entirely?
5. Should Cloudflare Access remain as an admin-only path even after the Clara-native gateway exists?
6. What explicit event should the agent wait for after user login: URL match, DOM condition, cookie presence, or user "Done" only?
7. Does KasmVNC's license and dependency footprint fit the browser rootfs distribution model?
8. Is KasmVNC's helper-app audio path worth supporting in v1, or should audio be explicitly deferred?
9. Does Neko's WebRTC model provide enough UX improvement to justify moving away from pure VNC?
10. Can Neko be adapted cleanly to the Firecracker browser VM/rootfs model, or would it force a container-first browser runtime?
11. Should Clara validate Browserbase first as a hosted product spike before committing to browser VM infrastructure?
12. Can Browserless be self-hosted on Clara hosts without weakening Firecracker isolation or complicating licensing?
13. Can Kernel Images be adapted into Clara's browser rootfs, or should Clara run it as a separate browser-worker runtime?
14. Is Kernel Images' WebRTC live view mature enough for product use once wrapped with Clara auth?

## Decision

For product UX under a strict VNC requirement, use KasmVNC-style web-native VNC behind Clara's Firebase-authenticated API/session authorization. Keep CDP for automation. Use VNC for human visibility and takeover. Keep noVNC + websockify as the compatibility fallback. Use Cloudflare Access browser-rendered VNC only for prototype, admin, or break-glass access where a second auth gate is acceptable.

If the product requirement is "best shared interactive browser" rather than "must be VNC", run a Neko spike before finalizing. Neko may be the better user experience, but it changes the architecture from VNC to WebRTC.

If the product requirement is "fastest reliable human-in-the-loop browser automation", run Browserbase and Browserless spikes too. They may solve the product workflow faster than owning browser VM infrastructure, but they change the architecture from Clara-owned browser machines to browser automation platform sessions.

If the product requirement is "own the browser runtime but avoid inventing it", run Kernel Images first. It appears to package the exact primitives Clara needs: CDP automation, live view, noVNC/WebRTC modes, and recording.

## References

- KasmVNC: https://kasm.com/kasmvnc
- Neko: https://github.com/m1k1o/neko
- Clara Kernel Images fork: https://github.com/mathaix/ocm-kernel-images
- Kernel Images: https://github.com/kernel/kernel-images
- Browserbase: https://docs.browserbase.com/fundamentals/using-browser-session
- Browserless: https://github.com/browserless/browserless
- noVNC: https://github.com/novnc/noVNC
- websockify: https://github.com/novnc/websockify
