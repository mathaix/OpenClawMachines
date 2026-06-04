# Studio Architecture Patterns for OCM Frontend

**Date:** 2026-03-03
**Status:** Approved
**Approach:** Bottom-up (foundation layers first, incremental component refactoring)

## Context

Review of [openclaw-studio](~/openclaw-studio) identified 5 architectural patterns worth adopting in the OCM frontend. Studio extracts business logic into testable pure functions, uses structured error types, prevents concurrent mutations with guards, and defines semantic design tokens. OCM currently has business logic embedded in React component callbacks, string-only errors, ad-hoc `acting` flags for double-click protection, and 150+ repeated Tailwind class combinations.

This design covers architecture only — no new product features.

## Layer 1: Structured Error Types

**New file:** `frontend/src/lib/errors.ts`

```typescript
export class ApiError extends Error {
  constructor(
    message: string,
    public readonly code: string,
    public readonly status: number,
    public readonly retryable: boolean = false,
  ) {
    super(message);
    this.name = "ApiError";
  }
}
```

**Changes to `api.ts`:**
- `request()` throws `ApiError` instead of `new Error(string)`
- `ApiError.retryable` is `true` for 429 and 5xx status codes
- `ApiError.code` comes from response body `code` field (falls back to `"unknown"`)
- `gatewayRPC()` throws `ApiError` with code `"rpc_error"` or `"rpc_timeout"`

Backwards compatible: `ApiError extends Error`, so existing `err instanceof Error` checks still work.

## Layer 2: Semantic Design Tokens

**Addition to `frontend/src/index.css`** inside `@layer components`:

Status badges:
- `.badge-running` `.badge-stopped` `.badge-provisioning` `.badge-error`

Buttons:
- `.btn-primary` `.btn-secondary` `.btn-danger` `.btn-ghost`

Surfaces:
- `.card` (white/dark card with border)

Text hierarchy:
- `.text-primary` `.text-secondary` `.text-tertiary`

Additive — existing inline classes continue working. Components migrate incrementally as they're touched.

## Layer 3: Operation Modules

**New directory:** `frontend/src/operations/`

### `operations/machine.ts`
Extracts machine lifecycle (start/stop/delete) from MachineCard, MachineView, MachineWorkspace.

```typescript
export async function transitionMachine(
  accountId: number, machineId: string, action: "start" | "stop" | "delete"
): Promise<MachineOperationResult>
```

### `operations/model.ts`
Extracts two-step model change (setMachineModel + pushMachineConfig) from MachineCard, MachineView.

```typescript
export async function changeModel(
  accountId: number, machineId: string, model: string
): Promise<MachineOperationResult>
```

### `operations/credentials.ts`
Extracts credential linking from MachineCredentials, ChannelSetup, CredentialSelector.

```typescript
export async function attachCredential(accountId, machineId, credentialId): Promise<MachineOperationResult>
export async function detachCredential(accountId, machineId, credentialId): Promise<MachineOperationResult>
export async function swapCredential(accountId, machineId, oldId, newId): Promise<MachineOperationResult>
```

All return `{ success: boolean; error?: Error }`. Pure async functions, testable without React.

## Layer 4: Mutation Queue with Guards

**New file:** `frontend/src/lib/useOperation.ts`

```typescript
export function useOperation<T>(options?: UseOperationOptions): UseOperationReturn<T>
```

Behaviors:
1. **Concurrent guard** — second call while first is running is a no-op
2. **Automatic loading state** — replaces `acting`/`saving`/`toggling` flags
3. **Error surfacing** — auto-toast on failure if `errorTitle` set, stores `error` for inline display
4. **Retryable awareness** — checks `ApiError.retryable` for retry UI hints

One `useOperation` per logical mutex (e.g., lifecycle actions share one lock, model change gets its own).

## Layer 5: WebSocket Policy/Executor Split

**New files:**
- `frontend/src/lib/ws/protocol.ts` — Frame parsing (bridge)
- `frontend/src/lib/ws/terminalPolicy.ts` — Decision logic (policy)

### protocol.ts
```typescript
export interface WsFrame { type: string; payload: string | ArrayBuffer; raw: MessageEvent; }
export function parseFrame(event: MessageEvent): WsFrame
```

### terminalPolicy.ts
```typescript
export type TerminalAction =
  | { kind: "set_session"; sessionId: string }
  | { kind: "write"; data: string; isReplay: boolean }
  | { kind: "ignore" };

export function resolveTerminalAction(frame: WsFrame, hasContent: boolean): TerminalAction
```

Terminal.tsx becomes the executor: calls parseFrame → resolveTerminalAction → applies the action. The policy function is pure and testable without xterm.js or DOM.

`useReconnectingWebSocket` itself is unchanged — the split happens in message handlers.

## File Summary

| Layer | New Files | Changes |
|-------|-----------|---------|
| 1. Errors | `lib/errors.ts` | `api.ts` request/gatewayRPC |
| 2. Tokens | `index.css` addition | Components (incremental) |
| 3. Operations | `operations/machine.ts`, `operations/model.ts`, `operations/credentials.ts` | MachineCard, MachineView, MachineWorkspace, MachineCredentials, ChannelSetup |
| 4. Mutation queue | `lib/useOperation.ts` | All components with acting/saving flags |
| 5. WS split | `lib/ws/protocol.ts`, `lib/ws/terminalPolicy.ts` | Terminal.tsx |

## Implementation Order

1. Structured errors (smallest, unblocks everything)
2. Design tokens (independent, low risk)
3. Operation modules (highest value, uses errors)
4. Mutation queue hook (uses errors, wraps operations)
5. WS policy split (independent, prepares for future)

Each layer ships with unit tests. Components refactored incrementally to use new layers.
