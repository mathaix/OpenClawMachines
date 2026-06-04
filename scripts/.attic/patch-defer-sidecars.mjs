#!/usr/bin/env node
// Patch openclaw's compiled server.impl-*.js to default `deferSidecars: true`
// instead of `false`, restoring the 4.15 cold-boot behavior where the gateway
// emits `ready` immediately after the HTTP server binds and loads channels +
// sidecars asynchronously.
//
// Why a patch and not a CLI flag: openclaw exposes `deferStartupSidecars` as
// a programmatic option to startGatewayServer() but not as a CLI argument.
// We can't reach it from init-openclaw.sh without modifying upstream's
// compiled JS.
//
// Behavior: locates the unique line
//   deferSidecars: opts.deferStartupSidecars === true
// in dist/*.js and rewrites it to
//   deferSidecars: opts.deferStartupSidecars !== false
// so the default flips from "wait for sidecars" to "defer by default; only
// wait if explicitly opted out". Existing callers passing
// `deferStartupSidecars: false` still get the old (waiting) behavior.
//
// Fail-closed: errors out if the file structure has changed, the target
// string doesn't appear, or appears more than once. The build then halts
// before producing a half-patched artifact.
//
// Usage:
//   node scripts/patch-defer-sidecars.mjs <openclaw-package-root>
//
// Where <openclaw-package-root> is the path with a `dist/` subdirectory
// (e.g. ${BUNDLE_DIR}/node_modules/openclaw).

import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { argv, exit } from "node:process";

const TARGET = "deferSidecars: opts.deferStartupSidecars === true";
const REPLACEMENT = "deferSidecars: opts.deferStartupSidecars !== false";

function fail(msg) {
	console.error(`patch-defer-sidecars: ${msg}`);
	exit(1);
}

const root = argv[2];
if (!root) fail("missing arg: pass <openclaw-package-root>");

const distDir = join(root, "dist");
let files;
try {
	files = readdirSync(distDir).filter((f) => f.endsWith(".js"));
} catch (err) {
	fail(`cannot read ${distDir}: ${err.message}`);
}

const matches = [];
for (const f of files) {
	const path = join(distDir, f);
	const content = readFileSync(path, "utf8");
	const hits = content.split(TARGET).length - 1;
	if (hits > 0) matches.push({ path, content, hits });
}

if (matches.length === 0) {
	// Maybe already patched. Verify the same uniqueness and context invariants
	// we'd require on a fresh patch — refuse to silently pass if dist/ shows
	// signs of an unrelated change that happens to contain the replacement.
	const replacementMatches = [];
	for (const f of files) {
		const path = join(distDir, f);
		const content = readFileSync(path, "utf8");
		const hits = content.split(REPLACEMENT).length - 1;
		if (hits > 0) replacementMatches.push({ path, content, hits });
	}
	if (replacementMatches.length === 0) {
		fail(
			`target string not found in any dist/*.js file:\n` +
			`  "${TARGET}"\n` +
			`Has openclaw refactored startGatewayServer? Update this patch site or remove the patch.`
		);
	}
	if (replacementMatches.length > 1 || replacementMatches[0].hits !== 1) {
		const where = replacementMatches.map((m) => `${m.path} (${m.hits}x)`).join("\n  ");
		fail(
			`already-patched check: replacement string appears in multiple files or multiple times:\n  ${where}\n` +
			`Refusing to silently pass — investigate.`
		);
	}
	// Context guard: confirm the replacement sits inside the post-attach call
	// site (recognizable by neighboring keys like onSidecarsReady, startupTrace,
	// startupTrace.mark("ready")). Catches the case where some unrelated code
	// happens to contain `deferSidecars: opts.deferStartupSidecars !== false`.
	const ctx = replacementMatches[0].content;
	const idx = ctx.indexOf(REPLACEMENT);
	const window = ctx.substring(Math.max(0, idx - 400), idx + REPLACEMENT.length + 200);
	if (!/onSidecarsReady\b/.test(window) || !/startupTrace\b/.test(window)) {
		fail(
			`already-patched check: replacement string is not in the expected post-attach call site\n` +
			`(missing onSidecarsReady/startupTrace context within ±200/400 bytes in ${replacementMatches[0].path}).\n` +
			`Refusing to pass — investigate before assuming the artifact is patched correctly.`
		);
	}
	console.log(`patch-defer-sidecars: already patched (${replacementMatches[0].path})`);
	exit(0);
}

if (matches.length > 1) {
	const where = matches.map((m) => `${m.path} (${m.hits}x)`).join("\n  ");
	fail(
		`target string found in multiple files; expected exactly one:\n  ${where}\n` +
		`Has tsdown's bundling output split? Patch script needs adjustment.`
	);
}

const m = matches[0];
if (m.hits !== 1) {
	fail(
		`target string appears ${m.hits} times in ${m.path}; expected exactly one.\n` +
		`Maybe the same expression is used in two places now. Investigate before patching.`
	);
}

const patched = m.content.replace(TARGET, REPLACEMENT);

// Sanity: replacement count check (paranoid).
if (patched.split(REPLACEMENT).length - 1 !== 1) {
	fail(`post-patch replacement count != 1 in ${m.path}; aborting`);
}
if (patched.includes(TARGET)) {
	fail(`post-patch ${m.path} still contains the original string; aborting`);
}

writeFileSync(m.path, patched);
console.log(`patch-defer-sidecars: patched ${m.path}`);
console.log(`  '${TARGET}'`);
console.log(`  → '${REPLACEMENT}'`);
console.log(`  effect: gateway "ready" fires after HTTP bind; sidecars load async (4.15 behavior)`);
