# Mascot 3D — Lattice + Spin-Settle Animation: 25-Iteration Log

Goal: a 3D version of **m5-happy-machine** (the in-product machine avatar from
issue #28) — body rebuilt as an orange wireframe **lattice** superellipsoid,
with a **spin → decelerate → overshoot → settle** entrance animation.
Direction: *"hologram of a machine"* — the microVM squircle as glowing
structure, face as solid geometry riding the lattice.

Harness: `mascot3d.html` exposes deterministic `renderAt(t)`;
`render-frames.mjs` captures spin/decel/overshoot/settled frames into
`renders/iNN.png` contact sheets. Same judge-from-renders discipline as the
logo rounds in `../design-process.md`.

Video pipeline: [HyperFrames](https://github.com/heygen-com/hyperframes)
renders the composition to deterministic MP4/GIF (`npx hyperframes render`,
needs `index.html` — copied from `mascot3d.html` — plus FFmpeg + Node 22).
Integration is two-sided: `window.__hf = {duration, seek}` for the raw engine
protocol, and an `hf-seek` CustomEvent listener for the CLI runtime (the same
hook its Three.js adapter uses; the CLI's injected bridge overrides a page's
own `__hf.seek`, so the event path is the one that matters there). Duration
comes from `data-duration` on the `data-composition-id` root. FFmpeg filmstrips
of the MP4 (`mascot3d-strip.png`) judge the *motion*; contact sheets judge the
*pose*.

| # | Change | Read of the render | Verdict |
|---|---|---|---|
| 1 | Scaffold: superellipsoid (n=4) param wireframe, depth-shaded orange lines, face anchored to front surface with normal-based fade, spin curve = ease-out + damped wobble | Body reads as 3D squircle ✓, face fades in during decel ✓. Silhouette lumpy (param clusters at corners), lattice sparse/uneven, eyes ride too high, tilt crowds the face | Keep concept; rebuild lattice generation |
| 2 | Lattice rebuilt as machined slices: rings at even heights, profiles at even polar angles (clean intersection grid), denser, zoomed | Clean machine-like grid ✓. Too busy behind the face; rings dominate → "beehive" during spin | Rebalance density, separate face from lattice |
| 3 | rings 6 / longs 10; face-panel mask dims lattice behind the visible face | Face pops ✓ but the dimmed region reads as a ragged *hole*, not a panel. Profile lines converge at poles → "horns" on top | Real panel fill; kill pole convergence |
| 4 | Profiles terminate on rim rings at ±0.93R (open machined lattice, no poles); eyes up to m5 proportion | Horns gone, silhouette clean. Settled hero recognizably m5-as-hologram | Face/lattice separation still via alpha dim |
| 5 | Projected dark glass panel (rounded region of the front surface, 0.82 alpha fill) replaces the per-segment dim | Face sits on a display panel ✓ — wires faintly visible through glass, no ragged edge | Static design solid; move to motion |
| 6 | Claw-machine **drop-in**: gravity fall to impact at t=0.62s, damped micro-bounce, squash-stretch pulse (sy 0.86/sx 1.08 decaying); drop+squash applied post-rotation | Drop reads in frames ✓. Face appears as edge-on slivers at glancing angles; harness missed the impact moment | Tighten face fade; capture impact frame |
| 7 | Face fade threshold nz 0.05→0.30; harness frames re-timed to drop/impact/recover/overshoot | Impact squash visible, face only appears when frontal ✓ | Pose pipeline good — motion still unverified |
| — | **HyperFrames adopted** (user pointer): `__hf` + `hf-seek` wiring, composition root added; first MP4 out (108 frames @30fps, 1.8MB) | Filmstrip shows the full story: drop → spin → land → settle ✓. **Two motion bugs stills never showed:** (a) at t=0 the face is dead-frontal (spinTurns=3.0 ⇒ angle ≡ 0 mod 2π — it starts facing you then spins *away*); (b) last ~1.5s is static — settle wobble too subtle at 30fps, trailing hold dead | Fix start phase; add idle life or trim |
| 8 | spinTurns 3.5 (starts back-facing), dropH 2.4 (enters off-frame), wobble 0.30 rad @7Hz, duration 3.4s | Off-frame entry ✓, impact squash visible ✓ | Body floats in a void |
| 9 | Ground shadow: radial orange pool that spreads/dims with height, tightens on landing | Drop reads grounded; settled hero anchored | Shadow slightly detached below body |
| 10 | Shadow tucked up; hologram glow pass (shadowBlur on near-camera wires) | Warm bloom on front wires — hologram read lands | Checkpoint: render video + judge |
| **11** | **Judge round 1** (independent agent, renders only): **5.5/10 ITERATE** | Naive read: "glowing wireframe **lantern/pumpkin** with a smiley face" — ring-dominant topology is the killer misread; face off-model ("hollow ring eyes, anemic mouth — charm ratio inverted"); entrance reads fade-drift not drop; face reveals **twice** (+1 creepy eye-peek at the silhouette); last 30% of timeline dead; shadow reads as a detached reflection pool | Prescriptions adopted as iterations 12–16: ① cube-mapped quad-grid retopology (kill polar convergence) ② restore exact m5 face on a visible glass panel ③ 10–15% interior fill for 34px survival ④ decisive drop + single choreographed reveal + trim tail ⑤ contact shadow that reacts to impact |
| 12 | **Retopology**: 3 orthogonal ring families (constant x/y/z cube slices projected to the superellipsoid) — every cell a quad, zero pole convergence, seams at cube edges | Corner frame reads as a machined panelized box; pumpkin dead | ① landed |
| 13 | Volumetric interior: 6 cube-face boundary fills at 5.5% alpha (overlaps stack) | Body reads as a translucent volume; glass panel now visible as the face's home | ③ partially landed (helps ≥64px) |
| 14 | Face restoration: solid eyes + small offset pupils (m5's +0.9/+0.9), mouth weight 1:3 mouth:eye (was 1:10), panel rim stroke | Settled hero is unmistakably m5 again — the chunky mouth was the charm | ② landed |
| 15 | Choreography: spinTurns 2.5/T 1.9 (2 turns burn in the fall), velocity stretch into impact, tilt eases −0.30→−0.16, duration 3.4→2.8s, deterministic blink at t=2.35 | Impact squash + recovery read; but t=0.4 still showed the distorted mid-fall face peek | ④ partial — glancing-angle problem is geometric |
| 16 | **Screen power-on**: chassis drops dark, display boots 0.25s after impact (facing × boot smoothstep); shadow impact flash | One reveal, zero peeks — "machine arrives, powers on" is the story | ④⑤ landed |
| 17 | Video #3 via HyperFrames + 15-frame strip | Full arc reads: dark tumble → squash → boot → wobble → blink → rest | Motion verified |
| 18 | In-context render (PNG variant of render-context.mjs): navbar 28 / hero 96 / card 34 | Hero charming; small sizes read as dark app icon with the white face carrying | Needs small-size answer |
| 19 | Transparent-bg poster + 16–128px legibility strip | Face survives to 16px; lattice gone <34px but silhouette+face hold | Evidence for judge round 2 |
| **20** | **Judge round 2 (same judge): 7/10 ITERATE** — "jumped from 5.5 to 7; the hard problems are solved" | ①⑤ landed, ②④ mostly, ③ partial. Naive read now "machined wireframe chassis/CRT" ✓. Remaining: small-size variant (brand-orange fill at ≤34px — the asset's real habitat), mouth still stroke-arc not filled grin, 7-frame idle plateau before the blink + boot fires one beat early, right-edge line doubling (cube edges drawn twice) | Iterations 21–24: avatar mode, grin weight, timeline compression, edge dedupe |
| 21 | Edge dedupe: boundary rings only from the y family + 4 corner verticals drawn once — no cube edge stroked twice | Silhouette is a single clean stroke; ghosting gone | Landed (forensic-zoom nubs only) |
| 22 | Mouth to m5 filled-grin weight (smileLW 0.135→0.21 of eyeScale, wider span) | "Thick filled grin with real mass" — charm parity with 2D m5 | Landed |
| 23 | Blink 2.35→2.05s (splits the idle 4/3), boot delayed one beat (impact+0.38s), idle micro-bob (0.012 units @0.7Hz) after settle | Faceless settled beat → boot flicker → wobble → blink → living idle | Landed |
| 24 | **Avatar mode** (`setAvatarMode`): solid brand-orange body (fill 0.34 stacked), light detail wires — the ≤34px variant; deployed in navbar/card context | At 28/34px reads "orange happy machine" like m5; 16px degrades to orange-square-plus-smile (acceptable favicon behavior) | Landed |
| **25** | **Judge round 3 (final): 7.8/10 — SHIP** (5.5 → 7.0 → 7.8) | All four round-2 prescriptions verified from pixels. Judge's lone blocker — a pure-black final filmstrip frame — was a harness off-by-one (84 frames ÷ 6 = 14 tiles, strip tiled 15×1; FFmpeg padded tile 15 black). Strip fixed; the video's true last frame verified as the settled smiling idle. Judge pre-committed: "if the last frame shows the idle pose, this is a SHIP at these scores" | **SHIPPED** |

## Final system

- **`renders/mascot3d.mp4`** — 960×960 @30fps, 2.8s entrance animation (HyperFrames render)
- **`renders/mascot3d.gif`** — 480px compact loop for README/docs embeds
- **`renders/poster.png`** / **`poster-transparent.png`** — settled hero (≥64px use)
- **`renders/poster-avatar.png`** — avatar mode (≤34px: navbar, machine cards, favicon-adjacent)
- **`renders/final.context.png`** — the deployed system in all three product slots
- `mascot3d.html` — single source of truth (geometry, animation, both render modes).
  `index.html` is a verbatim copy required by the HyperFrames CLI — regenerate
  with `cp mascot3d.html index.html` before `npx hyperframes render`.

Score trajectory across the 25 iterations: scaffold → judged 5.5 → retopology
+ choreography overhaul → judged 7.0 → charm/timing/edge polish + avatar
variant → **judged 7.8, SHIP**. As with the 2D rounds, the judges' naive
misreads (pumpkin, lantern, creepy eye-peek) drove the design more than the
successes did.
