# Claude Code slash commands

Project slash commands for working on OpenClaw Machines. In Claude Code, type
`/<name>` (e.g. `/test`) and the agent follows the steps in the matching
`.claude/commands/<name>.md` file. Adapted from the internal monorepo and
sanitized for this repo (the private Cloud Run / GCS deploy commands are
intentionally omitted — see below).

## Available commands

| Command | What it does |
|---------|--------------|
| `/start` | Start the control plane (`:8080`) and frontend (`:5173`) via `scripts/local-dev.sh` (Postgres + env + migrations). |
| `/stop` | Stop the dev servers (and optionally the Docker Postgres / local Firecracker worker). |
| `/status` | Health-check the control plane + frontend (`make status`). |
| `/test` | Run the test suite — `make test` (Go + frontend), or individual `test-go` / `test-unit` / `test-frontend` / `typecheck`. |
| `/verify` | Full quality gate before committing: `make check` (vet/lint/vuln/shellcheck) → `make test-go` → `make typecheck`. |
| `/pr` | Commit, push, and open a PR with a generated summary + test plan. |
| `/currentfeature` | Archive the current feature doc, bump `RELEASE.md`, and start a fresh branch from `main`. |
| `/codex` | Delegate a review or read-only task to the OpenAI Codex CLI (`codex review` / `codex exec`). Requires the `codex` CLI. |
| `/freshclone` | Make a clean clone of the repo (backs up the current dir) after corrupted/conflicted state. |

## Intentionally omitted (private deploy pipeline)

These exist in the internal monorepo but were left out because they drive
infrastructure that isn't part of this repo (the Make targets don't exist here):

- `/deploy` — `make deploy-all` (Cloud Run / Cloudflare).
- `/snapshot` — `make build-components` / `upload-agent` (build + upload artifacts to GCS).
- `/versions` — `gsutil cat gs://openclawmachines/...` (deployed artifact versions).

For local Firecracker provisioning instead, see `scripts/local-e2e-firecracker.sh`
and `docs/local-firecracker-e2e.md`.

## Notes

- `.claude/commands/` is tracked in this repo (only `.claude/worktrees/` is
  gitignored). If your clone has a local `.git/info/exclude` entry for `.claude/`,
  use `git add -f .claude/commands/...` when adding new commands.
