// Global setup — runs in a separate process before all test files.
// Renames .dev.vars so it doesn't override wrangler.test.toml vars,
// and restores it after all tests complete.

import fs from "node:fs";
import path from "node:path";

const devVarsPath = path.resolve(import.meta.dirname, "../../.dev.vars");
const devVarsBackup = devVarsPath + ".bak";

export async function setup() {
  // Rename .dev.vars to prevent it from overriding test vars in wrangler.test.toml
  if (fs.existsSync(devVarsPath)) {
    fs.renameSync(devVarsPath, devVarsBackup);
  }
}

export async function teardown() {
  // Restore .dev.vars
  if (fs.existsSync(devVarsBackup)) {
    fs.renameSync(devVarsBackup, devVarsPath);
  }
}
