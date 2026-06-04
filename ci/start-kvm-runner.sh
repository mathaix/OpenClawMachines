#!/usr/bin/env bash
set -euo pipefail

run_id="${1:-${GITHUB_RUN_ID:-unknown}}"

if [ -z "${OCM_KVM_RUNNER_START_COMMAND:-}" ]; then
	echo "OCM_KVM_RUNNER_START_COMMAND is required."
	echo "Set it to a maintainer-controlled command that starts or creates the KVM runner VM."
	exit 1
fi

echo "Starting KVM runner for workflow run ${run_id}..."
bash -lc "${OCM_KVM_RUNNER_START_COMMAND}"
