#!/usr/bin/env bash
set -u

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR" || exit 1

failures=0
warnings=0

section() {
	printf '\n== %s ==\n' "$1"
}

ok() {
	printf '  OK   %s\n' "$1"
}

warn() {
	warnings=$((warnings + 1))
	printf '  WARN %s\n' "$1"
}

fail() {
	failures=$((failures + 1))
	printf '  FAIL %s\n' "$1"
}

have() {
	command -v "$1" >/dev/null 2>&1
}

find_tool() {
	local cmd="$1"
	local dir
	if command -v "$cmd" >/dev/null 2>&1; then
		command -v "$cmd"
		return 0
	fi
	for dir in /usr/local/sbin /usr/local/bin /usr/sbin /sbin /usr/bin /bin; do
		if [ -x "$dir/$cmd" ]; then
			printf '%s\n' "$dir/$cmd"
			return 0
		fi
	done
	return 1
}

first_line() {
	"$@" 2>&1 | awk 'NR == 1 { print; exit }'
}

require_cmd() {
	local cmd="$1"
	local hint="$2"
	local path
	if have "$cmd"; then
		ok "$cmd found: $(command -v "$cmd")"
	elif path="$(find_tool "$cmd")"; then
		warn "$cmd found at $path but is not on PATH. Add its directory to PATH before running local agent or integration commands."
	else
		fail "$cmd not found. $hint"
	fi
}

warn_cmd() {
	local cmd="$1"
	local hint="$2"
	local path
	if have "$cmd"; then
		ok "$cmd found: $(command -v "$cmd")"
	elif path="$(find_tool "$cmd")"; then
		warn "$cmd found at $path but is not on PATH. Add its directory to PATH if you need this feature."
	else
		warn "$cmd not found. $hint"
	fi
}

config_value_from_file() {
	local key="$1"
	local file="$2"
	awk -v key="$key" '
		/^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
		index($0, "=") == 0 { next }
		{
			k = $0
			sub(/=.*/, "", k)
			gsub(/^[[:space:]]+|[[:space:]]+$/, "", k)
			if (k == key) {
				v = $0
				sub(/^[^=]*=/, "", v)
				gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
				gsub(/^"/, "", v)
				gsub(/"$/, "", v)
				print v
				exit
			}
		}
	' "$file"
}

config_value() {
	local key="$1"
	local fallback="${2:-}"
	local value=""

	if [ "${!key+x}" = "x" ]; then
		printf '%s' "${!key}"
		return 0
	fi

	for file in .env /etc/ocm-agent/agent.env; do
		if [ -r "$file" ]; then
			value="$(config_value_from_file "$key" "$file")"
			if [ -n "$value" ]; then
				printf '%s' "$value"
				return 0
			fi
		fi
	done

	printf '%s' "$fallback"
}

is_placeholder() {
	case "$1" in
		""|change-me|change-me-*|postgres://user:pass@*)
			return 0
			;;
	esac
	return 1
}

version_ge() {
	local have_version="$1"
	local required_version="$2"
	local i have_part required_part
	IFS=. read -r -a have_parts <<<"$have_version"
	IFS=. read -r -a required_parts <<<"$required_version"

	for i in 0 1 2; do
		have_part="${have_parts[$i]:-0}"
		required_part="${required_parts[$i]:-0}"
		if ((10#$have_part > 10#$required_part)); then
			return 0
		fi
		if ((10#$have_part < 10#$required_part)); then
			return 1
		fi
	done
	return 0
}

check_go_version() {
	if ! have go; then
		return
	fi

	local required="1.25.10"
	local raw version
	raw="$(go env GOVERSION 2>/dev/null || true)"
	version="${raw#go}"
	version="${version%%[!0-9.]*}"
	if [ -z "$version" ]; then
		warn "could not parse Go version from: $(first_line go version)"
		return
	fi

	if version_ge "$version" "$required"; then
		ok "go version $version satisfies backend/go.mod requirement $required"
	else
		fail "go version $version is older than backend/go.mod requirement $required. Install Go $required or newer."
	fi
}

check_secret_key() {
	local key
	key="$(config_value SECRET_ENCRYPTION_KEY)"
	if [ -z "$key" ]; then
		warn "SECRET_ENCRYPTION_KEY is not set. Local secret storage needs a 32-byte value, for example: openssl rand -hex 16"
		return
	fi
	if [ "${#key}" -ne 32 ]; then
		fail "SECRET_ENCRYPTION_KEY is ${#key} bytes; backend startup requires exactly 32 bytes."
	else
		ok "SECRET_ENCRYPTION_KEY has the required 32-byte length"
	fi
}

check_config_hint() {
	local key="$1"
	local reason="$2"
	local value
	value="$(config_value "$key")"
	if is_placeholder "$value"; then
		warn "$key is not set to a real local value. $reason"
	else
		ok "$key is configured"
	fi
}

check_file() {
	local path="$1"
	local hint="$2"
	if [ -f "$path" ]; then
		ok "$path exists"
	else
		fail "$path is missing. $hint"
	fi
}

check_kvm() {
	if [ "$(uname -s)" != "Linux" ]; then
		fail "Firecracker requires Linux with KVM. Use bare metal or a VM with nested virtualization enabled."
		return
	fi

	ok "Linux host detected ($(uname -srmo 2>/dev/null || uname -sr))"

	case "$(uname -m)" in
		x86_64|aarch64)
			ok "CPU architecture $(uname -m) is supported by Firecracker"
			;;
		*)
			fail "unsupported CPU architecture $(uname -m). Firecracker release binaries are expected for x86_64 or aarch64."
			;;
	esac

	if [ -e /dev/kvm ]; then
		ok "/dev/kvm exists"
		if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
			ok "current user can access /dev/kvm"
		else
			warn "current user cannot read/write /dev/kvm. Run the agent/tests as root or add the user to the kvm group."
		fi
	else
		fail "/dev/kvm is missing. Enable hardware virtualization or nested virtualization and load kvm_intel or kvm_amd."
		if [ -r /proc/cpuinfo ] && ! grep -Eq '(^flags|^Features).* (vmx|svm|virt) ' /proc/cpuinfo; then
			warn "CPU virtualization flags were not found in /proc/cpuinfo."
		fi
	fi
}

check_firecracker_binary() {
	if have firecracker; then
		ok "firecracker found: $(command -v firecracker)"
		ok "$(first_line firecracker --version)"
	elif [ -x /usr/local/bin/firecracker ]; then
		fail "firecracker exists at /usr/local/bin/firecracker but is not on PATH. Add /usr/local/bin to PATH before starting the agent."
	else
		fail "firecracker not found. Install a Firecracker release binary and ensure firecracker is on PATH."
	fi
}

check_runtime_files() {
	local kernel_path images_dir rootfs_manifest rootfs_path
	kernel_path="$(config_value KERNEL_PATH "/var/lib/ocm/vmlinux")"
	images_dir="$(config_value IMAGES_DIR "/var/lib/ocm/images")"
	rootfs_manifest="$(config_value ROOTFS_GCS_MANIFEST)"
	rootfs_path="${images_dir}/rootfs.ext4"

	if [ -f "$kernel_path" ]; then
		ok "kernel image exists at $kernel_path"
	elif [ "$kernel_path" = "/var/lib/ocm/vmlinux" ] && [ -f /var/lib/ocm/images/vmlinux ]; then
		fail "$kernel_path is missing, but /var/lib/ocm/images/vmlinux exists. Set KERNEL_PATH=/var/lib/ocm/images/vmlinux or create a symlink."
	else
		fail "kernel image is missing at $kernel_path. Place a Firecracker-compatible vmlinux there or set KERNEL_PATH."
	fi

	if [ -n "$rootfs_manifest" ]; then
		ok "ROOTFS_GCS_MANIFEST is configured; rootfs will be staged from that manifest"
	else
		check_file "$rootfs_path" "Run make build-rootfs or place a compatible rootfs.ext4 under IMAGES_DIR."
	fi

	if [ -d "$images_dir" ]; then
		ok "images directory exists at $images_dir"
	else
		warn "images directory $images_dir does not exist yet. Host provisioning or make build-rootfs creates it."
	fi

	local state_dir state_fs
	state_dir="$(config_value STATE_DIR "/var/lib/ocm/vms")"
	if [ -d "$state_dir" ]; then
		ok "state directory exists at $state_dir"
		if have findmnt; then
			state_fs="$(findmnt -T "$state_dir" -n -o FSTYPE 2>/dev/null || true)"
			if [ "$state_fs" = "xfs" ]; then
				ok "state directory is on XFS, suitable for reflink copies"
			elif [ -n "$state_fs" ]; then
				warn "state directory filesystem is $state_fs. XFS with reflink is recommended for fast VM rootfs copies."
			fi
		fi
	else
		warn "state directory $state_dir does not exist yet. Host provisioning creates it."
	fi
}

check_systemd_owner() {
	local owner
	owner="$(config_value VM_RUNTIME_OWNER "systemd-unit")"
	if [ "$owner" = "systemd-unit" ]; then
		require_cmd systemctl "Install systemd tools or set VM_RUNTIME_OWNER=direct for a non-systemd local experiment."
		warn_cmd journalctl "journalctl is useful for ocm-agent logs."
		if [ -d /run/systemd/system ]; then
			ok "systemd appears to be running"
		else
			fail "VM_RUNTIME_OWNER=systemd-unit requires a running systemd host. Set VM_RUNTIME_OWNER=direct or use a systemd-based Linux host."
		fi
	else
		ok "VM_RUNTIME_OWNER=$owner; systemd transient units are not required"
	fi
}

printf 'OpenClaw Machines local/BYO host preflight\n'
printf 'Repository: %s\n' "$ROOT_DIR"

section "Developer tools"
require_cmd bash "Install bash."
require_cmd git "Install git."
require_cmd make "Install make."
require_cmd awk "Install awk; preflight uses it to read config hints."
require_cmd grep "Install grep; preflight uses it for host capability checks."
require_cmd go "Install Go; backend/go.mod declares the required version."
check_go_version
require_cmd node "Install Node.js for the frontend and OpenClaw runtime tooling."
require_cmd npm "Install npm for frontend, worker, and runtime tooling."
warn_cmd curl "curl is used by health checks and setup scripts."
warn_cmd jq "jq is useful for enrollment and host setup scripts."
warn_cmd openssl "openssl is useful for generating local secrets."

if [ -d frontend/node_modules ]; then
	ok "frontend dependencies are installed"
else
	warn "frontend/node_modules is missing. Run npm ci in frontend before frontend tests or dev server work."
fi
if [ -d worker/node_modules ]; then
	ok "worker dependencies are installed"
else
	warn "worker/node_modules is missing. Run npm ci in worker before worker tests."
fi

section "Linux and KVM"
check_kvm

section "Firecracker runtime"
check_firecracker_binary
require_cmd ip "Install iproute2; the agent uses ip to create bridges and TAP devices."
require_cmd iptables "Install iptables; the agent uses it for NAT and isolation rules."
require_cmd sysctl "Install procps; the agent enables IPv4 forwarding through sysctl."
require_cmd mkfs.ext4 "Install e2fsprogs; data volumes and rootfs builds use mkfs.ext4."
require_cmd mount "Install mount utilities."
require_cmd umount "Install mount utilities."
require_cmd truncate "Install coreutils."
require_cmd findmnt "Install util-linux."
check_systemd_owner
check_runtime_files

section "Build helpers"
warn_cmd docker "Docker is required for make build-rootfs and make build-openclaw."
warn_cmd bsdtar "bsdtar/libarchive-tools is required for make build-rootfs."
warn_cmd zstd "zstd is required for compressed runtime/rootfs artifacts."
warn_cmd gsutil "gsutil is only needed if you use GCS-backed artifact downloads from setup scripts."
warn_cmd cloudflared "cloudflared is only needed for Cloudflare tunnel ingress; bridge-only local checks do not require it."

section "Local config hints"
if [ -f .env ]; then
	ok ".env exists"
else
	warn ".env is missing. Copy .env.example to .env or export values in your shell for local services."
fi
if [ -f .env.example ]; then
	ok ".env.example exists"
else
	warn ".env.example is missing; local config examples are unavailable."
fi

check_config_hint DATABASE_URL "Set a local or BYO PostgreSQL URL before running the backend API."
check_config_hint FC_AGENT_TOKEN "Set any strong local token before running ocm-agent."
check_secret_key

auth_mode="$(config_value AUTH_MODE "cfaccess")"
case "$auth_mode" in
	dev)
		if [ "$(config_value OCM_ALLOW_DEV_AUTH)" = "1" ]; then
			ok "AUTH_MODE=dev with OCM_ALLOW_DEV_AUTH=1"
		else
			fail "AUTH_MODE=dev requires OCM_ALLOW_DEV_AUTH=1; backend startup intentionally refuses dev auth without it."
		fi
		;;
	cfaccess)
		warn "AUTH_MODE defaults to cfaccess. For local-only development, set AUTH_MODE=dev and OCM_ALLOW_DEV_AUTH=1; for BYO auth, provide your own CF_ACCESS_TEAM_DOMAIN and CF_ACCESS_AUD."
		;;
	firebase)
		check_config_hint FIREBASE_PROJECT_ID "Set your Firebase project ID for AUTH_MODE=firebase."
		;;
	*)
		fail "AUTH_MODE=$auth_mode is not recognized. Use dev, cfaccess, or firebase."
		;;
esac

printf '\n'
if [ "$failures" -eq 0 ]; then
	printf 'Preflight passed with %d warning(s).\n' "$warnings"
	exit 0
fi

printf 'Preflight failed with %d failure(s) and %d warning(s).\n' "$failures" "$warnings"
printf 'See docs/local-setup.md for local/BYO-host setup expectations.\n'
exit 1
