// Singleton worker instance — lazily initialized, shared across all test files.
// With singleFork: true, all tests run in the same process,
// so this module is imported once and the instance is reused.

import { unstable_startWorker } from "wrangler";
import { startMockServer, stopMockServer } from "./mock-server.js";

let instance = null;
let initializing = null;

export async function getWorkerInstance() {
  if (instance) return instance;
  if (initializing) return initializing;

  initializing = (async () => {
    const mockServer = await startMockServer();

    const worker = await unstable_startWorker({
      config: "wrangler.test.toml",
    });

    instance = {
      worker,
      mockServer,
      mockPort: String(mockServer.port),
    };

    return instance;
  })();

  instance = await initializing;
  initializing = null;
  return instance;
}

// Clean up on process exit (called automatically when test runner finishes)
process.on("beforeExit", async () => {
  if (!instance) return;
  await instance.worker?.dispose().catch(() => {});
  await stopMockServer(instance.mockServer).catch(() => {});
  instance = null;
});
