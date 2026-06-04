# KVM Integration CI

OpenClaw Machines keeps ordinary pull request checks on free GitHub-hosted
runners. Tests that require `/dev/kvm` run only on a maintainer-controlled
self-hosted runner.

## Workflow Shape

- `.github/workflows/test.yml` runs free CI on pull requests and pushes to
  `main`.
- `.github/workflows/kvm-integration.yml` runs KVM integration tests on pushes
  to `main`.
- Maintainers can manually run `KVM Integration` for a pull request by entering
  the PR number and exact head SHA.

The KVM workflow does not run automatically for public pull requests. This
prevents untrusted fork code from executing on a self-hosted runner.

## Runner Contract

The KVM runner must register with these labels:

- `self-hosted`
- `linux`
- `x64`
- `kvm`

The integration job runs:

```sh
make integration-kvm
```

## Start And Stop Hooks

The public repository stays provider-neutral. Configure these repository
secrets to control how the KVM machine is started and stopped:

- `OCM_KVM_RUNNER_START_COMMAND`: command run on a GitHub-hosted runner before
  the KVM job is queued.
- `OCM_KVM_RUNNER_STOP_COMMAND`: command run after the KVM job, even when the
  test job fails.
- `OCM_KVM_RUNNER_GH_TOKEN` (optional): token used only by the wait step to
  list self-hosted runners. Set this if the default workflow token cannot read
  repository runner state.

Examples of valid hook commands include a cloud CLI command, an SSH command to a
runner manager, or a small script checked into a private overlay repository.

The wait step polls GitHub for an idle online runner with the `kvm` label. The
default wait is 600 seconds and can be overridden with
`OCM_KVM_RUNNER_WAIT_SECONDS`.

Runner-control secrets are scoped to the GitHub-hosted start, wait, and stop
jobs. They are not exported to the self-hosted integration job that executes PR
code.

## Security Rules

- Do not add `pull_request` to the KVM workflow.
- Do not use `pull_request_target` to check out and run pull request code.
- Keep KVM jobs behind `workflow_dispatch` for PRs.
- Prefer ephemeral self-hosted runners once the integration suite is stable.
