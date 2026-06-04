#!/usr/bin/env node
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

const packageRoot = process.argv[2];
if (!packageRoot) {
  console.error("Usage: node scripts/test-openclaw-runtime-patches.mjs <openclaw-package-root>");
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

function findFileContaining(needle) {
  const matches = [];
  for (const file of walk(distRoot)) {
    if (fs.readFileSync(file, "utf8").includes(needle)) matches.push(file);
  }
  if (matches.length !== 1) {
    throw new Error(`expected one match for ${needle}, found ${matches.length}`);
  }
  return matches[0];
}

findFileContaining("registrationTimeoutMs: Math.min(timeoutMs, 2e3)");
for (const file of walk(distRoot)) {
  if (fs.readFileSync(file, "utf8").includes("registrationTimeoutMs: 100")) {
    throw new Error(`unpatched native hook relay timeout remains in ${path.relative(packageRoot, file)}`);
  }
}

const requireFromOpenClaw = createRequire(path.join(packageRoot, "package.json"));
await import(pathToFileURL(requireFromOpenClaw.resolve("cli-cursor")).href);
const onExit = requireFromOpenClaw("signal-exit");
if (typeof onExit !== "function") {
  throw new Error("root signal-exit callable export unavailable");
}

console.log("openclaw runtime patches ok");
