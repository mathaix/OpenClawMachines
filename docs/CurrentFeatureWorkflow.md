# Feature Documentation Workflow

This document describes the workflow for tracking features from inception to release.

## Overview

```
┌─────────────────┐     PR Merged      ┌─────────────────────┐
│ CurrentFeature  │ ─────────────────► │ Feature_<PR#>.md    │
│ .md             │                    │                     │
└─────────────────┘                    └──────────┬──────────┘
        ▲                                         │
        │                                         ▼
   New feature                         ┌─────────────────────┐
   starts                              │ RELEASE.md          │
                                       │ (link added)        │
                                       └─────────────────────┘
```

## Workflow Steps

### 1. Start a New Feature

Before starting work, create a TaskCreate checklist of everything that needs to be accomplished. Each task should be specific and verifiable. As each item is completed, show evidence it works (test output, curl response, or log line) before marking it done. Do not mark anything complete without verification.

Use the `/currentfeature` skill or manually create `docs/CurrentFeature.md` with:
- Feature overview and goals
- Architecture decisions
- Implementation plan
- Open questions
- Testing checklist

**Only one `CurrentFeature.md` should exist at a time.** If multiple features are in progress, use separate branches with their own `CurrentFeature.md`.

### 2. During Development

Update `CurrentFeature.md` as the feature evolves:
- Check off completed items
- Document decisions made
- Add learnings and gotchas
- Update architecture if it changes

### 3. When PR is Merged

After the feature PR is merged to `main`, use the `/currentfeature` skill which will:

1. Find the last merged PR number
2. Rename `docs/CurrentFeature.md` → `docs/Feature_<PR#>.md`
3. Add link to `docs/RELEASE.md`
4. Create a fresh `docs/CurrentFeature.md` for the next feature
5. Create and push a new branch

Or manually:

```bash
PR_NUMBER=42

git mv docs/CurrentFeature.md docs/Feature_${PR_NUMBER}.md

# Add link to RELEASE.md
# Create fresh CurrentFeature.md

git add -A
git commit -m "docs: Archive Feature_${PR_NUMBER}.md and update RELEASE.md"
git push
```

### 4. Start Next Feature

Create a new `docs/CurrentFeature.md` for the next feature.

## File Locations

```
docs/
├── CurrentFeature.md          # Active feature being worked on
├── CurrentFeatureWorkflow.md  # This document
├── Feature_1.md               # Archived: PR #1
├── Feature_2.md               # Archived: PR #2
├── RELEASE.md                 # Links to all shipped features
├── architecture.md            # System architecture
└── ...
```

## RELEASE.md Format

```markdown
# Release Notes

## Features

- **PR #42**: One-click OpenClaw deployment ([Feature_42.md](Feature_42.md))
- **PR #56**: Channel setup wizard ([Feature_56.md](Feature_56.md))
- ...

## Bug Fixes

- PR #43 - Fix VM termination on idle timeout
- ...
```

## Benefits

1. **Audit Trail**: Every shipped feature has documentation
2. **Knowledge Base**: New team members can understand past decisions
3. **Release Notes**: Automatic changelog with links to details
4. **Single Source of Truth**: `CurrentFeature.md` is always the active work

---

## Documentation Style Guide

New documentation should follow one of two templates depending on the doc type. Existing docs do not need to be retroactively reformatted.

### Design Documents

Use this template for architecture docs, rearchitecture proposals, and feature designs:

    ## [Title]
    ### Context
    Problem statement or need being addressed.
    ### Decision
    What we are doing and how.
    ### Rationale
    Why this approach over alternatives.
    ### Risks
    Known risks and mitigations.
    ### See Also
    Cross-links to related docs and source files.

### Research Briefs

Use this template for analysis reports, investigations, and spike results:

    ## [Title]
    ### Question
    What we set out to answer.
    ### Method
    How we investigated (tools, data sources, experiments).
    ### Findings
    What we learned.
    ### Caveats
    Limitations, open questions, or areas needing further investigation.

### General Guidelines

- **Cross-link liberally** — every doc should have a "See Also" section linking to related docs and key source files
- **Keep inline notes concise** — 2-3 sentences max for "this doc assumes you've read X" pointers
- **Use relative links** — `[architecture.md](architecture.md)`, not absolute URLs
- **Decision tables** over open question lists — when decisions are pending, track them in a table with Status/Owner/Due columns
