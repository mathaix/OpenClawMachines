import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useOperation } from "./useOperation";

// Mock Toast — useOperation doesn't use it yet but we mock to prevent import errors
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
    const promise = new Promise<string>((r) => {
      resolve = r;
    });

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
    const slow = new Promise<void>((r) => {
      resolve1 = r;
    });

    const { result } = renderHook(() => useOperation<void>());

    let p1: Promise<void | undefined>;
    act(() => {
      p1 = result.current.execute(async () => {
        calls.push(1);
        await slow;
      });
    });

    // Second call while first is running should be no-op
    let p2: Promise<void | undefined>;
    act(() => {
      p2 = result.current.execute(async () => {
        calls.push(2);
      });
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
