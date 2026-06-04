# Contributing

OpenClaw Machines public core accepts contributions under Apache-2.0.

The public core covers the CLI, Firecracker microVM runtime, worker agents,
host enrollment, and the minimum control plane for local, BYO-host, or
operator-hosted deployments. Billing, plan enforcement, commercial hosted flows,
and private-overlay services are outside the public-core contribution scope.

## Before You Start

- Check the existing issue or open one for larger changes.
- Keep patches focused. Avoid broad doc scrubs, generated churn, or unrelated
  refactors in the same change.
- Do not include secrets, private infrastructure details, customer data, or
  commercial hosted-plan material in public-core changes.
- Report security issues through `SECURITY.md` instead of opening a public issue
  or pull request.

## Commit Signoff

Use DCO signoff on commits:

```bash
git commit -s
```

## Checks

Before opening a pull request, run the checks that match your change. At a
minimum for docs-only changes, review links and formatting. For code changes,
start with:

```bash
make test
make check
```

Firecracker and rootfs changes may require a KVM-enabled Linux host. See
`docs/TESTING.md` for the detailed test matrix.
