#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";

const packageRoot = process.argv[2];
if (!packageRoot) {
  console.error("Usage: node scripts/patch-openclaw-runtime.mjs <openclaw-package-root>");
  process.exit(2);
}

const distRoot = path.join(packageRoot, "dist");
if (!fs.existsSync(distRoot)) {
  console.error(`ERROR: openclaw dist/ not found at ${distRoot}`);
  process.exit(1);
}

function* walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      yield* walk(full);
    } else if (entry.isFile() && full.endsWith(".js")) {
      yield full;
    }
  }
}

function countOccurrences(source, needle) {
  let count = 0;
  let offset = 0;
  while (true) {
    const index = source.indexOf(needle, offset);
    if (index === -1) return count;
    count += 1;
    offset = index + needle.length;
  }
}

function patchUnique({ label, needle, replacement }) {
  const hits = [];
  for (const file of walk(distRoot)) {
    const source = fs.readFileSync(file, "utf8");
    const count = countOccurrences(source, needle);
    if (count > 0) hits.push({ file, source, count });
  }

  if (hits.length === 0) {
    const alreadyPatched = [];
    for (const file of walk(distRoot)) {
      const source = fs.readFileSync(file, "utf8");
      if (source.includes(replacement)) alreadyPatched.push(file);
    }
    if (alreadyPatched.length === 1) {
      console.log(`  ${label}: already patched (${path.relative(packageRoot, alreadyPatched[0])})`);
      return;
    }
    console.error(`ERROR: patch target not found: ${label}`);
    process.exit(1);
  }

  if (hits.length !== 1 || hits[0].count !== 1) {
    console.error(`ERROR: patch target is ambiguous: ${label}`);
    for (const hit of hits) {
      console.error(`  ${path.relative(packageRoot, hit.file)} (${hit.count} occurrence(s))`);
    }
    process.exit(1);
  }

  const hit = hits[0];
  fs.writeFileSync(hit.file, hit.source.replace(needle, replacement));
  console.log(`  ${label}: patched ${path.relative(packageRoot, hit.file)}`);
}

patchUnique({
  label: "native hook relay registration timeout",
  needle: "registrationTimeoutMs: 100,\n\t\t\ttimeoutMs",
  replacement: "registrationTimeoutMs: Math.min(timeoutMs, 2e3),\n\t\t\ttimeoutMs",
});
