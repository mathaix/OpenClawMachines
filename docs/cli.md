# CLI

The `ocm` CLI is maintained as a separate Apache-2.0 project:

<https://github.com/mathaix/ocm-cli>

This repository should not carry or patch main-repo CLI source code. Keep API
contracts, local setup, and operator-hosted control-plane behavior compatible
with the companion CLI, and make CLI implementation changes in `mathaix/ocm-cli`.

For day-to-day operator recovery with the CLI, see
[operator-troubleshooting.md](operator-troubleshooting.md). It covers common
failure points such as expired local CLI sessions, `ocm machines ssh`
reachability, Slack pairing state, gateway recovery, and safe config edits.
