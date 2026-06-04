#!/bin/bash
# Build an OpenClaw runtime artifact (.tar.zst) using npm flat install.
#
# Layout (inside /data/ocm/runtime/openclaw/current/ at runtime):
#   bin/openclaw          — shell wrapper
#   openclaw.mjs          — node entrypoint (from npm bin)
#   dist/                 — compiled JS
#   dist/extensions/      — bundled plugins
#   node_modules/         — flat npm dependencies
#
# Why npm instead of pnpm: pnpm's content-addressable store uses symlinks
# and hard links that don't survive tar packaging and extraction.
#
# Must be run from the project root on a Linux machine with Docker and zstd.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}/.."
OUTPUT_DIR="${OPENCLAW_ARTIFACTS_DIR:-/var/lib/ocm/openclaw-artifacts}"
IMAGE_NAME="${OPENCLAW_IMAGE_NAME:-ocm-openclaw-runtime}"
CHANNEL="${OPENCLAW_CHANNEL:-stable}"
RUNTIME_EXTERNALS_DIR="${PROJECT_ROOT}/scripts/openclaw-runtime-externals"
RUNTIME_EXTERNALS_MOUNT="/tmp/openclaw-runtime-externals-src"

command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is not installed"; exit 1; }
command -v zstd >/dev/null 2>&1 || { echo "ERROR: zstd is not installed"; exit 1; }

if [ ! -f "${RUNTIME_EXTERNALS_DIR}/package-lock.json" ]; then
	echo "ERROR: ${RUNTIME_EXTERNALS_DIR}/package-lock.json not found"
	exit 1
fi

if docker info >/dev/null 2>&1; then
	DOCKER="docker"
else
	echo "Using sudo for docker commands (user not in docker group)"
	DOCKER="sudo docker"
fi

TMP_DIR="$(mktemp -d)"
CONTAINER_ID=""
cleanup() {
	if [ -n "${CONTAINER_ID}" ]; then
		$DOCKER rm -f "${CONTAINER_ID}" >/dev/null 2>&1 || true
	fi
	rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

if [ -z "${OPENCLAW_VERSION:-}" ]; then
	echo "ERROR: OPENCLAW_VERSION env var is required (e.g. OPENCLAW_VERSION=2026.4.2)"
	exit 1
fi

echo "=========================================="
echo "Building rootfs base image: ${IMAGE_NAME}"
echo "=========================================="
DOCKER_BUILDKIT=1 $DOCKER build -t "${IMAGE_NAME}" -f "${PROJECT_ROOT}/rootfs/Dockerfile.openclaw" "${PROJECT_ROOT}/rootfs"

echo "OpenClaw version: ${OPENCLAW_VERSION}"

echo ""
echo "=========================================="
echo "Installing openclaw@${OPENCLAW_VERSION} via npm (flat node_modules)"
echo "=========================================="

BUNDLE_DIR="${TMP_DIR}/bundle"
mkdir -p "${BUNDLE_DIR}/bin"

# Install inside a container and copy out the flat tree.
# npm --prefix puts everything under PREFIX/lib/node_modules/.
#
# Runtime externals are packages that openclaw's dist imports but doesn't
# declare as dependencies (upstream packaging gap). Without these, surface
# handlers (Slack, Telegram, Discord, Lark, etc.) fail at runtime with
# MODULE_NOT_FOUND errors. Install them from a committed lockfile so the same
# OPENCLAW_VERSION produces the same external dependency set.
#
# Use --no-clobber for most packages to avoid perturbing OpenClaw's own tree.
# signal-exit is special: restore-cursor needs v4's ESM named export, while
# older CJS consumers in OpenClaw still expect the root v3 callable export.
CONTAINER_ID="$($DOCKER run -d \
	-v "${RUNTIME_EXTERNALS_DIR}:${RUNTIME_EXTERNALS_MOUNT}:ro" \
	"${IMAGE_NAME}" /bin/sh -c "
	npm install -g openclaw@${OPENCLAW_VERSION} --prefix /tmp/oc --engine-strict 2>&1 && \
	mkdir -p /tmp/openclaw-runtime-externals && \
	cp -a ${RUNTIME_EXTERNALS_MOUNT}/. /tmp/openclaw-runtime-externals/ && \
	npm ci --ignore-scripts --omit=dev --prefix /tmp/openclaw-runtime-externals 2>&1 && \
	mv /tmp/openclaw-runtime-externals/node_modules/signal-exit /tmp/openclaw-runtime-signal-exit-v4 && \
	mkdir -p /tmp/oc/lib/node_modules/openclaw/node_modules && \
	cp -a --no-clobber /tmp/openclaw-runtime-externals/node_modules/. /tmp/oc/lib/node_modules/openclaw/node_modules/ && \
	mkdir -p /tmp/oc/lib/node_modules/openclaw/node_modules/restore-cursor/node_modules && \
	cp -a /tmp/openclaw-runtime-signal-exit-v4 /tmp/oc/lib/node_modules/openclaw/node_modules/restore-cursor/node_modules/signal-exit && \
	cd /tmp/oc/lib/node_modules/openclaw && \
	node --input-type=module -e 'import \"cli-cursor\";' && \
	node -e 'const onExit = require(\"signal-exit\"); if (typeof onExit !== \"function\") throw new Error(\"root signal-exit callable export unavailable\");' && \
	node -e '
		const fs = require(\"fs\");
		const lock = JSON.parse(fs.readFileSync(\"/tmp/openclaw-runtime-externals/package-lock.json\", \"utf8\"));
		const deps = lock.packages[\"\"].dependencies || {};
		const versions = {};
		for (const name of Object.keys(deps).sort()) {
			const pkg = lock.packages[\"node_modules/\" + name];
			versions[name] = pkg && pkg.version ? pkg.version : deps[name];
		}
		fs.writeFileSync(\"/tmp/oc/openclaw-runtime-externals.versions.json\", JSON.stringify(versions, null, 2) + \"\n\");
	' && \
	echo 'NPM_INSTALL_OK' > /tmp/oc/.done && \
	sleep 600
")"

echo "Waiting for npm install to complete..."
for i in $(seq 1 120); do
	if $DOCKER exec "${CONTAINER_ID}" test -f /tmp/oc/.done 2>/dev/null; then
		break
	fi
	if [ "$i" = "120" ]; then
		echo "ERROR: npm install timed out"
		$DOCKER logs "${CONTAINER_ID}" 2>&1 | tail -20
		exit 1
	fi
	sleep 2
done
echo "npm install complete"

# --- Capture build environment + assert resolved openclaw version ---
# npm install --engine-strict (above) already enforces engines.node, but we record
# it here so the artifact carries an audit trail of what Node/pnpm produced it.
OC_NODE_VERSION="$($DOCKER exec "${CONTAINER_ID}" node --version 2>/dev/null | tr -d 'v\n\r' || echo unknown)"
OC_PNPM_VERSION="$($DOCKER exec "${CONTAINER_ID}" pnpm --version 2>/dev/null | tr -d '\n\r' || echo unknown)"
OC_RESOLVED_VERSION="$($DOCKER exec "${CONTAINER_ID}" node -e "console.log(require('/tmp/oc/lib/node_modules/openclaw/package.json').version)" 2>/dev/null | tr -d '\n\r' || true)"
OC_ENGINES_NODE="$($DOCKER exec "${CONTAINER_ID}" node -e "console.log((require('/tmp/oc/lib/node_modules/openclaw/package.json').engines||{}).node||'')" 2>/dev/null | tr -d '\n\r' || true)"

if [ -z "${OC_RESOLVED_VERSION}" ]; then
	echo "ERROR: could not read resolved openclaw version from installed package"
	exit 1
fi
if [ "${OC_RESOLVED_VERSION}" != "${OPENCLAW_VERSION}" ]; then
	echo "ERROR: openclaw version mismatch"
	echo "  requested: ${OPENCLAW_VERSION}"
	echo "  resolved:  ${OC_RESOLVED_VERSION}"
	exit 1
fi

echo "Build environment:"
echo "  node:     v${OC_NODE_VERSION}"
echo "  pnpm:     ${OC_PNPM_VERSION}"
echo "  openclaw: v${OC_RESOLVED_VERSION} (matches requested)"
echo "  engines:  node ${OC_ENGINES_NODE:-<unspecified>}"

# --- Copy flat node_modules (dependencies) ---
echo "Copying node_modules..."
$DOCKER cp "${CONTAINER_ID}:/tmp/oc/lib/node_modules" "${BUNDLE_DIR}/node_modules"
$DOCKER cp "${CONTAINER_ID}:/tmp/oc/openclaw-runtime-externals.versions.json" "${BUNDLE_DIR}/openclaw-runtime-externals.versions.json"
cp "${RUNTIME_EXTERNALS_DIR}/package.json" "${BUNDLE_DIR}/openclaw-runtime-externals.package.json"
cp "${RUNTIME_EXTERNALS_DIR}/package-lock.json" "${BUNDLE_DIR}/openclaw-runtime-externals.package-lock.json"

# --- Patch broken package.json exports ---
# Several packages (pi-ai, pi-coding-agent, osc-progress) have exports with
# only "import" condition but no "default". jiti (CJS loader used by openclaw)
# needs "default" to resolve them. Patch all packages under openclaw's node_modules.
echo "Patching package.json exports for jiti compatibility..."
find "${BUNDLE_DIR}/node_modules/openclaw/node_modules" -name 'package.json' -maxdepth 3 | while read -r pj; do
	node -e "
		var f='$pj', p=JSON.parse(require('fs').readFileSync(f));
		var n=0;
		if(p.exports) {
			for(var k in p.exports) {
				var e=p.exports[k];
				if(typeof e==='object' && e.import && !e.default) { e.default=e.import; n++; }
			}
		}
		if(n) { require('fs').writeFileSync(f,JSON.stringify(p,null,2)); console.log('  '+p.name+': patched '+n+' exports'); }
	" 2>/dev/null
done
echo "Patch complete"

echo "Validating signal-exit compatibility..."
(
	cd "${BUNDLE_DIR}/node_modules/openclaw"
	node --input-type=module -e 'import "cli-cursor";'
	node -e 'const onExit = require("signal-exit"); if (typeof onExit !== "function") throw new Error("root signal-exit callable export unavailable");'
)

# --- Verify openclaw package has dist/ ---
OC_PKG="${BUNDLE_DIR}/node_modules/openclaw"
if [ ! -d "${OC_PKG}/dist" ]; then
	echo "ERROR: openclaw dist/ not found"
	exit 1
fi

echo "Applying OpenClaw runtime patches..."
node "${PROJECT_ROOT}/scripts/patch-openclaw-runtime.mjs" "${OC_PKG}"
node "${PROJECT_ROOT}/scripts/test-openclaw-runtime-patches.mjs" "${OC_PKG}"

# --- Copy the entrypoint (openclaw.mjs) into the package dir ---
# The npm bin script resolves dist/ relative to its own location via import.meta.url.
# It must live inside the openclaw package directory.
echo "Copying entrypoint..."
$DOCKER exec "${CONTAINER_ID}" cat /tmp/oc/bin/openclaw > "${OC_PKG}/openclaw.mjs"

# --- Bundle platform plugins into dist/extensions ---
# Platform plugins are installed here alongside openclaw
# so they're versioned together in the artifact. User-installed plugins live
# separately on the data volume (~/.openclaw/extensions/) and are NOT bundled here.
EXT_DIR="${OC_PKG}/dist/extensions"
mkdir -p "${EXT_DIR}"

echo "Installing opik-openclaw plugin..."
OPIK_TGZ="${PROJECT_ROOT}/plugins/opik-openclaw-plugin.tgz"
if [ ! -f "${OPIK_TGZ}" ]; then
	echo "ERROR: ${OPIK_TGZ} not found"
	echo "Build it with: bash scripts/build-opik-plugin.sh"
	exit 1
fi
mkdir -p "${EXT_DIR}/opik-openclaw"
tar -xzf "${OPIK_TGZ}" -C "${EXT_DIR}/opik-openclaw" --strip-components=1

# --- Scrub unused channel adapters (Phase 1 cold-boot optimization) ---
# OpenClaw 4.26 awaits sidecar startup before signaling "ready" — every bundled
# channel adapter gets imported even when no tenant has it configured. Channel
# SDKs (Matrix js-sdk, MS Teams + jwks-rsa, baileys, Discord/carbon, etc.) are
# heavy; removing unused ones cuts cold-boot time materially.
#
# OCM's product surface exposes telegram, slack, discord, whatsapp via
# ChannelTokenFields in backend/internal/configassembly/assembler.go.
# The keep-list mirrors that surface 1:1, enforced by the
# TestChannelKeepListMatchesBuildScript invariant. The scrub loop below
# only operates on in-tree extensions (SHIPPED_CHANNELS_LIST), so listing
# externalized channels here is harmless declarative — it documents the
# product surface without claiming the artifact contains them.
#
# As of openclaw 5.2: telegram + slack still ship in-tree (the scrub keeps
# them). discord + whatsapp were externalized to @openclaw/{discord,whatsapp}
# npm packages and are NOT present in this artifact. If a tenant enables
# either on a 5.2-built VM, the gateway will fail to load the channel.
# OCM has zero users on either today, so this is documented as known
# degraded state until a future change either:
#   (a) installs @openclaw/discord + @openclaw/whatsapp at build time
#       (versions in dist/scripts/lib/official-external-channel-catalog.json),
#       or
#   (b) removes discord/whatsapp from ChannelTokenFields entirely.
#
# If ChannelTokenFields gains a new entry, this list must be updated in
# the same change (the Go invariant test enforces this).
#
OCM_KEEP_CHANNELS_DEFAULT="telegram slack discord whatsapp"
OCM_KEEP_CHANNELS="${OCM_KEEP_CHANNELS:-${OCM_KEEP_CHANNELS_DEFAULT}}"

# Discover what channel adapters this openclaw version actually ships, then
# fail the build on any unknown channel (denylist drift protection).
SHIPPED_CHANNELS_LIST="$(node -e '
	const fs = require("fs"), path = require("path");
	const ext = process.argv[1];
	const out = [];
	for (const dir of fs.readdirSync(ext)) {
		const manifest = path.join(ext, dir, "openclaw.plugin.json");
		if (!fs.existsSync(manifest)) continue;
		try {
			const m = JSON.parse(fs.readFileSync(manifest, "utf8"));
			if (Array.isArray(m.channels) && m.channels.length) out.push(dir);
		} catch {}
	}
	console.log(out.sort().join(" "));
' "${EXT_DIR}")"

# Known channel set as of openclaw 2026.5.19. The 5.2 release externalized
# 17 channel plugins out of dist/extensions/ into separate @openclaw/* npm
# packages; 8 channels now ship in-tree. New upstream channels must
# be classified explicitly (either added to keep list or to this set).
OCM_KNOWN_CHANNELS="clickclack imessage irc matrix mattermost signal slack telegram"

for ch in ${SHIPPED_CHANNELS_LIST}; do
	case " ${OCM_KNOWN_CHANNELS} " in
		*" ${ch} "*) ;;
		*)
			echo "ERROR: unknown channel adapter '${ch}' in openclaw dist/extensions"
			echo "       Add it to OCM_KEEP_CHANNELS (to keep) or OCM_KNOWN_CHANNELS (to scrub)"
			echo "       in scripts/build-openclaw-runtime.sh."
			echo "       This guard prevents new upstream channels from silently bypassing the scrub."
			exit 1
			;;
	esac
done

OCM_SCRUBBED_CHANNELS_LIST=""
SCRUB_COUNT=0
for ch in ${SHIPPED_CHANNELS_LIST}; do
	keep=0
	for k in ${OCM_KEEP_CHANNELS}; do
		[ "$k" = "$ch" ] && keep=1 && break
	done
	[ "$keep" = "1" ] && continue
	rm -rf "${EXT_DIR}/${ch}"
	OCM_SCRUBBED_CHANNELS_LIST="${OCM_SCRUBBED_CHANNELS_LIST}${OCM_SCRUBBED_CHANNELS_LIST:+ }${ch}"
	SCRUB_COUNT=$((SCRUB_COUNT + 1))
done
echo "Scrubbed ${SCRUB_COUNT} unused channel adapter(s): ${OCM_SCRUBBED_CHANNELS_LIST:-none}"
echo "Kept channel adapter(s): ${OCM_KEEP_CHANNELS}"

echo "Bundled plugins (post-scrub): $(ls "${EXT_DIR}")"

# Note: the `defer-sidecars` patcher (which simulated 4.15's lazy-load on
# top of 4.16-4.26 artifacts) was retired when OCM jumped to openclaw 5.2.
# 5.2 narrows the runtime preloader natively (scoped by config, channels,
# slots, auto-enable rules), so the patch is no longer needed and would
# fail on a target string that no longer exists upstream. The patcher script
# is preserved at scripts/.attic/patch-defer-sidecars.mjs for archaeology.

# Post-scrub safety: import openclaw's main entry to exercise the eager part of
# the import graph. If a kept module references a scrubbed sibling via a
# relative import (which the import-drift scanner ignores), this fails loudly
# at build time instead of in production.
#
# Uses dynamic import() because openclaw's entry is ESM with top-level await
# (require() rejects those). We only catch resolution/syntax errors — runtime
# initialization errors (DB connections, etc.) are out of scope here.
echo "Post-scrub import check..."
$DOCKER run --rm \
	-v "${BUNDLE_DIR}:/bundle:ro" \
	"${IMAGE_NAME}" node --input-type=module -e "
		try {
			await import('/bundle/node_modules/openclaw/dist/index.js');
			console.log('post-scrub import: OK');
		} catch (err) {
			// Distinguish module-resolution errors (what we care about) from
			// runtime errors (env-dependent, not our concern at build time).
			if (err && (err.code === 'ERR_MODULE_NOT_FOUND' || err.code === 'MODULE_NOT_FOUND' || /Cannot find module/.test(err.message))) {
				console.error('post-scrub import failed (module resolution):', err.message);
				process.exit(1);
			}
			console.log('post-scrub import: OK (resolved cleanly; runtime error ignored: ' + (err.message || err) + ')');
		}
	"

# --- Import-drift check ---
# Walk openclaw's dist/ and each bundled plugin's dist/, collect every bare-specifier
# import/require, and assert it resolves from the importing file via Node's module
# resolver. Catches the case where an upstream bump drops a transitively-provided
# dep we depend on, before the artifact ships to any VM.
echo "Running import-drift check..."
IMPORT_CHECK_JS="${TMP_DIR}/import-drift-check.mjs"
cat > "${IMPORT_CHECK_JS}" <<'DRIFTCHECK'
import { createRequire, builtinModules } from "module";
import fs from "fs";
import path from "path";

const bundleRoot = "/bundle";
const ocPkg = path.join(bundleRoot, "node_modules/openclaw");
const extDir = path.join(ocPkg, "dist/extensions");
const builtins = new Set(builtinModules);
const scanRoots = [ocPkg + "/dist"];
if (fs.existsSync(extDir)) {
  for (const name of fs.readdirSync(extDir)) {
    const plug = path.join(extDir, name);
    if (!fs.statSync(plug).isDirectory()) continue;
    const pdist = path.join(plug, "dist");
    scanRoots.push(fs.existsSync(pdist) ? pdist : plug);
  }
}

// Known-acceptable unresolved imports. Each entry MUST have a rationale.
// Add here only after investigating — the default answer is "declare it in
// scripts/openclaw-runtime-externals/package.json".
const allowlist = new Map([
  ["node-llama-cpp", "peerDependency in openclaw — user-installed on demand, gated behind engine selection"],
  ["@aws-sdk/signature-v4a", "appears only in aws-sdk error-message strings instructing users to install it; not actually imported"],
  ["@discordjs/opus", "optionalDependency in openclaw — native C++ binding for Discord voice; not used on our VMs and commonly skipped when native build fails"],
  ["vitest", "OpenClaw 2026.5.19 ships test-helper modules in dist/; they are not imported by the runtime entrypoint and are excluded from the production artifact deps"],
  // Note: Lark/Feishu/Matrix/Nostr/QQ allowlist entries removed — those channels
  // are scrubbed from dist/extensions/ at build time (see scrub block above).
]);

// Match `import X from "…"`, `import("…")`, `require("…")`.
// Captures (1) the optional `type` keyword (so we can skip type-only imports
// in .ts files — those don't need runtime resolution), (2) the module specifier.
const importRe = /(?:^|[^\w.$])(?:import\s+(type\s+)?(?:[\w*{},\s]+\s+from\s+)?|import\s*\(|require\s*\()\s*["']([^"']+)["']/g;
const blockCommentRe = /\/\*[\s\S]*?\*\//g;
const lineCommentRe = /(^|[^:\\])\/\/[^\n]*/g;
const problems = new Map();

function pkgName(spec) {
  if (!spec || spec.startsWith(".") || spec.startsWith("/") || spec.startsWith("node:")) return null;
  if (!/^[@a-zA-Z0-9_-]/.test(spec)) return null;
  return spec.startsWith("@") ? spec.split("/").slice(0, 2).join("/") : spec.split("/")[0];
}

function stripComments(src) {
  return src.replace(blockCommentRe, "").replace(lineCommentRe, "$1");
}

function walk(dir) {
  for (const f of fs.readdirSync(dir, { withFileTypes: true })) {
    const fp = path.join(dir, f.name);
    if (f.isDirectory()) {
      if (f.name === "node_modules") continue;
      walk(fp);
      continue;
    }
    if (!/\.(m?[jt]s|c[jt]s)$/.test(f.name)) continue;
    if (/\.d\.(m?[jt]s|c[jt]s)$/.test(f.name)) continue; // type-only declarations, not runtime
    const src = stripComments(fs.readFileSync(fp, "utf8"));
    const req = createRequire(fp);
    const seenHere = new Set();
    for (const match of src.matchAll(importRe)) {
      if (match[1]) continue; // `import type ...` — no runtime resolution needed
      const spec = match[2];
      const name = pkgName(spec);
      if (!name) continue;
      if (builtins.has(name)) continue;
      if (name === "openclaw") continue;
      if (allowlist.has(name)) continue;
      if (seenHere.has(spec)) continue;
      seenHere.add(spec);
      try {
        req.resolve(spec);
      } catch {
        const rel = fp.replace(bundleRoot + "/", "");
        if (!problems.has(name)) problems.set(name, []);
        problems.get(name).push(rel);
      }
    }
  }
}

let scanned = 0;
for (const dir of scanRoots) {
  if (!fs.existsSync(dir)) continue;
  walk(dir);
  scanned++;
}

if (problems.size > 0) {
  console.error("ERROR: import-drift check failed — packages imported but unresolvable:");
  for (const [name, importers] of problems) {
    console.error(`  ${name}`);
    for (const imp of importers.slice(0, 3)) console.error(`    from ${imp}`);
    if (importers.length > 3) console.error(`    ... and ${importers.length - 3} more importer(s)`);
  }
  console.error("");
  console.error("Fix options:");
  console.error("  1. Declare the missing package in scripts/openclaw-runtime-externals/package.json");
  console.error("     and run `npm install` in that directory to refresh package-lock.json.");
  console.error("  2. If the import is genuinely acceptable (peer-dep, error-string false positive, etc.),");
  console.error("     add it to the allowlist in scripts/build-openclaw-runtime.sh with a rationale.");
  process.exit(1);
}
console.log(`Import-drift check passed (${scanned} dir(s) scanned, ${allowlist.size} entr${allowlist.size === 1 ? "y" : "ies"} allowlisted).`);
DRIFTCHECK

$DOCKER run --rm \
	-v "${BUNDLE_DIR}:/bundle:ro" \
	-v "${IMPORT_CHECK_JS}:/tmp/import-drift-check.mjs:ro" \
	"${IMAGE_NAME}" node /tmp/import-drift-check.mjs

EXTERNAL_RUNTIME_DEPS_JSON="$(cat "${BUNDLE_DIR}/openclaw-runtime-externals.versions.json")"
# Build the channel scrub report for forensic comparison across builds.
SCRUB_REPORT_JSON="$(node -e '
	const kept = process.argv[1].trim().split(/\s+/).filter(Boolean).sort();
	const scrubbed = process.argv[2].trim().split(/\s+/).filter(Boolean).sort();
	console.log(JSON.stringify({kept, scrubbed, kept_count: kept.length, scrubbed_count: scrubbed.length}));
' "${OCM_KEEP_CHANNELS}" "${OCM_SCRUBBED_CHANNELS_LIST}")"
cat > "${BUNDLE_DIR}/openclaw-build-info.json" <<EOF
{
  "version": "v${OC_RESOLVED_VERSION}",
  "requested_version": "v${OPENCLAW_VERSION}",
  "resolved_version": "v${OC_RESOLVED_VERSION}",
  "channel": "${CHANNEL}",
  "install_method": "npm-flat",
  "build_environment": {
    "node": "v${OC_NODE_VERSION}",
    "pnpm": "${OC_PNPM_VERSION}",
    "engines_node": "${OC_ENGINES_NODE}"
  },
  "external_runtime_dependencies": ${EXTERNAL_RUNTIME_DEPS_JSON},
  "channel_scrub": ${SCRUB_REPORT_JSON},
  "bundled_plugins": {
    "opik-openclaw": "plugins/opik-openclaw-plugin.tgz"
  }
}
EOF

cat > "${BUNDLE_DIR}/bin/openclaw" <<'WRAPPER'
#!/bin/sh
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT="${SCRIPT_DIR}/.."
export NODE_PATH="${ROOT}/node_modules:${ROOT}/node_modules/openclaw/node_modules"
# Patch broken package.json exports (pi-ai missing "default" condition)
for pj in "${ROOT}"/node_modules/openclaw/node_modules/@mariozechner/pi-ai/package.json; do
  [ -f "$pj" ] && node -e "
    const f='$pj',p=JSON.parse(require('fs').readFileSync(f));
    if(p.exports&&p.exports['.']&&!p.exports['.'].default){
      p.exports['.'].default=p.exports['.'].import||p.main||'./dist/index.js';
      require('fs').writeFileSync(f,JSON.stringify(p,null,2));
    }" 2>/dev/null
done
exec node "${ROOT}/node_modules/openclaw/openclaw.mjs" "$@"
WRAPPER
chmod +x "${BUNDLE_DIR}/bin/openclaw"

# --- Package ---
mkdir -p "${OUTPUT_DIR}"
ARTIFACT_PATH="${OUTPUT_DIR}/openclaw-v${OPENCLAW_VERSION}-linux-amd64.tar.zst"

echo ""
echo "Packaging artifact: ${ARTIFACT_PATH}"
tar -C "${BUNDLE_DIR}" -cf - . | zstd -3 -T0 -f -o "${ARTIFACT_PATH}"

cp "${BUNDLE_DIR}/openclaw-build-info.json" "${OUTPUT_DIR}/openclaw-v${OPENCLAW_VERSION}.build-info.json"

echo ""
echo "=========================================="
echo "OpenClaw runtime artifact built"
echo "=========================================="
echo "Version:  v${OPENCLAW_VERSION}"
echo "Artifact: ${ARTIFACT_PATH}"
echo "Size:     $(du -h "${ARTIFACT_PATH}" | cut -f1)"
echo "=========================================="
