#!/usr/bin/env bash
# Profile browser rootfs cold start inside Firecracker.
#
# Run on a KVM host with firecracker, root privileges, jq, curl, gsutil, and
# zstd installed. The script records rootfs staging/copy timings, network setup,
# Firecracker startup, CDP readiness latency, memory pressure, and basic CPU
# load evidence in a JSON profile.
#
# Usage:
#   sudo MANIFEST_URI=gs://openclawmachines/kernel-browser-rootfs/manifest.json \
#     RUN_LABEL=kernel-browser bash scripts/profile-browser-vm-cold-start.sh

set -euo pipefail

: "${MANIFEST_URI:=gs://openclawmachines/kernel-browser-rootfs/manifest.json}"
: "${KERNEL:=/var/lib/ocm/vmlinux}"
: "${CACHE_DIR:=/var/lib/ocm/browser-profile-cache}"
: "${WORK_DIR:=/tmp/ocm-browser-profile}"
: "${RUN_LABEL:=manual}"
: "${PROFILE_CASE:=cold-start}"
: "${VM_ID:=profile-bvm-$(date +%s)}"
: "${BRIDGE_NAME:=bvmprof-br0}"
: "${BRIDGE_IP:=192.168.210.1}"
: "${VM_IP:=192.168.210.10}"
: "${TAP_NAME:=bvmproftap0}"
: "${VCPUS:=2}"
: "${MEM_MIB:=4096}"
: "${WAIT_CDP_SECS:=600}"
: "${LATE_READINESS_THRESHOLD_SECS:=180}"
: "${SKIP_MANIFEST:=false}"
: "${ROOTFS_SOURCE:=}"
: "${ENABLE_DNAT_TEST:=false}"
: "${CDP_HOST_PORT:=29222}"
: "${NEKO_HOST_PORT:=28080}"
: "${KEEP_RUN_ROOTFS:=false}"
: "${ALLOW_FULL_COPY:=true}"

RUN_ID="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$VM_ID}"
RUN_DIR="$WORK_DIR/$RUN_ID"
OUTPUT="${OUTPUT:-$RUN_DIR/profile.json}"
CONSOLE_LOG="$RUN_DIR/console.log"
SOCKET="$RUN_DIR/firecracker.sock"
RUN_ROOTFS="$RUN_DIR/rootfs.ext4"
FC_PID=""
PRIMARY_IFACE=""
NAT_INSTALLED=false
DNAT_INSTALLED=false

ROOTFS_STAGE_MS=0
ROOTFS_COPY_MS=0
TAP_CREATE_MS=0
NAT_SETUP_MS=0
DNAT_SETUP_MS=0
FIRECRACKER_PROCESS_START_MS=0
FIRECRACKER_CONFIG_MS=0
FIRECRACKER_INSTANCE_START_MS=0
CDP_WAIT_MS=0
LIVE_VIEW_WAIT_MS=0
TOTAL_MS=0

MANIFEST_VERSION=""
MANIFEST_URL=""
ROOTFS_CACHED=false
ROOTFS_SIZE_BYTES=0
ROOTFS_COPY_MODE=""
ROOTFS_COPY_REFLINK_ERROR=""
CDP_READY=false
LIVE_VIEW_READY=false
CHROME_BROWSER=""
CDP_TARGET_COUNT=0
RSS_PEAK_KIB=0
RSS_FINAL_KIB=0

info() { printf -- '-> %s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
now_ms() { date +%s%3N; }

duration_ms_since() {
	local start_ms="$1"
	local end_ms
	end_ms="$(now_ms)"
	printf '%s\n' "$((end_ms - start_ms))"
}

lineage_from_manifest() {
	case "$MANIFEST_URI" in
		*kernel-browser-rootfs*) printf 'kernel-browser-rootfs' ;;
		*browser-rootfs*) printf 'browser-rootfs' ;;
		*) printf 'unknown' ;;
	esac
}

safe_name() {
	printf '%s' "$1" | tr -c 'A-Za-z0-9_.-' '_'
}

cgroup_memory_current() {
	if [ -f /sys/fs/cgroup/memory.current ]; then
		cat /sys/fs/cgroup/memory.current
	elif [ -f /sys/fs/cgroup/memory/memory.usage_in_bytes ]; then
		cat /sys/fs/cgroup/memory/memory.usage_in_bytes
	else
		printf ''
	fi
}

collect_snapshot() {
	local name="$1"
	free -m >"$RUN_DIR/free-$name.txt" 2>/dev/null || true
	cat /proc/meminfo >"$RUN_DIR/meminfo-$name.txt" 2>/dev/null || true
	cat /proc/loadavg >"$RUN_DIR/loadavg-$name.txt" 2>/dev/null || true
	cat /proc/stat >"$RUN_DIR/cpu-stat-$name.txt" 2>/dev/null || true
	if command -v vmstat >/dev/null 2>&1; then
		vmstat 1 2 >"$RUN_DIR/vmstat-$name.txt" 2>/dev/null || true
	fi
	cgroup_memory_current >"$RUN_DIR/cgroup-memory-$name.txt" 2>/dev/null || true
}

free_available_mib() {
	local file="$1"
	awk '/^Mem:/ {print $7 + 0}' "$file" 2>/dev/null || printf '0'
}

loadavg_text() {
	local file="$1"
	tr -d '\n' <"$file" 2>/dev/null || printf ''
}

rss_kib() {
	if [ -n "$FC_PID" ] && kill -0 "$FC_PID" 2>/dev/null; then
		ps -o rss= -p "$FC_PID" 2>/dev/null | awk '{print $1 + 0}'
	else
		printf '0\n'
	fi
}

cleanup() {
	if [ -n "$FC_PID" ] && kill -0 "$FC_PID" 2>/dev/null; then
		kill -KILL "$FC_PID" 2>/dev/null || true
		wait "$FC_PID" 2>/dev/null || true
	fi
	pkill -f "firecracker.*$SOCKET" 2>/dev/null || true

	if [ "$DNAT_INSTALLED" = true ] && [ -n "$PRIMARY_IFACE" ]; then
		iptables -t nat -D PREROUTING -i "$PRIMARY_IFACE" -p tcp --dport "$CDP_HOST_PORT" -j DNAT --to-destination "${VM_IP}:9222" 2>/dev/null || true
		iptables -t nat -D PREROUTING -i "$PRIMARY_IFACE" -p tcp --dport "$NEKO_HOST_PORT" -j DNAT --to-destination "${VM_IP}:8080" 2>/dev/null || true
	fi
	if [ "$NAT_INSTALLED" = true ] && [ -n "$PRIMARY_IFACE" ]; then
		iptables -t nat -D POSTROUTING -s "192.168.210.0/24" -o "$PRIMARY_IFACE" -j MASQUERADE 2>/dev/null || true
		iptables -D FORWARD -i "$BRIDGE_NAME" -o "$PRIMARY_IFACE" -j ACCEPT 2>/dev/null || true
		iptables -D FORWARD -i "$PRIMARY_IFACE" -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
	fi

	ip link del "$TAP_NAME" 2>/dev/null || true
	ip link del "$BRIDGE_NAME" 2>/dev/null || true
	rm -f "$SOCKET"
	if [ "$KEEP_RUN_ROOTFS" != "true" ] && [ "$KEEP_RUN_ROOTFS" != "1" ]; then
		rm -f "$RUN_ROOTFS"
	fi
}
trap cleanup EXIT

if [ "$(id -u)" -ne 0 ]; then
	fail "must run as root"
fi

command -v firecracker >/dev/null 2>&1 || fail "firecracker not found"
command -v curl >/dev/null 2>&1 || fail "curl not found"
command -v jq >/dev/null 2>&1 || fail "jq not found"
command -v ip >/dev/null 2>&1 || fail "ip command not found"
command -v iptables >/dev/null 2>&1 || fail "iptables not found"
[ -f "$KERNEL" ] || fail "kernel not found at $KERNEL"

mkdir -p "$RUN_DIR" "$CACHE_DIR"
LINEAGE="${LINEAGE:-$(lineage_from_manifest)}"
TOTAL_START_MS="$(now_ms)"

info "profiling browser VM cold start"
info "run_id=$RUN_ID lineage=$LINEAGE manifest=$MANIFEST_URI"

collect_snapshot before
dmesg --ctime 2>/dev/null | grep -Ei 'oom|out of memory|killed process' | tail -100 >"$RUN_DIR/dmesg-oom-before.txt" || true

stage_start_ms="$(now_ms)"
if [ "$SKIP_MANIFEST" = "true" ] || [ "$SKIP_MANIFEST" = "1" ]; then
	[ -n "$ROOTFS_SOURCE" ] || fail "ROOTFS_SOURCE is required when SKIP_MANIFEST=true"
	[ -f "$ROOTFS_SOURCE" ] || fail "ROOTFS_SOURCE not found: $ROOTFS_SOURCE"
	ROOTFS_CACHE="$ROOTFS_SOURCE"
	ROOTFS_CACHED=true
	MANIFEST_VERSION="local"
	MANIFEST_URL="file://$ROOTFS_SOURCE"
else
	command -v gsutil >/dev/null 2>&1 || fail "gsutil not found"
	command -v zstd >/dev/null 2>&1 || fail "zstd not found"

	MANIFEST_JSON="$(gsutil cat "$MANIFEST_URI")"
	MANIFEST_VERSION="$(printf '%s' "$MANIFEST_JSON" | jq -r '.version // "unknown"')"
	MANIFEST_URL="$(printf '%s' "$MANIFEST_JSON" | jq -r '.url // empty')"
	[ -n "$MANIFEST_URL" ] || fail "manifest did not include .url"

	CACHE_KEY="$(safe_name "$LINEAGE-$MANIFEST_VERSION")"
	ROOTFS_CACHE="$CACHE_DIR/$CACHE_KEY.ext4"
	if [ -f "$ROOTFS_CACHE" ]; then
		ROOTFS_CACHED=true
	else
		tmp_archive="$RUN_DIR/rootfs-download"
		info "downloading rootfs version $MANIFEST_VERSION"
		gsutil cp "$MANIFEST_URL" "$tmp_archive" >/dev/null
		if file "$tmp_archive" | grep -qi 'zstandard'; then
			zstd -d -f "$tmp_archive" -o "$ROOTFS_CACHE" >/dev/null
		elif printf '%s' "$MANIFEST_URL" | grep -qE '\.zst($|[?#])'; then
			zstd -d -f "$tmp_archive" -o "$ROOTFS_CACHE" >/dev/null
		else
			mv "$tmp_archive" "$ROOTFS_CACHE"
		fi
	fi
fi
ROOTFS_STAGE_MS="$(duration_ms_since "$stage_start_ms")"
ROOTFS_SIZE_BYTES="$(stat -c '%s' "$ROOTFS_CACHE")"

copy_start_ms="$(now_ms)"
if cp --reflink=always "$ROOTFS_CACHE" "$RUN_ROOTFS" 2>"$RUN_DIR/reflink-copy.err"; then
	ROOTFS_COPY_MODE="reflink"
else
	ROOTFS_COPY_REFLINK_ERROR="$(tr -d '\n' <"$RUN_DIR/reflink-copy.err" 2>/dev/null || printf '')"
	rm -f "$RUN_ROOTFS"
	if [ "$ALLOW_FULL_COPY" != "true" ] && [ "$ALLOW_FULL_COPY" != "1" ]; then
		fail "rootfs reflink copy failed and ALLOW_FULL_COPY is not enabled: $ROOTFS_COPY_REFLINK_ERROR"
	fi
	cp --reflink=auto --sparse=always "$ROOTFS_CACHE" "$RUN_ROOTFS"
	ROOTFS_COPY_MODE="full"
fi
ROOTFS_COPY_MS="$(duration_ms_since "$copy_start_ms")"

tap_start_ms="$(now_ms)"
ip link del "$TAP_NAME" 2>/dev/null || true
ip link del "$BRIDGE_NAME" 2>/dev/null || true
ip link add name "$BRIDGE_NAME" type bridge
ip addr add "${BRIDGE_IP}/24" dev "$BRIDGE_NAME"
ip link set "$BRIDGE_NAME" up
ip tuntap add "$TAP_NAME" mode tap
ip link set "$TAP_NAME" master "$BRIDGE_NAME"
ip link set "$TAP_NAME" up
TAP_CREATE_MS="$(duration_ms_since "$tap_start_ms")"

nat_start_ms="$(now_ms)"
sysctl -w net.ipv4.ip_forward=1 >/dev/null
PRIMARY_IFACE="$(ip route show default | awk '{print $5}' | head -1)"
if [ -n "$PRIMARY_IFACE" ]; then
	iptables -t nat -A POSTROUTING -s "192.168.210.0/24" -o "$PRIMARY_IFACE" -j MASQUERADE
	iptables -A FORWARD -i "$BRIDGE_NAME" -o "$PRIMARY_IFACE" -j ACCEPT
	iptables -A FORWARD -i "$PRIMARY_IFACE" -o "$BRIDGE_NAME" -m state --state RELATED,ESTABLISHED -j ACCEPT
	NAT_INSTALLED=true
fi
NAT_SETUP_MS="$(duration_ms_since "$nat_start_ms")"

dnat_start_ms="$(now_ms)"
if [ "$ENABLE_DNAT_TEST" = "true" ] || [ "$ENABLE_DNAT_TEST" = "1" ]; then
	[ -n "$PRIMARY_IFACE" ] || fail "cannot install DNAT test rules without a default interface"
	iptables -t nat -A PREROUTING -i "$PRIMARY_IFACE" -p tcp --dport "$CDP_HOST_PORT" -j DNAT --to-destination "${VM_IP}:9222"
	iptables -t nat -A PREROUTING -i "$PRIMARY_IFACE" -p tcp --dport "$NEKO_HOST_PORT" -j DNAT --to-destination "${VM_IP}:8080"
	DNAT_INSTALLED=true
fi
DNAT_SETUP_MS="$(duration_ms_since "$dnat_start_ms")"

mac_from_ip() {
	local ip="$1"
	local a b c d
	IFS='.' read -r a b c d <<<"$ip"
	printf '02:FC:00:%02x:%02x:%02x' "$b" "$c" "$d"
}
MAC="$(mac_from_ip "$VM_IP")"

fc_start_ms="$(now_ms)"
firecracker --api-sock "$SOCKET" >"$CONSOLE_LOG" 2>&1 &
FC_PID="$!"
for _ in $(seq 1 20); do
	[ -S "$SOCKET" ] && break
	sleep 0.25
done
[ -S "$SOCKET" ] || fail "Firecracker socket did not appear"
FIRECRACKER_PROCESS_START_MS="$(duration_ms_since "$fc_start_ms")"

fc_api() {
	local method="$1" path="$2" body="${3:-}"
	if [ -n "$body" ]; then
		curl --fail --silent --show-error --unix-socket "$SOCKET" -X "$method" "http://localhost$path" \
			-H "Content-Type: application/json" -H "Accept: application/json" -d "$body" >/dev/null
	else
		curl --fail --silent --show-error --unix-socket "$SOCKET" -X "$method" "http://localhost$path" >/dev/null
	fi
}

config_start_ms="$(now_ms)"
fc_api PUT /machine-config "{\"vcpu_count\":$VCPUS,\"mem_size_mib\":$MEM_MIB,\"smt\":false}"
KERNEL_ARGS="console=ttyS0 reboot=k panic=1 pci=off init=/sbin/overlay-init ip=${VM_IP}::${BRIDGE_IP}:255.255.255.0::eth0:off"
fc_api PUT /boot-source "{\"kernel_image_path\":\"$KERNEL\",\"boot_args\":\"$KERNEL_ARGS\"}"
fc_api PUT /drives/rootfs "{\"drive_id\":\"rootfs\",\"path_on_host\":\"$RUN_ROOTFS\",\"is_root_device\":true,\"is_read_only\":false}"
fc_api PUT /network-interfaces/eth0 "{\"iface_id\":\"eth0\",\"host_dev_name\":\"$TAP_NAME\",\"guest_mac\":\"$MAC\"}"
FIRECRACKER_CONFIG_MS="$(duration_ms_since "$config_start_ms")"

instance_start_ms="$(now_ms)"
fc_api PUT /actions '{"action_type":"InstanceStart"}'
FIRECRACKER_INSTANCE_START_MS="$(duration_ms_since "$instance_start_ms")"

cdp_start_ms="$(now_ms)"
attempts="$(((WAIT_CDP_SECS + 1) / 2))"
for _ in $(seq 1 "$attempts"); do
	rss_now="$(rss_kib)"
	if [ "$rss_now" -gt "$RSS_PEAK_KIB" ]; then
		RSS_PEAK_KIB="$rss_now"
	fi
	if curl -sf -m 2 "http://${VM_IP}:9222/json/version" >"$RUN_DIR/cdp-version.json" 2>/dev/null; then
		CDP_READY=true
		break
	fi
	sleep 2
done
CDP_WAIT_MS="$(duration_ms_since "$cdp_start_ms")"
RSS_FINAL_KIB="$(rss_kib)"
if [ "$RSS_FINAL_KIB" -gt "$RSS_PEAK_KIB" ]; then
	RSS_PEAK_KIB="$RSS_FINAL_KIB"
fi

if [ "$CDP_READY" = true ]; then
	CHROME_BROWSER="$(jq -r '.Browser // ""' "$RUN_DIR/cdp-version.json" 2>/dev/null || true)"
	curl -sf -m 3 "http://${VM_IP}:9222/json/list" >"$RUN_DIR/cdp-list.json" 2>/dev/null || true
	CDP_TARGET_COUNT="$(jq 'if type == "array" then length else 0 end' "$RUN_DIR/cdp-list.json" 2>/dev/null || printf '0')"
fi

live_start_ms="$(now_ms)"
for _ in $(seq 1 30); do
	if curl -sf -m 2 "http://${VM_IP}:8080/" >/dev/null 2>&1; then
		LIVE_VIEW_READY=true
		break
	fi
	sleep 2
done
LIVE_VIEW_WAIT_MS="$(duration_ms_since "$live_start_ms")"

collect_snapshot after
dmesg --ctime 2>/dev/null | grep -Ei 'oom|out of memory|killed process' | tail -100 >"$RUN_DIR/dmesg-oom-after.txt" || true
TOTAL_MS="$(duration_ms_since "$TOTAL_START_MS")"

FREE_AVAILABLE_BEFORE_MIB="$(free_available_mib "$RUN_DIR/free-before.txt")"
FREE_AVAILABLE_AFTER_MIB="$(free_available_mib "$RUN_DIR/free-after.txt")"
CGROUP_MEMORY_BEFORE_BYTES="$(tr -d '\n' <"$RUN_DIR/cgroup-memory-before.txt" 2>/dev/null || printf '')"
CGROUP_MEMORY_AFTER_BYTES="$(tr -d '\n' <"$RUN_DIR/cgroup-memory-after.txt" 2>/dev/null || printf '')"
LOADAVG_BEFORE="$(loadavg_text "$RUN_DIR/loadavg-before.txt")"
LOADAVG_AFTER="$(loadavg_text "$RUN_DIR/loadavg-after.txt")"
LATE_READINESS=false
if [ "$CDP_READY" = true ] && [ "$CDP_WAIT_MS" -gt "$((LATE_READINESS_THRESHOLD_SECS * 1000))" ]; then
	LATE_READINESS=true
fi

jq -n \
	--arg run_id "$RUN_ID" \
	--arg run_label "$RUN_LABEL" \
	--arg profile_case "$PROFILE_CASE" \
	--arg timestamp "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	--arg hostname "$(hostname)" \
	--arg kernel "$(uname -r)" \
	--arg manifest_uri "$MANIFEST_URI" \
	--arg manifest_version "$MANIFEST_VERSION" \
	--arg manifest_url "$MANIFEST_URL" \
	--arg lineage "$LINEAGE" \
	--arg vm_id "$VM_ID" \
	--arg vm_ip "$VM_IP" \
	--arg bridge_ip "$BRIDGE_IP" \
	--arg bridge_name "$BRIDGE_NAME" \
	--arg tap_name "$TAP_NAME" \
	--arg chrome_browser "$CHROME_BROWSER" \
	--arg console_log "$CONSOLE_LOG" \
	--arg work_dir "$RUN_DIR" \
	--arg rootfs_copy_mode "$ROOTFS_COPY_MODE" \
	--arg rootfs_copy_reflink_error "$ROOTFS_COPY_REFLINK_ERROR" \
	--arg loadavg_before "$LOADAVG_BEFORE" \
	--arg loadavg_after "$LOADAVG_AFTER" \
	--arg cgroup_memory_before_bytes "$CGROUP_MEMORY_BEFORE_BYTES" \
	--arg cgroup_memory_after_bytes "$CGROUP_MEMORY_AFTER_BYTES" \
	--argjson vcpus "$VCPUS" \
	--argjson mem_mib "$MEM_MIB" \
	--argjson wait_cdp_secs "$WAIT_CDP_SECS" \
	--argjson late_readiness_threshold_secs "$LATE_READINESS_THRESHOLD_SECS" \
	--argjson rootfs_cached "$ROOTFS_CACHED" \
	--argjson rootfs_size_bytes "$ROOTFS_SIZE_BYTES" \
	--argjson rootfs_stage_ms "$ROOTFS_STAGE_MS" \
	--argjson rootfs_copy_ms "$ROOTFS_COPY_MS" \
	--argjson tap_create_ms "$TAP_CREATE_MS" \
	--argjson nat_setup_ms "$NAT_SETUP_MS" \
	--argjson dnat_setup_ms "$DNAT_SETUP_MS" \
	--argjson dnat_enabled "$DNAT_INSTALLED" \
	--argjson firecracker_process_start_ms "$FIRECRACKER_PROCESS_START_MS" \
	--argjson firecracker_config_ms "$FIRECRACKER_CONFIG_MS" \
	--argjson firecracker_instance_start_ms "$FIRECRACKER_INSTANCE_START_MS" \
	--argjson cdp_wait_ms "$CDP_WAIT_MS" \
	--argjson live_view_wait_ms "$LIVE_VIEW_WAIT_MS" \
	--argjson total_ms "$TOTAL_MS" \
	--argjson cdp_ready "$CDP_READY" \
	--argjson live_view_ready "$LIVE_VIEW_READY" \
	--argjson cdp_target_count "$CDP_TARGET_COUNT" \
	--argjson late_readiness "$LATE_READINESS" \
	--argjson free_available_before_mib "$FREE_AVAILABLE_BEFORE_MIB" \
	--argjson free_available_after_mib "$FREE_AVAILABLE_AFTER_MIB" \
	--argjson firecracker_rss_peak_kib "$RSS_PEAK_KIB" \
	--argjson firecracker_rss_final_kib "$RSS_FINAL_KIB" \
	'{
		run_id: $run_id,
		run_label: $run_label,
		profile_case: $profile_case,
		timestamp: $timestamp,
		host: {hostname: $hostname, kernel: $kernel},
		rootfs: {
			manifest_uri: $manifest_uri,
			manifest_version: $manifest_version,
			manifest_url: $manifest_url,
			lineage: $lineage,
			cached: $rootfs_cached,
			size_bytes: $rootfs_size_bytes,
			copy_mode: $rootfs_copy_mode,
			copy_reflink_error: $rootfs_copy_reflink_error
		},
		config: {
			vm_id: $vm_id,
			vm_ip: $vm_ip,
			bridge_ip: $bridge_ip,
			bridge_name: $bridge_name,
			tap_name: $tap_name,
			vcpus: $vcpus,
			mem_mib: $mem_mib,
			wait_cdp_secs: $wait_cdp_secs,
			late_readiness_threshold_secs: $late_readiness_threshold_secs
		},
		timings_ms: {
			rootfs_stage: $rootfs_stage_ms,
			rootfs_copy: $rootfs_copy_ms,
			tap_create: $tap_create_ms,
			nat_setup: $nat_setup_ms,
			dnat_setup: $dnat_setup_ms,
			firecracker_process_start: $firecracker_process_start_ms,
			firecracker_config: $firecracker_config_ms,
			firecracker_instance_start: $firecracker_instance_start_ms,
			cdp_wait: $cdp_wait_ms,
			live_view_wait: $live_view_wait_ms,
			total: $total_ms
		},
		readiness: {
			cdp_ready: $cdp_ready,
			live_view_ready: $live_view_ready,
			chrome_browser: $chrome_browser,
			cdp_target_count: $cdp_target_count,
			late_readiness: $late_readiness
		},
		memory: {
			free_available_before_mib: $free_available_before_mib,
			free_available_after_mib: $free_available_after_mib,
			firecracker_rss_peak_kib: $firecracker_rss_peak_kib,
			firecracker_rss_final_kib: $firecracker_rss_final_kib,
			cgroup_memory_before_bytes: $cgroup_memory_before_bytes,
			cgroup_memory_after_bytes: $cgroup_memory_after_bytes
		},
		cpu: {
			loadavg_before: $loadavg_before,
			loadavg_after: $loadavg_after
		},
		evidence: {
			work_dir: $work_dir,
			console_log: $console_log,
			free_before: ($work_dir + "/free-before.txt"),
			free_after: ($work_dir + "/free-after.txt"),
			meminfo_before: ($work_dir + "/meminfo-before.txt"),
			meminfo_after: ($work_dir + "/meminfo-after.txt"),
			cpu_stat_before: ($work_dir + "/cpu-stat-before.txt"),
			cpu_stat_after: ($work_dir + "/cpu-stat-after.txt"),
			vmstat_before: ($work_dir + "/vmstat-before.txt"),
			vmstat_after: ($work_dir + "/vmstat-after.txt"),
			dmesg_oom_before: ($work_dir + "/dmesg-oom-before.txt"),
			dmesg_oom_after: ($work_dir + "/dmesg-oom-after.txt"),
			cdp_version: ($work_dir + "/cdp-version.json"),
			cdp_list: ($work_dir + "/cdp-list.json")
		}
	}' >"$OUTPUT"

info "profile written to $OUTPUT"
if [ "$CDP_READY" != true ]; then
	info "CDP did not become ready; see $CONSOLE_LOG"
	exit 2
fi
