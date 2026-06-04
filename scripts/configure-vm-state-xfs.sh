#!/usr/bin/env bash
# configure-vm-state-xfs.sh - configure reflink-capable VM state storage.
#
# This creates a sparse XFS image file on the existing host filesystem and mounts
# it at STATE_DIR, normally /var/lib/ocm/vms. The backing filesystem keeps its
# existing redundancy, while VM rootfs clones get XFS reflink support.
#
# Safe defaults:
#   - idempotent when the mount/image already exists
#   - refuses to run while Firecracker VMs are live
#   - preserves the full VM state directory, including vms.json, browser-vms.json,
#     per-VM rootfs/data images, runtime files, and cached manifests
#   - verifies state-file entry counts before and after the final mount
#   - moves a non-empty old state dir aside for rollback before final mount
#
# Usage:
#   sudo bash scripts/configure-vm-state-xfs.sh
#
# Optional environment:
#   OCM_BASE=/var/lib/ocm
#   OCM_STATE_DIR=/var/lib/ocm/vms
#   OCM_VM_STATE_XFS_IMAGE=/var/lib/ocm/ocm-xfs.img
#   OCM_VM_STATE_XFS_SIZE=200G
#   OCM_AGENT_ENV=/etc/ocm-agent/agent.env
#   OCM_SET_BROWSER_STATE_DIR=true
#   OCM_STOP_AGENT=true
#   OCM_RESTART_AGENT=false

set -euo pipefail

OCM_BASE="${OCM_BASE:-/var/lib/ocm}"
OCM_STATE_DIR="${OCM_STATE_DIR:-${STATE_DIR:-${OCM_BASE}/vms}}"
OCM_VM_STATE_XFS_IMAGE="${OCM_VM_STATE_XFS_IMAGE:-${OCM_BASE}/ocm-xfs.img}"
OCM_VM_STATE_XFS_SIZE="${OCM_VM_STATE_XFS_SIZE:-200G}"
OCM_AGENT_ENV="${OCM_AGENT_ENV:-/etc/ocm-agent/agent.env}"
OCM_SET_BROWSER_STATE_DIR="${OCM_SET_BROWSER_STATE_DIR:-true}"
OCM_STOP_AGENT="${OCM_STOP_AGENT:-true}"
OCM_RESTART_AGENT="${OCM_RESTART_AGENT:-false}"

log() { echo "==> $*"; }
err() { echo "ERROR: $*" >&2; exit 1; }

truthy() {
    case "${1,,}" in
        1|true|yes|on) return 0 ;;
        *) return 1 ;;
    esac
}

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        err "Must run as root (use sudo)"
    fi
}

require_tools() {
    local missing=()
    local tool
    for tool in findmnt mount umount truncate mkfs.xfs cp blkid jq; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            missing+=("$tool")
        fi
    done
    if [ "${#missing[@]}" -gt 0 ]; then
        err "Missing required tools: ${missing[*]}"
    fi
}

validate_paths() {
    case "$OCM_STATE_DIR" in
        /*) ;;
        *) err "OCM_STATE_DIR must be absolute: $OCM_STATE_DIR" ;;
    esac
    case "$OCM_VM_STATE_XFS_IMAGE" in
        /*) ;;
        *) err "OCM_VM_STATE_XFS_IMAGE must be absolute: $OCM_VM_STATE_XFS_IMAGE" ;;
    esac
    [ "$OCM_STATE_DIR" != "/" ] || err "OCM_STATE_DIR must not be /"
    [ "$OCM_VM_STATE_XFS_IMAGE" != "$OCM_STATE_DIR" ] || err "image path must not equal state dir"
    case "$OCM_VM_STATE_XFS_IMAGE" in
        "$OCM_STATE_DIR"/*) err "image path must not be inside OCM_STATE_DIR; it would be hidden by the mount" ;;
    esac
}

service_is_active() {
    command -v systemctl >/dev/null 2>&1 || return 1
    systemctl is-active --quiet "$1" 2>/dev/null
}

stop_agent_if_needed() {
    if service_is_active ocm-agent; then
        if truthy "$OCM_STOP_AGENT"; then
            log "Stopping ocm-agent before storage migration..."
            systemctl stop ocm-agent
        else
            err "ocm-agent is active; stop it first or set OCM_STOP_AGENT=true"
        fi
    fi
}

assert_no_live_vms() {
    local live_units=""
    if command -v systemctl >/dev/null 2>&1; then
        live_units="$(systemctl list-units 'ocm-vm-*.service' 'ocm-browser-vm-*.service' --state=active --no-legend --plain 2>/dev/null || true)"
    fi
    if [ -n "$live_units" ]; then
        echo "$live_units" >&2
        err "Live OCM Firecracker systemd units are still active; drain and stop VMs first"
    fi

    local live_pids=""
    if command -v pgrep >/dev/null 2>&1; then
        live_pids="$(pgrep -af '(^|/)firecracker([[:space:]]|$)' 2>/dev/null || true)"
    fi
    if [ -n "$live_pids" ]; then
        echo "$live_pids" >&2
        err "Live Firecracker processes are still present; drain and stop VMs first"
    fi
}

probe_reflink_dir() {
    local dir="$1"
    local probe_dir
    probe_dir="$(mktemp -d "${dir}/.ocm-reflink-probe.XXXXXX")"
    printf 'ocm reflink probe\n' > "${probe_dir}/src"
    if cp --reflink=always "${probe_dir}/src" "${probe_dir}/dst" 2>/dev/null; then
        rm -rf "$probe_dir"
        return 0
    fi
    rm -rf "$probe_dir"
    return 1
}

verify_state_mount() {
    local fs_type
    fs_type="$(findmnt -T "$OCM_STATE_DIR" -n -o FSTYPE 2>/dev/null || true)"
    [ "$fs_type" = "xfs" ] || err "$OCM_STATE_DIR is mounted, but filesystem is ${fs_type:-unknown}, not xfs"
    probe_reflink_dir "$OCM_STATE_DIR" || err "$OCM_STATE_DIR is XFS but cp --reflink=always failed"
}

upsert_env() {
    local key="$1"
    local value="$2"
    local path="$3"
    local dir
    dir="$(dirname "$path")"
    mkdir -p "$dir"
    if [ ! -f "$path" ]; then
        printf '%s=%s\n' "$key" "$value" > "$path"
        return
    fi
    local tmp
    tmp="$(mktemp "${path}.tmp.XXXXXX")"
    grep -v "^${key}=" "$path" > "$tmp" || true
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
    cat "$tmp" > "$path"
    rm -f "$tmp"
}

write_agent_env() {
    upsert_env "STATE_DIR" "$OCM_STATE_DIR" "$OCM_AGENT_ENV"
    mkdir -p /etc/ocm-agent
    printf 'STATE_DIR=%s\n' "$OCM_STATE_DIR" > /etc/ocm-agent/vm-state.env
    if truthy "$OCM_SET_BROWSER_STATE_DIR"; then
        upsert_env "BROWSER_STATE_DIR" "$OCM_STATE_DIR" "$OCM_AGENT_ENV"
        printf 'BROWSER_STATE_DIR=%s\n' "$OCM_STATE_DIR" > /etc/ocm-agent/browser-state.env
    fi
}

write_systemd_dropin() {
    if [ ! -d /etc/systemd/system ]; then
        return
    fi
    local dropin_dir=/etc/systemd/system/ocm-agent.service.d
    mkdir -p "$dropin_dir"
    cat > "${dropin_dir}/storage.conf" <<EOF
[Unit]
RequiresMountsFor=${OCM_STATE_DIR}
EOF
    if command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload >/dev/null 2>&1 || true
    fi
}

assert_fstab_compatible() {
    local fstab=/etc/fstab
    local src="$OCM_VM_STATE_XFS_IMAGE"
    local dst="$OCM_STATE_DIR"
    [ -f "$fstab" ] || return

    while read -r fs_file fs_mount fs_type _rest; do
        [ -n "${fs_file:-}" ] || continue
        case "$fs_file" in \#*) continue ;; esac
        if [ "${fs_mount:-}" = "$dst" ]; then
            if [ "$fs_file" = "$src" ] && [ "${fs_type:-}" = "xfs" ]; then
                return
            fi
            err "$dst already has a different /etc/fstab entry: $fs_file $fs_mount $fs_type"
        fi
    done < "$fstab"
}

ensure_fstab_entry() {
    local fstab=/etc/fstab
    local src="$OCM_VM_STATE_XFS_IMAGE"
    local dst="$OCM_STATE_DIR"
    touch "$fstab"

    while read -r fs_file fs_mount fs_type _rest; do
        [ -n "${fs_file:-}" ] || continue
        case "$fs_file" in \#*) continue ;; esac
        if [ "${fs_mount:-}" = "$dst" ]; then
            if [ "$fs_file" = "$src" ] && [ "${fs_type:-}" = "xfs" ]; then
                return
            fi
            err "$dst already has a different /etc/fstab entry: $fs_file $fs_mount $fs_type"
        fi
    done < "$fstab"

    printf '%s %s xfs loop,noatime,nofail 0 0\n' "$src" "$dst" >> "$fstab"
}

create_or_verify_image() {
    mkdir -p "$(dirname "$OCM_VM_STATE_XFS_IMAGE")"
    if [ -e "$OCM_VM_STATE_XFS_IMAGE" ]; then
        local fs_type
        fs_type="$(blkid -p -o value -s TYPE "$OCM_VM_STATE_XFS_IMAGE" 2>/dev/null || true)"
        [ "$fs_type" = "xfs" ] || err "$OCM_VM_STATE_XFS_IMAGE exists but is ${fs_type:-not an XFS filesystem}"
        log "Using existing XFS image $OCM_VM_STATE_XFS_IMAGE"
        return
    fi

    log "Creating sparse XFS image $OCM_VM_STATE_XFS_IMAGE ($OCM_VM_STATE_XFS_SIZE)..."
    truncate -s "$OCM_VM_STATE_XFS_SIZE" "$OCM_VM_STATE_XFS_IMAGE"
    mkfs.xfs -f -m reflink=1 "$OCM_VM_STATE_XFS_IMAGE" >/dev/null
}

copy_existing_state_payload() {
    local src_dir="$1"
    local dst_dir="$2"
    [ -d "$src_dir" ] || return
    if [ -z "$(find "$src_dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null || true)" ]; then
        return
    fi

    cp -a --sparse=always "${src_dir}/." "$dst_dir/"
}

json_array_len_or_missing() {
    local path="$1"
    if [ ! -e "$path" ]; then
        printf 'missing'
        return
    fi
    [ -f "$path" ] || err "$path exists but is not a regular file"

    local count
    if ! count="$(jq -e 'if type == "array" then length else error("expected JSON array") end' "$path")"; then
        err "$path is not a valid JSON array; refusing to migrate ambiguous agent state"
    fi
    printf '%s' "$count"
}

top_level_entry_count() {
    local dir="$1"
    if [ ! -d "$dir" ]; then
        printf '0'
        return
    fi
    find "$dir" -mindepth 1 -maxdepth 1 -print 2>/dev/null | wc -l | tr -d '[:space:]'
}

capture_state_inventory() {
    local dir="$1"
    STATE_ENTRY_COUNT="$(top_level_entry_count "$dir")"
    STATE_VMS_COUNT="$(json_array_len_or_missing "${dir}/vms.json")"
    STATE_BROWSER_VMS_COUNT="$(json_array_len_or_missing "${dir}/browser-vms.json")"
}

verify_json_count_preserved() {
    local label="$1"
    local expected="$2"
    local path="$3"
    if [ "$expected" = "missing" ]; then
        return
    fi

    local actual
    actual="$(json_array_len_or_missing "$path")"
    [ "$actual" != "missing" ] || err "$label existed before migration but is missing after copy: $path"
    [ "$actual" = "$expected" ] || err "$label entry count changed during migration: before=$expected after=$actual path=$path"
}

verify_state_inventory_preserved() {
    local dir="$1"
    local phase="$2"
    local actual_entries
    actual_entries="$(top_level_entry_count "$dir")"
    if [ "$STATE_ENTRY_COUNT" -gt 0 ] && [ "$actual_entries" -lt "$STATE_ENTRY_COUNT" ]; then
        err "state entry count shrank during $phase: before=$STATE_ENTRY_COUNT after=$actual_entries dir=$dir"
    fi

    verify_json_count_preserved "vms.json" "$STATE_VMS_COUNT" "${dir}/vms.json"
    verify_json_count_preserved "browser-vms.json" "$STATE_BROWSER_VMS_COUNT" "${dir}/browser-vms.json"
}

rollback_state_dir_move() {
    local backup_dir="$1"
    [ -n "$backup_dir" ] || return
    [ -d "$backup_dir" ] || return

    if mountpoint -q "$OCM_STATE_DIR" 2>/dev/null; then
        if ! umount "$OCM_STATE_DIR" >/dev/null 2>&1; then
            echo "ERROR: rollback could not unmount $OCM_STATE_DIR; backup remains at $backup_dir" >&2
            return 1
        fi
    fi
    if mountpoint -q "$OCM_STATE_DIR" 2>/dev/null; then
        echo "ERROR: rollback refused to modify mounted path $OCM_STATE_DIR; backup remains at $backup_dir" >&2
        return 1
    fi
    rm -rf "$OCM_STATE_DIR"
    mv "$backup_dir" "$OCM_STATE_DIR"
}

state_dir_has_payload() {
    [ -d "$OCM_STATE_DIR" ] || return 1
    [ -n "$(find "$OCM_STATE_DIR" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null || true)" ]
}

mount_image_temporarily() {
    local mount_dir="$1"
    mkdir -p "$mount_dir"
    mount -o loop,noatime "$OCM_VM_STATE_XFS_IMAGE" "$mount_dir"
}

mount_final_state_dir() {
    mkdir -p "$OCM_STATE_DIR"
    mount -o loop,noatime "$OCM_VM_STATE_XFS_IMAGE" "$OCM_STATE_DIR"
}

main() {
    require_root
    require_tools
    validate_paths
    assert_fstab_compatible

    mkdir -p "$OCM_BASE" "$(dirname "$OCM_STATE_DIR")"

    local existing_mount
    existing_mount="$(findmnt -rn -S "$OCM_VM_STATE_XFS_IMAGE" -o TARGET 2>/dev/null | head -n 1 || true)"
    if [ -n "$existing_mount" ]; then
        [ "$existing_mount" = "$OCM_STATE_DIR" ] || err "$OCM_VM_STATE_XFS_IMAGE is already mounted at $existing_mount, not $OCM_STATE_DIR"
    fi

    if mountpoint -q "$OCM_STATE_DIR" 2>/dev/null; then
        log "$OCM_STATE_DIR is already mounted; verifying XFS reflink support..."
        verify_state_mount
        ensure_fstab_entry
        write_agent_env
        write_systemd_dropin
        log "VM state storage already configured at $OCM_STATE_DIR"
        return
    fi

    stop_agent_if_needed
    assert_no_live_vms
    capture_state_inventory "$OCM_STATE_DIR"
    log "Pre-migration state inventory: entries=${STATE_ENTRY_COUNT} vms=${STATE_VMS_COUNT} browser_vms=${STATE_BROWSER_VMS_COUNT}"
    create_or_verify_image

    local tmp_mount
    tmp_mount="$(mktemp -d /tmp/ocm-vm-state-xfs.XXXXXX)"
    mount_image_temporarily "$tmp_mount"
    trap 'umount "$tmp_mount" >/dev/null 2>&1 || true; rmdir "$tmp_mount" >/dev/null 2>&1 || true' EXIT

    log "Preserving full VM state payload into new XFS image..."
    copy_existing_state_payload "$OCM_STATE_DIR" "$tmp_mount"
    verify_state_inventory_preserved "$tmp_mount" "temporary XFS copy"
    probe_reflink_dir "$tmp_mount" || err "temporary XFS mount does not support reflink"
    umount "$tmp_mount"
    rmdir "$tmp_mount"
    trap - EXIT

    local backup_dir=""
    if state_dir_has_payload; then
        backup_dir="${OCM_STATE_DIR}.pre-xfs.$(date -u +%Y%m%dT%H%M%SZ)"
        log "Moving existing state dir aside to $backup_dir"
        mv "$OCM_STATE_DIR" "$backup_dir"
    fi

    local rollback_final_mount=false
    if [ -n "$backup_dir" ]; then
        rollback_final_mount=true
        trap 'if [ "$rollback_final_mount" = true ]; then rollback_state_dir_move "$backup_dir"; fi' EXIT
    fi

    mount_final_state_dir
    verify_state_mount
    verify_state_inventory_preserved "$OCM_STATE_DIR" "final XFS mount"
    rollback_final_mount=false
    trap - EXIT

    ensure_fstab_entry
    write_agent_env
    write_systemd_dropin

    log "VM state XFS storage ready: state_dir=$OCM_STATE_DIR image=$OCM_VM_STATE_XFS_IMAGE"
    log "Check backing usage with: df -h $OCM_BASE"
    log "Check XFS usage with: df -h $OCM_STATE_DIR"

    if truthy "$OCM_RESTART_AGENT"; then
        log "Starting ocm-agent..."
        systemctl start ocm-agent
    fi
}

main "$@"
