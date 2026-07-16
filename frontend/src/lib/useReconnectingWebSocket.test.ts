import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useReconnectingWebSocket } from "./useReconnectingWebSocket";

// Mock WebSocket
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  url: string;
  binaryType: BinaryType = "blob";
  readyState: number = WebSocket.CONNECTING;
  onopen: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = WebSocket.CLOSED;
  });

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  simulateOpen() {
    this.readyState = WebSocket.OPEN;
    this.onopen?.(new Event("open"));
  }

  simulateClose(code: number, reason = "") {
    this.readyState = WebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code, reason }));
  }

  simulateMessage(data: string) {
    this.onmessage?.(new MessageEvent("message", { data }));
  }

  simulateError() {
    this.onerror?.(new Event("error"));
  }
}

describe("useReconnectingWebSocket", () => {
  beforeEach(() => {
    MockWebSocket.instances = [];
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", MockWebSocket);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // The mount effect schedules the first connect via setTimeout(0) so that
  // React StrictMode's throwaway first mount is cancelled before a socket
  // opens. Flush that timer so tests see the socket created on mount.
  function mountRWS(
    options: Parameters<typeof useReconnectingWebSocket>[0],
  ) {
    const rendered = renderHook(() => useReconnectingWebSocket(options));
    act(() => {
      vi.advanceTimersByTime(0);
    });
    return rendered;
  }

  it("connects on mount", () => {
    const onMessage = vi.fn();
    mountRWS({ url: "ws://localhost/test", onMessage });

    expect(MockWebSocket.instances).toHaveLength(1);
    expect(MockWebSocket.instances[0].url).toBe("ws://localhost/test");
  });

  it("cancels the initial connect if unmounted before it fires (StrictMode-safe)", () => {
    const onMessage = vi.fn();
    // NOTE: use renderHook directly (not mountRWS) so the deferred connect
    // timer has NOT fired yet when we unmount.
    const { unmount } = renderHook(() =>
      useReconnectingWebSocket({ url: "ws://localhost/test", onMessage }),
    );
    // Unmount before the deferred connect timer fires — the throwaway mount
    // must not open a socket (this is what prevents the StrictMode dual-socket
    // reconnect war).
    unmount();
    act(() => {
      vi.advanceTimersByTime(0);
    });

    expect(MockWebSocket.instances).toHaveLength(0);
  });

  it("sets status to connected on open", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({ url: "ws://localhost/test", onMessage });

    act(() => MockWebSocket.instances[0].simulateOpen());

    expect(result.current.status).toBe("connected");
  });

  it("does not reconnect on normal close (1000)", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({ url: "ws://localhost/test", onMessage });

    act(() => MockWebSocket.instances[0].simulateOpen());
    act(() => MockWebSocket.instances[0].simulateClose(1000));

    expect(result.current.status).toBe("disconnected");
    // No new WebSocket created for reconnect
    expect(MockWebSocket.instances).toHaveLength(1);
  });

  it("does not reconnect on going away close (1001)", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({ url: "ws://localhost/test", onMessage });

    act(() => MockWebSocket.instances[0].simulateOpen());
    act(() => MockWebSocket.instances[0].simulateClose(1001));

    expect(result.current.status).toBe("disconnected");
    expect(MockWebSocket.instances).toHaveLength(1);
  });

  it("reconnects with exponential backoff on abnormal close", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({
        url: "ws://localhost/test",
        onMessage,
        initialDelay: 1000,
        maxRetries: 3,
      });

    act(() => MockWebSocket.instances[0].simulateOpen());
    // Abnormal close (code 1006)
    act(() => MockWebSocket.instances[0].simulateClose(1006));

    expect(result.current.status).toBe("connecting");

    // First retry after 1000ms (1000 * 2^0)
    act(() => vi.advanceTimersByTime(1000));
    expect(MockWebSocket.instances).toHaveLength(2);

    // Second abnormal close
    act(() => MockWebSocket.instances[1].simulateClose(1006));

    // Second retry after 2000ms (1000 * 2^1)
    act(() => vi.advanceTimersByTime(2000));
    expect(MockWebSocket.instances).toHaveLength(3);
  });

  it("stops reconnecting after maxRetries and sets error status", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({
        url: "ws://localhost/test",
        onMessage,
        maxRetries: 2,
        initialDelay: 100,
      });

    act(() => MockWebSocket.instances[0].simulateOpen());

    // First abnormal close + retry
    act(() => MockWebSocket.instances[0].simulateClose(1006));
    act(() => vi.advanceTimersByTime(100));

    // Second abnormal close + retry
    act(() => MockWebSocket.instances[1].simulateClose(1006));
    act(() => vi.advanceTimersByTime(200));

    // Third abnormal close — no more retries
    act(() => MockWebSocket.instances[2].simulateClose(1006));

    expect(result.current.status).toBe("error");
    // No more instances should be created
    const countBefore = MockWebSocket.instances.length;
    act(() => vi.advanceTimersByTime(10000));
    expect(MockWebSocket.instances).toHaveLength(countBefore);
  });

  it("manual reconnect resets retry counter", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({
        url: "ws://localhost/test",
        onMessage,
        maxRetries: 1,
        initialDelay: 100,
      });

    act(() => MockWebSocket.instances[0].simulateOpen());

    // Exhaust retries
    act(() => MockWebSocket.instances[0].simulateClose(1006));
    act(() => vi.advanceTimersByTime(100));
    act(() => MockWebSocket.instances[1].simulateClose(1006));

    expect(result.current.status).toBe("error");

    // Manual reconnect should reset
    act(() => result.current.reconnect());
    const latestWs = MockWebSocket.instances[MockWebSocket.instances.length - 1];
    act(() => latestWs.simulateOpen());

    expect(result.current.status).toBe("connected");
  });

  it("forwards messages to onMessage callback", () => {
    const onMessage = vi.fn();
    mountRWS({ url: "ws://localhost/test", onMessage });

    act(() => MockWebSocket.instances[0].simulateOpen());
    act(() => MockWebSocket.instances[0].simulateMessage("hello"));

    expect(onMessage).toHaveBeenCalledTimes(1);
    expect(onMessage.mock.calls[0][0].data).toBe("hello");
  });

  it("send works when connected", () => {
    const onMessage = vi.fn();
    const { result } = mountRWS({ url: "ws://localhost/test", onMessage });

    act(() => MockWebSocket.instances[0].simulateOpen());
    act(() => result.current.send("test data"));

    expect(MockWebSocket.instances[0].send).toHaveBeenCalledWith("test data");
  });
});
