#!/usr/bin/env bash
# test-xfs-setup.sh — Create an XFS loop device for integration tests.
#
# Enables reflink (CoW) copies so per-VM rootfs clones are instant (~0ms)
# instead of full 3.4GB copies (~21s each on ext4).
#
# Usage: source scripts/test-xfs-setup.sh
#   Sets TEST_STATE_DIR to the XFS mount point.
#
# Requires: root, xfsprogs

set -euo pipefail

XFS_IMG="/tmp/ocm-test-xfs.img"
XFS_MOUNT="/tmp/ocm-test"
XFS_SIZE="15G"  # sparse — only allocates what's used

# Skip if already mounted
if mountpoint -q "$XFS_MOUNT" 2>/dev/null; then
    echo "[xfs] $XFS_MOUNT already mounted as XFS"
    export TEST_STATE_DIR="$XFS_MOUNT"
    exit 0
fi

# Clean up any stale state
rm -rf "$XFS_MOUNT" "$XFS_IMG" 2>/dev/null || true
mkdir -p "$XFS_MOUNT"

# Create sparse XFS image (reflink=1 enables CoW clones)
truncate -s "$XFS_SIZE" "$XFS_IMG"
mkfs.xfs -f -m reflink=1 "$XFS_IMG" >/dev/null 2>&1

# Mount with loop
mount -o loop "$XFS_IMG" "$XFS_MOUNT"
chmod 755 "$XFS_MOUNT"

echo "[xfs] Mounted XFS with reflink at $XFS_MOUNT (sparse $XFS_SIZE)"

# --- Pre-stage rootfs to XFS for instant reflink clones ---
# Each test creates its own orchestrator which stages rootfs from ImagesDir → StateDir.
# If ImagesDir is on ext4, every test does a full 3.4GB copy (~21s each).
# Pre-staging to XFS means tests can reflink from XFS→XFS (~8ms each).
IMAGES_SRC="/var/lib/ocm/images"
IMAGES_DST="$XFS_MOUNT/images"

if [ -f "$IMAGES_SRC/rootfs.ext4" ]; then
    mkdir -p "$IMAGES_DST"
    echo "[xfs] Pre-staging rootfs to XFS for reflink clones..."
    cp "$IMAGES_SRC/rootfs.ext4" "$IMAGES_DST/rootfs.ext4"
    # Also copy kernel if present (needed by orchestrator)
    if [ -f "$IMAGES_SRC/vmlinux" ]; then
        mkdir -p "$IMAGES_DST"
        cp "$IMAGES_SRC/vmlinux" "$IMAGES_DST/vmlinux"
    fi
    # Copy browser rootfs if present
    if [ -f "$IMAGES_SRC/browser-rootfs.ext4" ]; then
        mkdir -p "$IMAGES_DST"
        cp "$IMAGES_SRC/browser-rootfs.ext4" "$IMAGES_DST/browser-rootfs.ext4"
    fi
    echo "[xfs] Pre-staged rootfs to $IMAGES_DST ($(du -sh "$IMAGES_DST" | cut -f1))"
else
    echo "[xfs] No rootfs found at $IMAGES_SRC — tests will download from GCS"
fi
