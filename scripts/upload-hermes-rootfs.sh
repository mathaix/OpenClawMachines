#!/bin/bash
# Upload a Hermes rootfs image to GCS.

set -euo pipefail

export GCS_PREFIX="${GCS_PREFIX:-hermes-rootfs}"
exec "$(dirname "$0")/upload-rootfs.sh" "${1:-/var/lib/ocm/images/hermes-rootfs.ext4}"
