#!/usr/bin/env bash
set -euo pipefail

run_id="${1:-${GITHUB_RUN_ID:-unknown}}"

if [ -z "${OCM_KVM_RUNNER_STOP_COMMAND:-}" ]; then
	echo "OCM_KVM_RUNNER_STOP_COMMAND is required."
	echo "Set it to a maintainer-controlled command that stops or deletes the KVM runner VM."
	exit 1
fi

echo "Stopping KVM runner for workflow run ${run_id}..."
bash -lc "${OCM_KVM_RUNNER_STOP_COMMAND}"
