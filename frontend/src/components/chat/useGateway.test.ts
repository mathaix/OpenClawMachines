import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useGateway, GatewayMessage } from "./useGateway";

// Mock WebSocket
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  onopen: (() => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  readyState = 0; // CONNECTING
  sent: string[] = [];

  constructor(public url: string) {
    MockWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.readyState = 3; // CLOSED
    this.onclose?.({ code: 1000 });
  }

  // Test helpers
  simulateOpen() {
    this.readyState = 1; // OPEN
    this.onopen?.();
  }

  simulateMessage(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) });
  }
}

beforeEach(() => {
  MockWebSocket.instances = [];
  vi.stubGlobal("WebSocket", MockWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useGateway", () => {
  it("connects and authenticates with gateway protocol v3", async () => {
    const { result } = renderHook(() =>
      useGateway({
        wsUrl: "wss://example.com/ws",
        token: "test-token",
      })
    );

    expect(result.current.status).toBe("connecting");

    const ws = MockWebSocket.instances[0];
    expect(ws).toBeDefined();
    expect(ws.url).toBe("wss://example.com/ws");

    // Simulate WebSocket open
    act(() => ws.simulateOpen());

    // Should have sent connect request
    expect(ws.sent).toHaveLength(1);
    const connectReq = JSON.parse(ws.sent[0]);
    expect(connectReq.type).toBe("req");
    expect(connectReq.method).toBe("connect");
    expect(connectReq.params.auth.token).toBe("test-token");
    expect(connectReq.params.minProtocol).toBe(3);
    expect(connectReq.params.maxProtocol).toBe(3);
    expect(connectReq.params.client.id).toBe("ocm-chat");

    // Simulate hello-ok response
    act(() =>
      ws.simulateMessage({
        type: "res",
        id: connectReq.id,
        ok: true,
        payload: { type: "hello-ok", sessions: [], presence: {}, health: {} },
      })
    );

    expect(result.current.status).toBe("connected");
  });

  it("sends chat.send request", async () => {
    const { result } = renderHook(() =>
      useGateway({ wsUrl: "wss://example.com/ws", token: "tok" })
    );

    const ws = MockWebSocket.instances[0];
    act(() => ws.simulateOpen());

    const connectReq = JSON.parse(ws.sent[0]);
    act(() =>
      ws.simulateMessage({
        type: "res",
        id: connectReq.id,
        ok: true,
        payload: { type: "hello-ok", sessions: [], presence: {}, health: {} },
      })
    );

    act(() => result.current.sendMessage("Hello world"));

    expect(ws.sent).toHaveLength(2);
    const chatReq = JSON.parse(ws.sent[1]);
    expect(chatReq.type).toBe("req");
    expect(chatReq.method).toBe("chat.send");
    expect(chatReq.params.content).toBe("Hello world");
  });

  it("receives streaming messages via chat events", async () => {
    const onMessage = vi.fn();
    const { result } = renderHook(() =>
      useGateway({ wsUrl: "wss://example.com/ws", token: "tok", onMessage })
    );

    const ws = MockWebSocket.instances[0];
    act(() => ws.simulateOpen());

    const connectReq = JSON.parse(ws.sent[0]);
    act(() =>
      ws.simulateMessage({
        type: "res",
        id: connectReq.id,
        ok: true,
        payload: { type: "hello-ok", sessions: [], presence: {}, health: {} },
      })
    );

    // Simulate a chat event (assistant message chunk)
    act(() =>
      ws.simulateMessage({
        type: "event",
        event: "chat",
        payload: {
          type: "text",
          role: "assistant",
          content: "Hello!",
          sessionKey: "s1",
          messageId: "m1",
        },
      })
    );

    expect(onMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "text",
        role: "assistant",
        content: "Hello!",
      })
    );
  });

  it("sends chat.abort request", async () => {
    const { result } = renderHook(() =>
      useGateway({ wsUrl: "wss://example.com/ws", token: "tok" })
    );

    const ws = MockWebSocket.instances[0];
    act(() => ws.simulateOpen());

    const connectReq = JSON.parse(ws.sent[0]);
    act(() =>
      ws.simulateMessage({
        type: "res",
        id: connectReq.id,
        ok: true,
        payload: { type: "hello-ok", sessions: [], presence: {}, health: {} },
      })
    );

    act(() => result.current.abort());

    const abortReq = JSON.parse(ws.sent[1]);
    expect(abortReq.method).toBe("chat.abort");
  });

  it("disconnects when wsUrl becomes null", async () => {
    const { result, rerender } = renderHook(
      (props: { wsUrl: string | null; token: string }) =>
        useGateway(props),
      { initialProps: { wsUrl: "wss://example.com/ws", token: "tok" } }
    );

    const ws = MockWebSocket.instances[0];
    act(() => ws.simulateOpen());

    rerender({ wsUrl: null, token: "tok" });
    expect(ws.readyState).toBe(3); // CLOSED
    expect(result.current.status).toBe("disconnected");
  });
});
