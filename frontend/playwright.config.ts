import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:5173";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? "list" : "html",

  use: {
    baseURL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },

  projects: [
    {
      name: "setup",
      testMatch: "**/auth.setup.ts",
    },
    {
      name: "manual-auth",
      testMatch: /manual-auth\.setup\.ts/,
      use: { headless: false },
    },
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        storageState: "e2e/.auth/state.json",
      },
      dependencies: ["setup"],
    },
    {
      // Dev-auth: AUTH_MODE=dev auto-authenticates every request (no login
      // form, no cookie/storageState, no setup dependency). Used by CI
      // (ci/spot-e2e.sh) which runs the control plane in the local/dev profile.
      // Capture screenshot + trace on EVERY run (not just failures) so the
      // uploaded artifact is real validation evidence for the spot E2E gate.
      // No video: recording needs Playwright's ffmpeg from cdn.playwright.dev,
      // which stalls on CI runners (3/3 install attempts timed out post-download
      // in run 27371571014); the trace's screencast film strip covers replay.
      name: "chromium-dev",
      use: {
        ...devices["Desktop Chrome"],
        channel: "chrome",
        screenshot: "on",
        video: "off",
        trace: "on",
      },
    },
  ],

  webServer: process.env.PLAYWRIGHT_BASE_URL
    ? undefined
    : {
        command: "npm run dev",
        url: "http://localhost:5173",
        reuseExistingServer: !process.env.CI,
        timeout: 30_000,
      },
});
