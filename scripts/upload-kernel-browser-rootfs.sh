#!/bin/bash
# Upload the Kernel Images based browser rootfs to GCS.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export ROOTFS_BASENAME="${ROOTFS_BASENAME:-kernel-browser-rootfs}"
export GCS_PREFIX="${GCS_PREFIX:-kernel-browser-rootfs}"

exec "${SCRIPT_DIR}/upload-browser-rootfs.sh" "${1:-/var/lib/ocm/images/kernel-browser-rootfs.ext4}"
