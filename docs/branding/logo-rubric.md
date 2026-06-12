# Logo Rubric — OpenClaw Machines

Used to score every candidate in the redesign (issue #28). A judge (independent
of the designer) scores each criterion 0–10 from **rendered images** (360px
light/dark, 64px, 16px), never from SVG source. Weighted sum → /100.

| # | Criterion | Wt | What 10/10 looks like |
|---|---|---:|---|
| 1 | **16px legibility** | 20 | The silhouette is instantly recognizable as itself at favicon size, on light and dark. No detail collapse, no smudge. |
| 2 | **Concept fit** | 20 | Communicates the product: a claw (OpenClaw) and/or isolation/machines (sandboxed microVMs, fleet). A stranger could guess the territory. |
| 3 | **Distinctiveness** | 20 | Memorable after one glance; not confusable with other dev-infra marks (no generic hexagon-with-thing, no cube clichés without a twist). |
| 4 | **Craft** | 15 | Clean geometry: consistent stroke weights/radii, optical (not just mathematical) centering, balanced negative space. |
| 5 | **Versatility** | 15 | Works on light and dark without modification; survives single-color reduction; sits well in a navbar next to a wordmark; square-crops cleanly to an avatar. |
| 6 | **Timelessness** | 10 | No effects that date (heavy gradients, glows, skeuomorphic highlights). Flat or near-flat. |

**Hard gates** (instant disqualification regardless of score):
- Unrecognizable or misleading at 16px (reads as a different object entirely).
- Requires a specific background to work.
- Resembles an existing well-known mark.

**Baseline (current logo, scored 2026-06-12):** the `logo-icon.svg` claw reads
as a red horseshoe/magnet "U", its dark gradient muddies on dark backgrounds,
and the 16px render is an illegible blob. The lockup's red+teal palette doesn't
match the product UI (orange/dark). Baseline score: see
[design-process.md](design-process.md).
