# Security

Please do not disclose vulnerabilities publicly before maintainers have had
time to investigate and remediate.

## Reporting

Use GitHub's private vulnerability reporting flow for this repository when it is
available. If that flow is unavailable, contact the maintainers privately and
clearly mark the report as security-sensitive. Do not open a public issue or
pull request for an unfixed vulnerability.

Include:

- Affected versions, commits, or deployment profile.
- Reproduction steps or proof of concept.
- Expected and observed impact.
- Any logs, screenshots, or traces that do not expose secrets.
- Suggested mitigations, if known.

## Scope

This policy covers the Apache-2.0 public core: CLI, control plane, worker
agents, host enrollment, Firecracker runtime, rootfs, and public integration
surfaces. Reports involving private-overlay services should still be sent
privately so maintainers can route them correctly.

## Supported Versions

Before a tagged stable release, security fixes are handled on `main`.
