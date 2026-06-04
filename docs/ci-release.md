# CI and Release Lanes

This repo keeps public PR automation limited to checks that can run safely on a
standard GitHub-hosted runner. Public PR jobs must not require KVM, root access,
cloud credentials, deployment credentials, or hosted promotion permissions.

## Public CI

The public lane is `.github/workflows/test.yml` plus static secret scanning in
`.github/workflows/security.yml`. It runs on pull requests and pushes to `main`
using standard GitHub-hosted runners.

Allowed public PR commands:

- `make test`
- `make typecheck`
- `make check`
- secret scanning that reads repository contents and commit history only

These jobs use read-only repository permissions and do not run Firecracker,
Docker image publishing, rootfs upload, Cloudflare tunnel tests, GCP deploys, or
release promotion targets.

`make preflight` is a local/BYO-host readiness check for operators. It is not a
public PR gate unless it is split into a CI-safe mode that only performs
unprivileged static or unit checks.

## Trusted KVM Lane

Firecracker tests are trusted-code only. They require a Linux host with KVM,
Firecracker, root privileges, network setup, and cleanup scripts. Do not wire
these targets directly to `pull_request`, `pull_request_target`, or any workflow
that checks out arbitrary public PR code.

Trusted-only targets:

- `make test-integration`
- `make test-integration-e2e`
- `make test-integration-run TEST=...`
- `make integration-kvm`

`.github/workflows/kvm-integration.yml` runs `make integration-kvm` on pushes to
`main`. Maintainers may also run it manually for a pull request by entering the
PR number and exact head SHA after reviewing the change. Tunnel tests also need
scoped Cloudflare credentials and must remain outside public PR CI.

## Release Lane

Release and deployment lanes must stay separate from public CI. Public workflows
must not include production deployment secrets, GCP deploy commands, Cloudflare
publishing commands, artifact uploads, rootfs uploads, or hosted promotion steps.

Before a release or rootfs upload, maintainers should use the trusted lane to run
the relevant KVM gates, then run deployment or promotion commands from a trusted
environment with scoped secrets.

## Manual Review Lane

`.github/workflows/codex-review.yml` is manual because it uses an OpenAI API
secret and PR write permission. Maintainers may dispatch it after deciding a PR
is safe to review with that trusted workflow.
