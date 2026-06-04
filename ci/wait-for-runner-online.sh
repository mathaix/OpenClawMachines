#!/usr/bin/env bash
set -euo pipefail

label="${1:-${OCM_KVM_RUNNER_LABEL:-kvm}}"
timeout_seconds="${OCM_KVM_RUNNER_WAIT_SECONDS:-600}"
poll_seconds="${OCM_KVM_RUNNER_POLL_SECONDS:-10}"

if [ -z "${GITHUB_REPOSITORY:-}" ]; then
	echo "GITHUB_REPOSITORY is required."
	exit 1
fi

if [ -z "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]; then
	echo "GH_TOKEN or GITHUB_TOKEN is required."
	exit 1
fi

deadline=$((SECONDS + timeout_seconds))
echo "Waiting up to ${timeout_seconds}s for an idle self-hosted runner labeled '${label}'..."

while [ "${SECONDS}" -lt "${deadline}" ]; do
	runners_json="$(gh api "repos/${GITHUB_REPOSITORY}/actions/runners?per_page=100")"
	count="$(jq --arg label "${label}" '[.runners[] | select(.status == "online" and .busy == false and ([.labels[].name] | index($label)))] | length' <<<"${runners_json}")"

	if [ "${count}" -gt 0 ]; then
		echo "Found ${count} idle online runner(s) with label '${label}'."
		exit 0
	fi

	sleep "${poll_seconds}"
done

echo "Timed out waiting for an idle online runner with label '${label}'."
exit 1
