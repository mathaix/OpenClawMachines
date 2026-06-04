# Studio Architecture Patterns — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Adopt 5 architectural patterns from openclaw-studio into the OCM frontend: structured errors, design tokens, operation modules, mutation queue hook, and WebSocket policy/executor split.

**Architecture:** Bottom-up layered approach. Each layer is independently shippable and testable. Components refactored incrementally to consume new layers. No new product features — this is pure architecture.

**Tech Stack:** React 18, TypeScript 5, Vitest, Tailwind CSS 3, existing `useReconnectingWebSocket` hook.

---

### Task 1: Structured Error Types

**Files:**
- Create: `frontend/src/lib/errors.ts`
- Create: `frontend/src/lib/errors.test.ts`
- Modify: `frontend/src/lib/api.ts:13-35` (request function)
- Modify: `frontend/src/lib/api.ts:293-335` (gatewayRPC function)
- Modify: `frontend/src/lib/api.test.ts:67-89` (error tests)

**Step 1: Write the ApiError class**

Create `frontend/src/lib/errors.ts`:

```typescript
export class ApiError extends Error {
  public readonly code: string;
  public readonly status: number;
  public readonly retryable: boolean;

  constructor(
    message: string,
    code: string,
    status: number,
    retryable = false,
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.retryable = retryable;
  }
}
```

**Step 2: Write failing tests for ApiError**

Create `frontend/src/lib/errors.test.ts`:

```typescript
import { describe, it, expect } from "vitest";
import { ApiError } from "./errors";

describe("ApiError", () => {
  it("extends Error and has correct name", () => {
    const err = new ApiError("not found", "not_found", 404);
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("ApiError");
    expect(err.message).toBe("not found");
  });

  it("exposes code, status, and retryable", () => {
    const err = new ApiError("overloaded", "rate_limit", 429, true);
    expect(err.code).toBe("rate_limit");
    expect(err.status).toBe(429);
    expect(err.retryable).toBe(true);
  });

  it("defaults retryable to false", () => {
    const err = new ApiError("bad input", "validation", 400);
    expect(err.retryable).toBe(false);
  });
});
```

**Step 3: Run tests to verify they pass**

Run: `cd frontend && npx vitest run src/lib/errors.test.ts`
Expected: 3 PASS

**Step 4: Update api.ts request() to throw ApiError**

In `frontend/src/lib/api.ts`, change the `request()` function error path from:

```typescript
if (!res.ok) {
  const body = await res.json().catch(() => ({ error: res.statusText }));
  throw new Error(body.error || res.statusText);
}
```

To:

```typescript
if (!res.ok) {
  const body = await res.json().catch(() => ({ error: res.statusText }));
  throw new ApiError(
    body.error || res.statusText,
    body.code || "unknown",
    res.status,
    res.status === 429 || res.status >= 500,
  );
}
```

Add the import at the top of api.ts:

```typescript
import { ApiError } from "./errors";
```

**Step 5: Update api.ts gatewayRPC() to throw ApiError**

In `gatewayRPC()`, change the three error sites:

```typescript
// Timeout
throw new ApiError("Gateway RPC timed out", "rpc_timeout", 0, true);

// RPC error response
throw new ApiError(data.error.message || "RPC error", "rpc_error", 0, false);

// Invalid response
throw new ApiError("Invalid RPC response", "rpc_parse_error", 0, false);

// WebSocket connection failed
throw new ApiError("WebSocket connection failed", "ws_connect_failed", 0, true);

// WebSocket closed abnormally
throw new ApiError(`WebSocket closed: ${event.reason || event.code}`, "ws_closed", 0, true);
```

**Step 6: Update existing api.test.ts error assertions**

The existing tests assert `toThrow('invalid request body')` and `toThrow('Internal Server Error')`. These still work because `ApiError.message` matches. But add new assertions to verify ApiError properties:

In the `'should handle HTTP error with JSON error body'` test, add after the existing assertion:

```typescript
try {
  await listAccounts();
} catch (err) {
  expect(err).toBeInstanceOf(ApiError);
  expect((err as ApiError).status).toBe(400);
  expect((err as ApiError).retryable).toBe(false);
}
```

In the `'should handle HTTP error with non-JSON body'` test, add:

```typescript
try {
  await listAccounts();
} catch (err) {
  expect(err).toBeInstanceOf(ApiError);
  expect((err as ApiError).status).toBe(500);
  expect((err as ApiError).retryable).toBe(true);
}
```

Add the import to api.test.ts:

```typescript
import { ApiError } from './errors';
```

**Step 7: Run all api tests**

Run: `cd frontend && npx vitest run src/lib/errors.test.ts src/lib/api.test.ts`
Expected: All PASS

**Step 8: Commit**

```bash
git add frontend/src/lib/errors.ts frontend/src/lib/errors.test.ts frontend/src/lib/api.ts frontend/src/lib/api.test.ts
git commit -m "feat(frontend): add ApiError structured error type

Replace generic Error throws in api.ts with ApiError that carries
code, status, and retryable fields. Enables smarter retry logic
and better error UIs without string parsing."
```

---

### Task 2: Design Tokens

**Files:**
- Modify: `frontend/src/index.css:1-3` (add @layer components block after tailwind imports)

**Step 1: Add component layer to index.css**

After the three `@tailwind` imports and before the `body` rule, add:

```css
@layer components {
  /* ── Status badges ──────────── */
  .badge-running      { @apply bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400; }
  .badge-stopped      { @apply bg-gray-100 text-gray-800 dark:bg-surface-elevated dark:text-gray-300; }
  .badge-provisioning { @apply bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400; }
  .badge-error        { @apply bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400; }

  /* ── Buttons ──────────── */
  .btn-primary   { @apply bg-brand-600 text-white hover:bg-brand-700 disabled:opacity-50 rounded-lg px-4 py-2 text-sm font-medium transition-colors; }
  .btn-secondary { @apply border border-gray-300 dark:border-border text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-surface-elevated rounded-lg px-4 py-2 text-sm font-medium transition-colors; }
  .btn-danger    { @apply bg-red-50 text-red-700 hover:bg-red-100 dark:bg-transparent dark:text-red-400 dark:border dark:border-red-900 dark:hover:bg-red-950 rounded-lg px-4 py-2 text-sm font-medium transition-colors; }
  .btn-ghost     { @apply text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200 rounded-lg px-3 py-2 text-sm transition-colors; }

  /* ── Surfaces ──────────── */
  .card { @apply bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border; }
}
```

**Step 2: Verify build still works**

Run: `cd frontend && npx vite build 2>&1 | tail -5`
Expected: Build succeeds with no errors

**Step 3: Migrate MachineCard status badges**

In `frontend/src/components/MachineCard.tsx`, change the `statusColors` map to use token classes:

```typescript
const statusColors: Record<string, string> = {
  running: "badge-running",
  stopped: "badge-stopped",
  provisioning: "badge-provisioning",
  error: "badge-error",
};
```

Also change the card root `<div>` to use the `.card` token:

```tsx
<div className="card p-4 transition-colors hover:bg-gray-50 dark:hover:bg-surface-card-hover">
```

**Step 4: Run existing MachineCard tests**

Run: `cd frontend && npx vitest run src/components/MachineCard.test.tsx`
Expected: All PASS (tests check behavior, not class names)

**Step 5: Commit**

```bash
git add frontend/src/index.css frontend/src/components/MachineCard.tsx
git commit -m "feat(frontend): add semantic design tokens

Add @layer components with badge, button, card, and text tokens.
Migrate MachineCard to use badge-* and card tokens. Other components
will migrate incrementally."
```

---

### Task 3: Operation Modules — Machine Lifecycle

**Files:**
- Create: `frontend/src/operations/machine.ts`
- Create: `frontend/src/operations/machine.test.ts`

**Step 1: Write failing tests for transitionMachine**

Create `frontend/src/operations/machine.test.ts`:

```typescript
import { describe, it, expect, vi, beforeEach } from "vitest";
import { transitionMachine } from "./machine";

// Mock the API module
vi.mock("../lib/api", () => ({
  startMachine: vi.fn(),
  stopMachine: vi.fn(),
  deleteMachine: vi.fn(),
}));

import { startMachine, stopMachine, deleteMachine } from "../lib/api";

describe("transitionMachine", () => {
  beforeEach(() => vi.clearAllMocks());

  it("starts a machine successfully", async () => {
    vi.mocked(startMachine).mockResolvedValue({ status: "provisioning", host_id: 1, vm_ip: "10.0.0.1" });

    const result = await transitionMachine(1, "m-1", "start");

    expect(result.success).toBe(true);
    expect(result.error).toBeUndefined();
    expect(startMachine).toHaveBeenCalledWith(1, "m-1");
  });

  it("stops a machine successfully", async () => {
    vi.mocked(stopMachine).mockResolvedValue({ status: "stopped" });

    const result = await transitionMachine(1, "m-1", "stop");

    expect(result.success).toBe(true);
    expect(stopMachine).toHaveBeenCalledWith(1, "m-1");
  });

  it("deletes a machine successfully", async () => {
    vi.mocked(deleteMachine).mockResolvedValue(undefined as never);

    const result = await transitionMachine(1, "m-1", "delete");

    expect(result.success).toBe(true);
    expect(deleteMachine).toHaveBeenCalledWith(1, "m-1");
  });

  it("returns error on failure", async () => {
    vi.mocked(startMachine).mockRejectedValue(new Error("host full"));

    const result = await transitionMachine(1, "m-1", "start");

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("host full");
  });
});
```

**Step 2: Run tests to verify they fail**

Run: `cd frontend && npx vitest run src/operations/machine.test.ts`
Expected: FAIL — module `./machine` not found

**Step 3: Write the implementation**

Create `frontend/src/operations/machine.ts`:

```typescript
import { startMachine, stopMachine, deleteMachine } from "../lib/api";

export interface MachineOperationResult {
  success: boolean;
  error?: Error;
}

export async function transitionMachine(
  accountId: number,
  machineId: string,
  action: "start" | "stop" | "delete",
): Promise<MachineOperationResult> {
  try {
    switch (action) {
      case "start":
        await startMachine(accountId, machineId);
        break;
      case "stop":
        await stopMachine(accountId, machineId);
        break;
      case "delete":
        await deleteMachine(accountId, machineId);
        break;
    }
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}
```

**Step 4: Run tests to verify they pass**

Run: `cd frontend && npx vitest run src/operations/machine.test.ts`
Expected: 4 PASS

**Step 5: Commit**

```bash
git add frontend/src/operations/machine.ts frontend/src/operations/machine.test.ts
git commit -m "feat(frontend): add machine lifecycle operation module

Extract start/stop/delete logic into pure async function. Testable
without React rendering. Components will consume via useOperation."
```

---

### Task 4: Operation Modules — Model Change

**Files:**
- Create: `frontend/src/operations/model.ts`
- Create: `frontend/src/operations/model.test.ts`

**Step 1: Write failing tests**

Create `frontend/src/operations/model.test.ts`:

```typescript
import { describe, it, expect, vi, beforeEach } from "vitest";
import { changeModel } from "./model";

vi.mock("../lib/api", () => ({
  setMachineModel: vi.fn(),
  pushMachineConfig: vi.fn(),
}));

import { setMachineModel, pushMachineConfig } from "../lib/api";

describe("changeModel", () => {
  beforeEach(() => vi.clearAllMocks());

  it("sets model and pushes config on success", async () => {
    vi.mocked(setMachineModel).mockResolvedValue({ status: "ok" });
    vi.mocked(pushMachineConfig).mockResolvedValue({ status: "ok" });

    const result = await changeModel(1, "m-1", "anthropic/claude-sonnet-4-6");

    expect(result.success).toBe(true);
    expect(setMachineModel).toHaveBeenCalledWith(1, "m-1", "anthropic/claude-sonnet-4-6");
    expect(pushMachineConfig).toHaveBeenCalledWith(1, "m-1");
  });

  it("returns error if setMachineModel fails", async () => {
    vi.mocked(setMachineModel).mockRejectedValue(new Error("invalid model"));

    const result = await changeModel(1, "m-1", "bad/model");

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("invalid model");
    expect(pushMachineConfig).not.toHaveBeenCalled();
  });

  it("returns error if pushMachineConfig fails", async () => {
    vi.mocked(setMachineModel).mockResolvedValue({ status: "ok" });
    vi.mocked(pushMachineConfig).mockRejectedValue(new Error("push failed"));

    const result = await changeModel(1, "m-1", "anthropic/claude-sonnet-4-6");

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("push failed");
  });
});
```

**Step 2: Run tests — should fail**

Run: `cd frontend && npx vitest run src/operations/model.test.ts`
Expected: FAIL — module `./model` not found

**Step 3: Write implementation**

Create `frontend/src/operations/model.ts`:

```typescript
import { setMachineModel, pushMachineConfig } from "../lib/api";
import type { MachineOperationResult } from "./machine";

export async function changeModel(
  accountId: number,
  machineId: string,
  model: string,
): Promise<MachineOperationResult> {
  try {
    await setMachineModel(accountId, machineId, model);
    await pushMachineConfig(accountId, machineId);
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}
```

**Step 4: Run tests — should pass**

Run: `cd frontend && npx vitest run src/operations/model.test.ts`
Expected: 3 PASS

**Step 5: Commit**

```bash
git add frontend/src/operations/model.ts frontend/src/operations/model.test.ts
git commit -m "feat(frontend): add model change operation module

Two-step model change (set + push config) as a single testable
operation. Surfaces errors from either step."
```

---

### Task 5: Operation Modules — Credentials

**Files:**
- Create: `frontend/src/operations/credentials.ts`
- Create: `frontend/src/operations/credentials.test.ts`

**Step 1: Write failing tests**

Create `frontend/src/operations/credentials.test.ts`:

```typescript
import { describe, it, expect, vi, beforeEach } from "vitest";
import { attachCredential, detachCredential, swapCredential } from "./credentials";

vi.mock("../lib/api", () => ({
  linkMachineCredential: vi.fn(),
  unlinkMachineCredential: vi.fn(),
  pushMachineConfig: vi.fn(),
}));

import { linkMachineCredential, unlinkMachineCredential, pushMachineConfig } from "../lib/api";

describe("credential operations", () => {
  beforeEach(() => vi.clearAllMocks());

  describe("attachCredential", () => {
    it("links and pushes config on success", async () => {
      vi.mocked(linkMachineCredential).mockResolvedValue({ status: "ok" });
      vi.mocked(pushMachineConfig).mockResolvedValue({ status: "ok" });

      const result = await attachCredential(1, "m-1", 42);

      expect(result.success).toBe(true);
      expect(linkMachineCredential).toHaveBeenCalledWith(1, "m-1", 42);
      expect(pushMachineConfig).toHaveBeenCalledWith(1, "m-1");
    });

    it("returns error if link fails", async () => {
      vi.mocked(linkMachineCredential).mockRejectedValue(new Error("already linked"));

      const result = await attachCredential(1, "m-1", 42);

      expect(result.success).toBe(false);
      expect(result.error?.message).toBe("already linked");
    });
  });

  describe("detachCredential", () => {
    it("unlinks credential", async () => {
      vi.mocked(unlinkMachineCredential).mockResolvedValue(undefined as never);

      const result = await detachCredential(1, "m-1", 42);

      expect(result.success).toBe(true);
      expect(unlinkMachineCredential).toHaveBeenCalledWith(1, "m-1", 42);
    });
  });

  describe("swapCredential", () => {
    it("unlinks old, links new, pushes config", async () => {
      vi.mocked(unlinkMachineCredential).mockResolvedValue(undefined as never);
      vi.mocked(linkMachineCredential).mockResolvedValue({ status: "ok" });
      vi.mocked(pushMachineConfig).mockResolvedValue({ status: "ok" });

      const result = await swapCredential(1, "m-1", 10, 20);

      expect(result.success).toBe(true);
      expect(unlinkMachineCredential).toHaveBeenCalledWith(1, "m-1", 10);
      expect(linkMachineCredential).toHaveBeenCalledWith(1, "m-1", 20);
      expect(pushMachineConfig).toHaveBeenCalledWith(1, "m-1");
    });

    it("returns error if unlink fails", async () => {
      vi.mocked(unlinkMachineCredential).mockRejectedValue(new Error("not found"));

      const result = await swapCredential(1, "m-1", 10, 20);

      expect(result.success).toBe(false);
      expect(linkMachineCredential).not.toHaveBeenCalled();
    });
  });
});
```

**Step 2: Run tests — should fail**

Run: `cd frontend && npx vitest run src/operations/credentials.test.ts`
Expected: FAIL — module not found

**Step 3: Write implementation**

Create `frontend/src/operations/credentials.ts`:

```typescript
import { linkMachineCredential, unlinkMachineCredential, pushMachineConfig } from "../lib/api";
import type { MachineOperationResult } from "./machine";

export async function attachCredential(
  accountId: number,
  machineId: string,
  credentialId: number,
): Promise<MachineOperationResult> {
  try {
    await linkMachineCredential(accountId, machineId, credentialId);
    await pushMachineConfig(accountId, machineId);
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}

export async function detachCredential(
  accountId: number,
  machineId: string,
  credentialId: number,
): Promise<MachineOperationResult> {
  try {
    await unlinkMachineCredential(accountId, machineId, credentialId);
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}

export async function swapCredential(
  accountId: number,
  machineId: string,
  oldCredentialId: number,
  newCredentialId: number,
): Promise<MachineOperationResult> {
  try {
    await unlinkMachineCredential(accountId, machineId, oldCredentialId);
    await linkMachineCredential(accountId, machineId, newCredentialId);
    await pushMachineConfig(accountId, machineId);
    return { success: true };
  } catch (err) {
    return {
      success: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}
```

**Step 4: Run tests — should pass**

Run: `cd frontend && npx vitest run src/operations/credentials.test.ts`
Expected: 5 PASS

**Step 5: Commit**

```bash
git add frontend/src/operations/credentials.ts frontend/src/operations/credentials.test.ts
git commit -m "feat(frontend): add credential operation modules

Extract attach/detach/swap credential flows into pure async functions.
Consolidates logic from MachineCredentials, ChannelSetup, and
CredentialSelector."
```

---

### Task 6: Mutation Queue — useOperation Hook

**Files:**
- Create: `frontend/src/lib/useOperation.ts`
- Create: `frontend/src/lib/useOperation.test.ts`

**Step 1: Write failing tests**

Create `frontend/src/lib/useOperation.test.ts`:

```typescript
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useOperation } from "./useOperation";

// Mock Toast
vi.mock("../components/Toast", () => ({
  useToast: () => ({ toast: vi.fn() }),
}));

describe("useOperation", () => {
  beforeEach(() => vi.clearAllMocks());

  it("starts with loading=false and error=null", () => {
    const { result } = renderHook(() => useOperation());
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it("sets loading=true during execution", async () => {
    let resolve: (v: string) => void;
    const promise = new Promise<string>((r) => { resolve = r; });

    const { result } = renderHook(() => useOperation<string>());

    let executePromise: Promise<string | undefined>;
    act(() => {
      executePromise = result.current.execute(() => promise);
    });

    expect(result.current.loading).toBe(true);

    await act(async () => {
      resolve!("done");
      await executePromise!;
    });

    expect(result.current.loading).toBe(false);
  });

  it("returns the result on success", async () => {
    const { result } = renderHook(() => useOperation<string>());

    let value: string | undefined;
    await act(async () => {
      value = await result.current.execute(() => Promise.resolve("hello"));
    });

    expect(value).toBe("hello");
    expect(result.current.error).toBeNull();
  });

  it("sets error on failure", async () => {
    const { result } = renderHook(() => useOperation<string>());

    await act(async () => {
      await result.current.execute(() => Promise.reject(new Error("boom")));
    });

    expect(result.current.error?.message).toBe("boom");
    expect(result.current.loading).toBe(false);
  });

  it("prevents concurrent execution", async () => {
    const calls: number[] = [];
    let resolve1: () => void;
    const slow = new Promise<void>((r) => { resolve1 = r; });

    const { result } = renderHook(() => useOperation<void>());

    let p1: Promise<void | undefined>;
    act(() => {
      p1 = result.current.execute(async () => { calls.push(1); await slow; });
    });

    // Second call while first is running should be no-op
    let p2: Promise<void | undefined>;
    act(() => {
      p2 = result.current.execute(async () => { calls.push(2); });
    });

    await act(async () => {
      resolve1!();
      await p1!;
      await p2!;
    });

    expect(calls).toEqual([1]); // Second call never ran
  });

  it("clears error on next successful execution", async () => {
    const { result } = renderHook(() => useOperation<string>());

    await act(async () => {
      await result.current.execute(() => Promise.reject(new Error("fail")));
    });
    expect(result.current.error).not.toBeNull();

    await act(async () => {
      await result.current.execute(() => Promise.resolve("ok"));
    });
    expect(result.current.error).toBeNull();
  });
});
```

**Step 2: Run tests — should fail**

Run: `cd frontend && npx vitest run src/lib/useOperation.test.ts`
Expected: FAIL — module not found

**Step 3: Write implementation**

Create `frontend/src/lib/useOperation.ts`:

```typescript
import { useCallback, useRef, useState } from "react";

export interface UseOperationOptions {
  /** If set, auto-toasts on error with this title. */
  errorTitle?: string;
  /** If set, auto-toasts on success with this title. */
  successTitle?: string;
}

export interface UseOperationReturn<T> {
  /** Run an async operation. No-op if already running. Returns result or undefined if skipped/errored. */
  execute: (fn: () => Promise<T>) => Promise<T | undefined>;
  /** True while an operation is in flight. */
  loading: boolean;
  /** The error from the last failed execution, or null. Cleared on next success. */
  error: Error | null;
}

export function useOperation<T>(options?: UseOperationOptions): UseOperationReturn<T> {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const runningRef = useRef(false);
  const optionsRef = useRef(options);
  optionsRef.current = options;

  const execute = useCallback(async (fn: () => Promise<T>): Promise<T | undefined> => {
    if (runningRef.current) return undefined;
    runningRef.current = true;
    setLoading(true);
    setError(null);
    try {
      const result = await fn();
      setLoading(false);
      runningRef.current = false;
      return result;
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      setError(error);
      setLoading(false);
      runningRef.current = false;
      return undefined;
    }
  }, []);

  return { execute, loading, error };
}
```

Note: Toast integration is intentionally omitted from the hook itself. Components already have `useToast()` and can toast based on the result. This keeps the hook dependency-free and simpler to test. If toast auto-firing is wanted later, it can be added as a wrapper hook.

**Step 4: Run tests — should pass**

Run: `cd frontend && npx vitest run src/lib/useOperation.test.ts`
Expected: 6 PASS

**Step 5: Commit**

```bash
git add frontend/src/lib/useOperation.ts frontend/src/lib/useOperation.test.ts
git commit -m "feat(frontend): add useOperation mutation queue hook

Provides loading state, error tracking, and concurrent execution
guard. Replaces ad-hoc acting/saving/toggling flags in components."
```

---

### Task 7: Refactor MachineCard to Use Operations + useOperation

**Files:**
- Modify: `frontend/src/components/MachineCard.tsx`

**Step 1: Refactor MachineCard**

Replace the inline lifecycle handlers and model change logic with operation modules + useOperation. The full refactored component imports:

```typescript
import { useState, useEffect } from "react";
import { Link } from "react-router-dom";
import { getMachineModel } from "../lib/api";
import { PROVIDER_MODELS } from "../lib/types";
import { CopyButton } from "./CopyButton";
import { useToast } from "./Toast";
import { useOperation } from "../lib/useOperation";
import { transitionMachine } from "../operations/machine";
import { changeModel } from "../operations/model";
import type { Machine } from "../lib/types";
```

Replace the state variables:
- Remove: `const [acting, setActing] = useState(false);`
- Remove: `const [modelSaving, setModelSaving] = useState(false);`
- Add: `const lifecycle = useOperation();`
- Add: `const modelOp = useOperation();`

Replace `handleModelChange`:
```typescript
const handleModelChange = async (model: string) => {
  if (!accountId || !model) return;
  const result = await modelOp.execute(() => changeModel(accountId, machine.id, model));
  if (result?.success) {
    setCurrentModel(model);
    toast({ title: "Model updated", description: "Config pushed to machine." });
  } else if (result && !result.success) {
    toast({ title: "Failed to update model", description: result.error?.message || "Unknown error", variant: "error" });
  }
};
```

Replace `handleStart`, `handleStop`, `handleDelete`:
```typescript
const handleStart = async (e: React.MouseEvent) => {
  e.preventDefault();
  e.stopPropagation();
  if (!accountId) return;
  const result = await lifecycle.execute(() => transitionMachine(accountId, machine.id, "start"));
  if (result?.success) onStatusChange?.();
  else if (result && !result.success) toast({ title: "Failed to start machine", description: result.error?.message || "Unknown error", variant: "error" });
};

const handleStop = async (e: React.MouseEvent) => {
  e.preventDefault();
  e.stopPropagation();
  if (!accountId) return;
  const result = await lifecycle.execute(() => transitionMachine(accountId, machine.id, "stop"));
  if (result?.success) onStatusChange?.();
  else if (result && !result.success) toast({ title: "Failed to stop machine", description: result.error?.message || "Unknown error", variant: "error" });
};

const handleDelete = async (e: React.MouseEvent) => {
  e.preventDefault();
  e.stopPropagation();
  if (!accountId) return;
  if (!confirmDelete) { setConfirmDelete(true); return; }
  const result = await lifecycle.execute(() => transitionMachine(accountId, machine.id, "delete"));
  if (result?.success) onStatusChange?.();
  else if (result && !result.success) toast({ title: "Failed to delete machine", description: result.error?.message || "Unknown error", variant: "error" });
  setConfirmDelete(false);
};
```

In JSX, replace `disabled={!canStart || acting}` with `disabled={!canStart || lifecycle.loading}`:
- Start button: `disabled={!canStart || lifecycle.loading}`
- Stop button: `disabled={!canStop || lifecycle.loading}`
- Delete button: `disabled={!canDelete || lifecycle.loading}`
- Model select: `disabled={modelOp.loading}`
- Acting text: Replace `acting && canStart` with `lifecycle.loading && canStart`, etc.
- Model saving text: Replace `modelSaving` with `modelOp.loading`

**Step 2: Run MachineCard tests**

Run: `cd frontend && npx vitest run src/components/MachineCard.test.tsx`
Expected: All PASS

**Step 3: Commit**

```bash
git add frontend/src/components/MachineCard.tsx
git commit -m "refactor(frontend): MachineCard uses operation modules + useOperation

Replace inline lifecycle/model handlers with transitionMachine,
changeModel, and useOperation hook. Removes acting/modelSaving
state in favor of hook-managed loading and concurrent guards."
```

---

### Task 8: WebSocket Policy/Executor Split

**Files:**
- Create: `frontend/src/lib/ws/protocol.ts`
- Create: `frontend/src/lib/ws/protocol.test.ts`
- Create: `frontend/src/lib/ws/terminalPolicy.ts`
- Create: `frontend/src/lib/ws/terminalPolicy.test.ts`
- Modify: `frontend/src/components/Terminal.tsx:67-97` (handleMessage)

**Step 1: Write tests for parseFrame**

Create `frontend/src/lib/ws/protocol.test.ts`:

```typescript
import { describe, it, expect } from "vitest";
import { parseFrame } from "./protocol";

describe("parseFrame", () => {
  it("parses string frames into type + payload", () => {
    const event = new MessageEvent("message", { data: "shello-session" });
    const frame = parseFrame(event);
    expect(frame.type).toBe("s");
    expect(frame.payload).toBe("hello-session");
  });

  it("parses single-char frames as type with empty payload", () => {
    const event = new MessageEvent("message", { data: "s" });
    const frame = parseFrame(event);
    expect(frame.type).toBe("s");
    expect(frame.payload).toBe("");
  });

  it("returns ignore type for empty string", () => {
    const event = new MessageEvent("message", { data: "" });
    const frame = parseFrame(event);
    expect(frame.type).toBe("");
    expect(frame.payload).toBe("");
  });

  it("returns binary type for ArrayBuffer data", () => {
    const buf = new ArrayBuffer(4);
    const event = new MessageEvent("message", { data: buf });
    const frame = parseFrame(event);
    expect(frame.type).toBe("binary");
    expect(frame.payload).toBe(buf);
  });
});
```

**Step 2: Write parseFrame implementation**

Create `frontend/src/lib/ws/protocol.ts`:

```typescript
export interface WsFrame {
  type: string;
  payload: string | ArrayBuffer;
  raw: MessageEvent;
}

export function parseFrame(event: MessageEvent): WsFrame {
  if (typeof event.data === "string") {
    if (event.data.length > 0) {
      return { type: event.data[0], payload: event.data.slice(1), raw: event };
    }
    return { type: "", payload: "", raw: event };
  }
  return { type: "binary", payload: event.data, raw: event };
}
```

**Step 3: Run protocol tests**

Run: `cd frontend && npx vitest run src/lib/ws/protocol.test.ts`
Expected: 4 PASS

**Step 4: Write tests for resolveTerminalAction**

Create `frontend/src/lib/ws/terminalPolicy.test.ts`:

```typescript
import { describe, it, expect } from "vitest";
import { resolveTerminalAction } from "./terminalPolicy";
import type { WsFrame } from "./protocol";

const frame = (type: string, payload: string): WsFrame => ({
  type,
  payload,
  raw: new MessageEvent("message", { data: type + payload }),
});

describe("resolveTerminalAction", () => {
  it("returns set_session for 's' frames", () => {
    const action = resolveTerminalAction(frame("s", "abc-123"), false);
    expect(action).toEqual({ kind: "set_session", sessionId: "abc-123" });
  });

  it("returns write with isReplay=true for 'r' frames when no content yet", () => {
    const action = resolveTerminalAction(frame("r", "replay data"), false);
    expect(action).toEqual({ kind: "write", data: "replay data", isReplay: true });
  });

  it("returns ignore for 'r' frames when content already exists", () => {
    const action = resolveTerminalAction(frame("r", "replay data"), true);
    expect(action).toEqual({ kind: "ignore" });
  });

  it("returns write with isReplay=false for '0' frames", () => {
    const action = resolveTerminalAction(frame("0", "hello"), false);
    expect(action).toEqual({ kind: "write", data: "hello", isReplay: false });
  });

  it("returns ignore for unknown frame types", () => {
    const action = resolveTerminalAction(frame("x", "whatever"), false);
    expect(action).toEqual({ kind: "ignore" });
  });
});
```

**Step 5: Write terminalPolicy implementation**

Create `frontend/src/lib/ws/terminalPolicy.ts`:

```typescript
import type { WsFrame } from "./protocol";

export type TerminalAction =
  | { kind: "set_session"; sessionId: string }
  | { kind: "write"; data: string; isReplay: boolean }
  | { kind: "ignore" };

export function resolveTerminalAction(
  frame: WsFrame,
  hasContent: boolean,
): TerminalAction {
  switch (frame.type) {
    case "s":
      return { kind: "set_session", sessionId: frame.payload as string };
    case "r":
      return hasContent
        ? { kind: "ignore" }
        : { kind: "write", data: frame.payload as string, isReplay: true };
    case "0":
      return { kind: "write", data: frame.payload as string, isReplay: false };
    default:
      return { kind: "ignore" };
  }
}
```

**Step 6: Run policy tests**

Run: `cd frontend && npx vitest run src/lib/ws/terminalPolicy.test.ts`
Expected: 5 PASS

**Step 7: Refactor Terminal.tsx handleMessage**

In `frontend/src/components/Terminal.tsx`, add imports:

```typescript
import { parseFrame } from "../lib/ws/protocol";
import { resolveTerminalAction } from "../lib/ws/terminalPolicy";
```

Replace the `handleMessage` callback (lines 67-97) with:

```typescript
const handleMessage = useCallback((event: MessageEvent) => {
  const terminal = terminalRef.current;
  if (!terminal) return;

  const frame = parseFrame(event);

  // Handle binary frames (ArrayBuffer) directly
  if (frame.type === "binary" && frame.payload instanceof ArrayBuffer) {
    const text = new TextDecoder().decode(frame.payload);
    if (text.length > 0 && text[0] === "0") {
      terminal.write(text.slice(1));
    }
    return;
  }

  const action = resolveTerminalAction(frame, hasContentRef.current);
  switch (action.kind) {
    case "set_session":
      sessionIdRef.current = action.sessionId;
      sessionStorage.setItem(sessionKey, action.sessionId);
      break;
    case "write":
      terminal.write(action.data);
      hasContentRef.current = true;
      break;
  }
}, [sessionKey]);
```

**Step 8: Run all WS + existing tests**

Run: `cd frontend && npx vitest run src/lib/ws/ src/lib/useReconnectingWebSocket.test.ts`
Expected: All PASS

**Step 9: Commit**

```bash
git add frontend/src/lib/ws/protocol.ts frontend/src/lib/ws/protocol.test.ts \
  frontend/src/lib/ws/terminalPolicy.ts frontend/src/lib/ws/terminalPolicy.test.ts \
  frontend/src/components/Terminal.tsx
git commit -m "refactor(frontend): WebSocket policy/executor split for Terminal

Extract frame parsing (protocol.ts) and decision logic (terminalPolicy.ts)
from Terminal.tsx. Both are pure functions testable without xterm or DOM.
Terminal becomes a thin executor."
```

---

### Task 9: Run Full Test Suite + Typecheck

**Files:** None (verification only)

**Step 1: Run full test suite**

Run: `cd frontend && npx vitest run`
Expected: All tests PASS

**Step 2: Run typecheck**

Run: `cd frontend && npx tsc --noEmit`
Expected: No type errors

**Step 3: Run build**

Run: `cd frontend && npx vite build 2>&1 | tail -5`
Expected: Build succeeds

**Step 4: Final commit if any fixes needed**

If any test/type/build issues were found and fixed, commit with:

```bash
git commit -m "fix(frontend): resolve test/type issues from architecture refactor"
```
