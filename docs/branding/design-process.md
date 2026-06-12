# Logo Redesign — Design Process Log

Issue #28. This log captures the reasoning behind each step, as it happened.
Candidates live in `candidates/`, scored against [logo-rubric.md](logo-rubric.md)
by an independent judge agent from rendered images (`render.mjs`).

## Step 0 — Baseline analysis (why the current logo fails)

Rendered the existing `logo-icon.svg` and `logo-full.svg` at 360/64/16px on
light + dark:

- **Shape**: the "two pincers joined at the base" path actually reads as a
  **red horseshoe / magnet / letter U**. Nothing says claw, nothing says
  machines. The claw concept is in the comment, not in the silhouette.
- **Color**: a 3-stop red gradient (EF4444→7F1D1D) goes muddy on dark
  backgrounds; the 30%-opacity hairline highlight is invisible at every size —
  pure dead weight in the file.
- **16px**: an illegible red smudge. Fails the favicon test outright.
- **Lockup**: system-font text, red "OpenClaw" + teal "Machines". The teal
  appears nowhere in the product; the actual UI is **orange (#ea580c family) on
  near-black**, so the brand assets disagree with the product.

**Baseline rubric estimate**: 16px legibility 2, concept fit 2, distinctiveness
3, craft 3, versatility 4, timelessness 4 → **~30/100**. Anything we ship must
clear 70.

## Step 1 — Design constraints and strategy (before drawing anything)

Decisions taken up front, so iterations vary the *idea*, not the ground rules:

1. **Match the product palette**: primary mark in the UI's orange
  (`#ea580c` / `#f97316` range) + near-black `#0b0b10`, must also reduce to a
  single color. Drop teal and the red gradient.
2. **Flat geometry, no gradients/highlights** — addresses timelessness and the
  dark-background muddiness in one move.
3. **Design at 16px first**: every concept gets sketched as "what is the
  silhouette at 16px?" before any detailing. The current logo failed precisely
  here.
4. **The mark must carry one of two ideas (ideally both)**:
   - *claw / pincer* (OpenClaw identity), drawn so it cannot be misread as a
     U/horseshoe — meaning asymmetry or an explicit gripping gesture;
   - *isolation / machines* (the actual product) — a boundary, cell, or grid
     that the claw relates to.
5. **The container is meaningful here**: a microVM is literally a boundary
   around an agent. A rounded-square or hex boundary isn't decoration for this
   product — it's the product. Use it deliberately, not as default app-icon
   chrome.

## Step 2 — Round 1: five divergent concepts

Strategy: maximize conceptual spread first, refine later rounds. The five
directions and the reasoning for each:

| Candidate | Concept | Why it might win | Known risk going in |
|---|---|---|---|
| `r1-bracket-claw` | Two pincer arcs forming square brackets `[ ]` gripping a dot — claw doubles as code/terminal syntax | Claw + dev-tool in one glyph; asymmetric grip kills the horseshoe read | Brackets may read as punctuation, not claw |
| `r1-vm-grip` | Rounded-square VM boundary; a claw reaches in from outside holding a dot (the agent) | Tells the exact product story: something held safely inside a boundary | Two ideas may be too much detail for 16px |
| `r1-pincer-power` | Single bold pincer whose opening forms a power-button notch | One shape, two readings (claw + on/off = machines) | Power symbol may dominate; claw read may vanish |
| `r1-cell-grid` | 2×2 grid of rounded cells (a fleet of machines); one cell is a claw mark | "Many isolated machines" literally; grid scales beautifully | Grids are common in infra logos; claw cell may be too small at 16px |
| `r1-crab-geo` | Geometric crab: body square + two angular pincers, drawn with consistent radii | Owns the mascot territory but in flat, scalable geometry | Easy to land in "cute mascot" rather than "infra tool" |

Each is drawn flat, one color + near-black, on a 48×48 grid with 2px stroke
discipline. Renders + judge scores follow.

## Step 3 — Round 1 results (designer self-critique, before judging)

Rendered all five (`*.render.png`). Honest reads:

| Candidate | What it actually reads as | Verdict |
|---|---|---|
| `r1-bracket-claw` | Fat donut / camera shutter — the 7px strokes and teeth merged into a blob | **Fail.** Stroke discipline broke; gaps must be real gaps. |
| `r1-vm-grip` | The C-boundary works at all sizes; the "claw" reads as a **fish** | Keep structure, redraw the grip as an actual two-jaw pincer. |
| `r1-pincer-power` | Instantly a power button; crisp at 64 and 16. The hooks read as bird heads, so the claw idea is silent | **Strongest silhouette.** Refine hooks into real pincer jaws. |
| `r1-cell-grid` | Fleet-of-cells lands and survives 16px; the claw cell reads as pac-man / a play button | Grid is a good *secondary* motif; claw integration failed. |
| `r1-crab-geo` | A **bull's face** (pincers→horns, body→snout, slots→nostrils) | **Kill.** Object-level misread — rubric hard gate. |

Lesson recorded: negative-space details survive scaling; appendage details
(teeth, hooks) blob at stroke joins. Round 2 designs with cuts, not additions.

## Step 4 — Round 1 judge scores (independent agent, from renders + rubric only)

| Rank | Candidate | Total /100 | Judge's naive read |
|---|---|---|---|
| 1 | r1-bracket-claw | 57.5 | "eye / camera aperture / target" |
| 2 | r1-crab-geo | 54.0 | "a bull or ox head" |
| 3 | r1-cell-grid | 52.5 | "Microsoft-style tile grid with a Pac-Man bite" |
| 4 | r1-vm-grip | 50.0 | "a key in a keyhole / fish leaving a box" |
| — | r1-pincer-power | **DQ** | "a power button, full stop" — resembles an existing universal mark; in a fleet UI it implies *shutdown*. Would have tied 1st otherwise. |

Baseline (current logo) calibrated by the same judge: **23/100**. Ship bar: 70.

Designer + judge agreed independently on every misread (bull, fish, target,
pac-man) — validates judging from renders, not source.

## Step 5 — Round 2 reasoning

Judge directives adopted:
- **r2-bracket-claw**: widen the top/bottom gaps so the *silhouette* is two
  open pincers (not a ring); replace the center dot with a **rounded square —
  the microVM being held** (kills the eye/target read AND adds the product
  story); enforce one consistent gap width everywhere (no pinch slivers).
- **r2-crab**: kill the bull — pincers move *beside* the body with jaw gaps
  facing inward/down; asymmetry exaggerated ~2× so it reads as a fiddler-crab
  choice, not an error; eye slots widened to survive 16px dark.
- **r2-claw-c** (new): single bold "C" letterform drawn as a claw — tapered
  jaws, one jaw notch, gripping a small rounded square (VM). Letterforms are
  ownable; cuts not appendages, per the round-1 lesson.
- **vm-grip dropped** (worst 16px, two visual languages) and **cell-grid
  parked** as a possible secondary motif (judge: bite vanishes at 16px,
  Microsoft-adjacent grid).

## Step 6 — Round 2 judge scores

| Rank | Candidate | Score | Judge's naive read | Outcome |
|---|---|---|---|---|
| 1 | r2-bracket-claw | **67.5** (+10 vs r1) | "an eye with a square pupil" | Below 70 bar; 3 refinements prescribed |
| 2 | r2-claw-c | 62 → **DQ** | "Pac-Man about to eat a pellet" | Hard gate: well-known mark — same standard that killed pincer-power |
| 3 | r2-crab | 54 | "a cute robot head / Dependabot-family bot; eyes form a pause glyph" | Crab abandoned after two object-level misreads (bull, robot) |

Designer note: I flagged the Pac-Man risk pre-judging but underweighted my own
rubric's hard gate. The judge applied it consistently. Two DQs teach the same
lesson: **a circle-derived glyph with a radial opening will collide with famous
marks** (power symbol, Pac-Man). Round 3 must break circularity.

## Step 7 — Round 3 reasoning

Adopting the judge's prescriptions, three candidates:
- **r3-grip-diag**: bracket-claw rebuilt on a *diagonal* grip axis with
  *asymmetric* jaws (judge: symmetric concentric arcs around a centered square
  will always read iris-first). Rounded terminals at a consistent radius, jaw
  overlapping the squircle's corner.
- **r3-claw-c2**: the C-claw rescued per the judge's two DQ-escape changes —
  flat typographic terminals (geometric-grotesque C, not a wedge mouth) and the
  microVM squircle moved *inside* the counter with a square tooth clamping its
  edge. Letter that grips, not character that eats.
- **r3-hex-pinch**: insurance candidate breaking circularity entirely — a
  hexagonal pincer: two angular jaw plates meeting on a diagonal around the
  squircle. Hexagonal = infra-native, immune to Pac-Man/power reads.

## Step 8 — Round 3 judge scores

| Rank | Candidate | Score | Judge's naive read |
|---|---|---|---|
| 1 | **r3-hex-pinch** | **68.5** — new overall leader | "camera aperture / mechanical chuck holding a part" — first candidate where *machine gripping an object* is a plausible first read |
| 2 | r3-claw-c2 | 59.5 | "an orange letter G; payload reads as a padlock" |
| 3 | r3-grip-diag | 43.5 | "a lowercase a / curled creature" — retired: its load-bearing detail can't survive 16px *by construction* |

Cross-round lesson the judge confirmed: **angular/mechanical geometry beats
letterform rescues and organic blobs** every round. Also flagged: before any
ship decision, trademark-sweep the rotational-bracket neighborhood (OpenShift,
Proxmox — same industry, same warm palette).

## Step 9 — Round 4 reasoning

All-in on the leader, per the judge's three prescriptions:
- **r4-claw-grip**: hex-pinch with broken symmetry — one dominant angular jaw
  (wraps left+bottom), a smaller opposing thumb plate (top-right), and a
  tapered tooth on each inner edge pointing into the squircle with a hairline
  gap (grip *tension*, not a frame).
- **r4-claw-grip-soft**: same structure, outer corners rounded to match the
  squircle's radius language — tests whether industrial-hard or product-soft
  scores better on craft.
- **r4-claw-c3**: last shot for the C per judge's two fixes (fang moves to the
  C's terminal, payload floats free as a neutral squircle) — if it can't beat
  70 here, the letterform dies.

## Step 10 — Round 4 judge scores: first candidate clears the ship bar

| Rank | Candidate | Score | Notes |
|---|---|---|---|
| 1 | **r4-claw-grip** | **74.5** — clears 70 | Naive read: "mechanical gripper holding a chip" with a *correct-letter* C bonus read. Concept finally lands. |
| 2 | r4-claw-grip-soft | 69.5 | "Kill soft; don't split-test it" — loses exactly where the mark must win (16px), teeth clash with rounded limbs. |
| — | r4-claw-c3 | 54 → **DQ** | Reads as the letter **G** at 64/16 (squircle becomes the crossbar). Letterform retired after 3 attempts and 2 gate hits. |

Judge: "the gap to 85 is almost entirely craft, not concept." Prescribed: (1)
boolean-union all shapes — visible AA seams at the tooth joins; (2) normalize
the three mouth gaps to one unit sized for 64px; (3) squircle down ~2% (mark
reads bottom-weighted); (4) blunt the thumb tooth (arrowhead → refresh-arrow
risk); (5) verify the bottom notch cuts through.

## Step 11 — Round 5: craft polish (no concept changes)

All five fixes applied in a redraw: single compound path (subpaths share one
fill — no stacked shapes, no seams), gap unit standardized at 4/48 viewBox
everywhere (jaw↔thumb, mouth opening, both tooth↔VM clearances), squircle
nudged down 1.5 units, thumb tooth re-cut as a blunt trapezoid.

## Step 12 — Round 5 judge verdict: SHIP

**r5-claw-grip: 79.25/100 — "SHIP IT as the new primary mark."**

The judge verified each prescribed fix from the rendered pixels (including
flood-fill centroid measurement of the nucleus: r4 was 6px high/left of the
mark's bbox center; r5 is exact): 5/5 landed. Score trajectory across the
process: **23 (baseline) → 57.5 → 67.5 → 68.5 → 74.5 → 79.25**. Concept and
distinctiveness held flat in round 5 by design — the final +4.75 was pure
craft, which is what the judge said the gap was.

Non-blocking polish noted for follow-up: optical (vs mathematical) centering
test at -2/-3px x-offset; a favicon-specific cut with the top notch widened;
an SVG-unit audit of the thumb slot's angle-dependent clearance.

## Final ranking — top 5 captured in this PR

| Rank | Candidate | Score | One-line story |
|---|---|---|---|
| 1 | **r5-claw-grip** | **79.25** | Asymmetric machine claw gripping a microVM; single compound path, uniform 4-unit clearances |
| 2 | r4-claw-grip | 74.5 | Same concept pre-polish (seams, uneven gaps) |
| 3 | r4-claw-grip-soft | 69.5 | Rounded variant — judge: "kill soft," loses at 16px |
| 4 | r3-hex-pinch | 68.5 | Symmetric ancestor — reads aperture/chuck, taught us asymmetry = grip |
| 5 | r2-bracket-claw | 67.5 | Circular-era best — the squircle-as-microVM idea originated here |

Retired with cause: power-symbol DQ, Pac-Man DQ, letter-G DQ, a bull, a robot,
a fish, and a snail. Every misread is documented above with the judge's naive
reads — the misreads drove the design more than the successes did.

## Step 13 — Codex review (external reviewer)

Verdict: **"r5 is a credible mark and materially better than the current logo.
I would approve it as the chosen design direction, but not as a complete brand
replacement until trademark review and asset rollout are covered."** No
CRITICAL findings. Key points adopted into scope:

- **This PR selects the direction and captures the top 5; it deliberately does
  NOT replace the live brand assets** (`frontend/public/branding/`,
  favicon/webmanifest/og-image). Rollout is follow-up work.
- **Before rollout** (blocking, per both judge and Codex): a documented
  trademark comparison board (OpenShift, Proxmox, aperture/C-badge
  neighborhood); a favicon-specific cut (the 4-unit gaps are ~1.33px at 16px —
  not optional before replacing `favicon.png`); documented color variants
  (#EA580C is 5.52:1 on near-black but 3.56:1 on white — fine for a logo,
  needs mono/reverse variants for UI reuse).
- Codex confirmed SVG craft: no browser/GitHub-hostile features; the disjoint
  same-filled subpaths are safe under nonzero fill.
- Process gap acknowledged: one AI judge from renders is strong for iteration,
  weak for launch proof — blind human reads belong in the rollout step.

## Step 14 — Redirect: the mascot is the logo people see

Owner feedback: the redesign target they had in mind is the **mascot**
(`mascots.svg` — three round lobster blobs, red/blue/green; the red one is the
app-header logo today). Assessment of the current mascots: the *concept* is
excellent — a fleet of differently-colored agent-lobsters IS the product — but
the craft fails: heavy gradients, mitten claws that don't read as claws,
pinprick eyes, and mush below 64px.

Strategy adopted (the mature-project two-asset system): **geometric mark
(r5-claw-grip) for favicon/avatar/UI chrome + a mascot for personality** —
docs, web, social. They must share DNA. Mascot brief:
- flat, two-tone max per mascot (no gradients), crisp at 64px;
- the body becomes a **rounded square** — the microVM squircle from the mark —
  so mascot and mark are visibly the same family ("an agent in a box");
- claws must read as claws (open pincers, the r-series lesson: cuts not blobs);
- keep the multi-color fleet story (red primary, blue/green siblings).

## Step 15 — Round 6: three mascot candidates

- **m1-boxcrab**: squircle body, two open-pincer claws raised at the sides,
  stalk antennae, big friendly eyes. Direct "VM with a personality."
- **m2-lobster-flat**: faithful cleanup of the current round lobster — circle
  body kept, flattened, claws redrawn as real pincers. Tests whether the
  current shape survives a craft-only fix.
- **m3-claw-buddy**: the r5 mark's jaw+thumb claw shape reused as the mascot's
  *own claw*, body peeking from inside it — maximum mark↔mascot coherence.

## Step 16 — Rounds 6–8: the mascot arc and the page-context verdict

Owner redirect #1: the redesign target they cared about is the **mascot**
(`mascots.svg`, the lobster trio — judge calibrated it at **27.5/100**:
"three overlapping plush blobs — pigs? gumdrops?"). Rounds 6–7 produced
**m4-boxcrab** (squircle body, crab eye-stalks, docked pincers) from judge
prescriptions, and **m4-fleet** proved the multi-color fleet survives flat.

Owner redirect #2: *"you don't need to stick to that theme — pick something
that looks good on the whole page."* This changed the evaluation itself: built
`render-context.mjs`, which renders every candidate in three real product
contexts (dark navbar @28px beside the wordmark, README hero @96px on white,
dashboard machine-card avatar @34px), and ran a final in-context judging round.

## Step 17 — Final in-context verdict (the deciding round)

| Candidate | Navbar 28px | Hero 96px | Card 34px | Overall |
|---|---|---|---|---|
| **r5-claw-grip** | 9 | 8 | 8.5 | **8.5 — THE logo** |
| m5-happy-machine | 8 | 7.5 | 9 | 8 — best in-product avatar |
| m4-boxcrab | 5 | 8 | 5.5 | 6 — hero-only; "page presence kills it" |
| m6-lobster-mini | 4 | 5 | 4.5 | 4.5 — killed ("monkey before lobster") |

**The system the judge recommends (and this PR captures):**
- **r5-claw-grip = the brand mark** — navbar, favicon, hero, README. Only
  asset that survives all three sizes undegraded.
- **m5-happy-machine = the machine** — its squircle is *literally the square
  r5's claw grips*, so it's not a second mascot, it's the same object given a
  face. Use for machine-card avatars, empty states, loading states. It
  outscored r5 in the card context (9 vs 8.5) because "a machine with a face
  is the perfect repeated list element for your running agents."
- m4-boxcrab → illustration use only; m6 cut.
- **Wordmark fix:** the teal "Machines" floats unanchored next to any
  single-color orange mark and dates the lockup — make "Machines"
  white/neutral and let teal retreat to functional accents (it already owns
  the "Running" pill).
