#!/usr/bin/env node
// Debug script: test the gateway's writeFileWithinRoot to capture the actual error.
// Usage: node /usr/local/bin/debug-gateway-write.js [rootDir] [filename]
//
// Defaults:
//   rootDir  = /home/openclaw/.openclaw/workspace
//   filename = TEST_DEBUG.md

const path = require("path");
const fs = require("fs");
const { execFileSync } = require("child_process");

async function loadWriteFileWithinRoot() {
  const foundationModule =
    "/ocm-runtime/node_modules/openclaw/dist/plugin-sdk/memory-core-host-engine-foundation.js";
  const mod = await import(foundationModule);
  if (typeof mod.writeFileWithinRoot !== "function") {
    throw new Error(`writeFileWithinRoot not exported from ${foundationModule}`);
  }
  console.log("loaded:", foundationModule);
  return mod.writeFileWithinRoot;
}

const rootDir = process.argv[2] || "/home/openclaw/.openclaw/workspace";
const filename = process.argv[3] || "TEST_DEBUG.md";
const content = "debug-test-" + Date.now();

async function testWrite(writeFileWithinRoot, label, root) {
  console.log("\n=== " + label + " ===");
  console.log("rootDir:", root);
  try {
    await writeFileWithinRoot({
      rootDir: root,
      relativePath: filename,
      data: content,
      encoding: "utf8",
    });
    console.log("RESULT: OK");
    const p = path.join(root, filename);
    if (fs.existsSync(p)) fs.unlinkSync(p);
  } catch (err) {
    console.error("RESULT: FAIL");
    console.error("  type:", err.constructor.name);
    console.error("  code:", err.code);
    console.error("  message:", err.message);
    if (err.cause) {
      console.error(
        "  cause.type:",
        err.cause.constructor ? err.cause.constructor.name : typeof err.cause,
      );
      console.error("  cause.message:", err.cause.message || String(err.cause));
    }
    if (err.stack) {
      console.error("  stack:", err.stack.split("\n").slice(0, 8).join("\n"));
    }
  }
}

async function run() {
  const writeFileWithinRoot = await loadWriteFileWithinRoot();
  const realRoot = fs.realpathSync(rootDir);
  console.log("rootDir:", rootDir);
  console.log("realpath:", realRoot);

  console.log("\n=== Environment ===");
  try {
    const st = fs.lstatSync("/home/openclaw");
    console.log("/home/openclaw isSymlink:", st.isSymbolicLink());
    if (st.isSymbolicLink()) {
      console.log("/home/openclaw target:", fs.readlinkSync("/home/openclaw"));
    }
  } catch (e) {
    console.error("/home/openclaw:", e.message);
  }

  try {
    console.log("python3:", execFileSync("which", ["python3"]).toString().trim());
  } catch {
    console.log("python3: NOT FOUND");
  }

  const mounts = fs.readFileSync("/proc/mounts", "utf8");
  for (const line of mounts.split("\n")) {
    if (line.includes("home") || line.includes("workspace")) {
      console.log("mount:", line);
    }
  }

  await testWrite(writeFileWithinRoot, "Test 1: lexical path", rootDir);

  if (realRoot !== rootDir) {
    await testWrite(writeFileWithinRoot, "Test 2: realpath", realRoot);
  } else {
    console.log("\n=== Test 2: skipped (same as lexical) ===");
  }
}

run().catch((err) => {
  console.error("FAIL:", err && err.message ? err.message : String(err));
  process.exit(1);
});
