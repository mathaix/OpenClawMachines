import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    pool: "forks",
    forks: {
      singleFork: true,
    },
    testTimeout: 30_000,
    hookTimeout: 60_000,
    include: ["test/**/*.test.js"],
    fileParallelism: false,
    globalSetup: ["test/helpers/global-setup.js"],
  },
});
