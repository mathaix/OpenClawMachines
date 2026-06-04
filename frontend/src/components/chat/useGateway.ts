import { useState, useEffect, useRef, useCallback } from "react";

// --- Types ---

export type ConnectionStatus =
  | "disconnected"
  | "connecting"
  | "authenticating"
  | "connected"
  | "error";

export interface GatewayMessage {
  type: string; // "text", "tool_call", "tool_result", "thinking", "image", "error", etc.
  role: "user" | "assistant" | "system";
  content: string;
  sessionKey?: string;
  messageId?: string;
  toolName?: string;
  toolCallId?: string;
  thinkingMs?: number;
  streaming?: boolean;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  metadata?: Record<string, any>;
}

interface GatewayRequest {
  type: "req";
  id: string;
  method: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  params?: Record<string, any>;
}

interface GatewayResponse {
  type: "res";
  id: string;
  ok: boolean;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  payload?: Record<string, any>;
  error?: { code: string; message: string };
}

interface GatewayEvent {
  type: "event";
  event: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  payload: Record<string, any>;
}

type GatewayFrame = GatewayResponse | GatewayEvent;

export interface UseGatewayOptions {
  wsUrl: string | null;
  token: string;
  onMessage?: (msg: GatewayMessage) => void;
  onSessionList?: (sessions: { sessionKey: string; createdAt: string }[]) => void;
  onError?: (error: string) => void;
}

export interface UseGatewayReturn {
  status: ConnectionStatus;
  sendMessage: (content: string, sessionKey?: string) => void;
  abort: () => void;
  loadHistory: (sessionKey: string) => void;
  listSessions: () => void;
  streaming: boolean;
}

// --- Hook ---

let idCounter = 0;
function nextId(): string {
  return `ocm-${Date.now()}-${++idCounter}`;
}

export function useGateway(options: UseGatewayOptions): UseGatewayReturn {
  const { wsUrl, token, onMessage, onSessionList, onError } = options;

  const [status, setStatus] = useState<ConnectionStatus>(
    wsUrl ? "connecting" : "disconnected"
  );
  const [streaming, setStreaming] = useState(false);

  const [reconnectTrigger, setReconnectTrigger] = useState(0);
  const reconnectAttemptRef = useRef(0);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const statusRef = useRef(status);
  statusRef.current = status;

  // Stable callbacks via refs
  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;
  const onSessionListRef = useRef(onSessionList);
  onSessionListRef.current = onSessionList;
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;

  const sendRequest = useCallback(
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (method: string, params?: Record<string, any>): string => {
      const id = nextId();
      const req: GatewayRequest = { type: "req", id, method, ...(params ? { params } : {}) };
      wsRef.current?.send(JSON.stringify(req));
      return id;
    },
    []
  );

  // Connect effect
  useEffect(() => {
    if (!wsUrl) {
      setStatus("disconnected");
      return;
    }

    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;
    setStatus("connecting");

    let connectId: string | null = null;

    ws.onopen = () => {
      setStatus("authenticating");
      connectId = nextId();
      const connectReq: GatewayRequest = {
        type: "req",
        id: connectId,
        method: "connect",
        params: {
          minProtocol: 3,
          maxProtocol: 3,
          auth: { token },
          client: { id: "ocm-chat", version: "1.0", platform: "web", mode: "webchat" },
          role: "operator",
          scopes: ["operator.admin"],
        },
      };
      ws.send(JSON.stringify(connectReq));
    };

    ws.onmessage = (event: MessageEvent) => {
      let frame: GatewayFrame;
      try {
        frame = JSON.parse(event.data as string);
      } catch {
        return;
      }

      // Handle connect response
      if (frame.type === "res" && (frame as GatewayResponse).id === connectId) {
        const res = frame as GatewayResponse;
        if (res.ok && res.payload?.type === "hello-ok") {
          setStatus("connected");
          reconnectAttemptRef.current = 0; // Reset on successful connect
          // If sessions exist in snapshot, notify
          if (res.payload.sessions && onSessionListRef.current) {
            onSessionListRef.current(res.payload.sessions);
          }
        } else {
          setStatus("error");
          onErrorRef.current?.(res.error?.message || "Connection rejected");
        }
        return;
      }

      // Handle other responses (chat.send ack, etc.)
      if (frame.type === "res") {
        const res = frame as GatewayResponse;
        if (res.payload?.sessions && onSessionListRef.current) {
          onSessionListRef.current(res.payload.sessions);
        }
        if (res.payload?.messages && onMessageRef.current) {
          for (const msg of res.payload.messages) {
            onMessageRef.current(msg);
          }
        }
        return;
      }

      // Handle events
      if (frame.type === "event") {
        const evt = frame as GatewayEvent;
        if (evt.event === "chat" && onMessageRef.current) {
          onMessageRef.current(evt.payload as GatewayMessage);
        }
        if (evt.event === "chat.stream.start") {
          setStreaming(true);
        }
        if (evt.event === "chat.stream.end" || evt.event === "chat.done") {
          setStreaming(false);
        }
      }
    };

    let cancelled = false;

    ws.onclose = (event) => {
      if (statusRef.current !== "disconnected") {
        setStatus("disconnected");
      }
      // Auto-reconnect with exponential backoff (unless intentionally closed)
      if (event.code !== 1000 && !cancelled) {
        const delay = Math.min(1000 * Math.pow(2, reconnectAttemptRef.current), 30000);
        reconnectAttemptRef.current++;
        reconnectTimerRef.current = setTimeout(() => {
          // Trigger reconnect by re-running effect
          setReconnectTrigger((n) => n + 1);
        }, delay);
      }
    };

    ws.onerror = () => {
      setStatus("error");
    };

    return () => {
      cancelled = true;
      ws.close();
      wsRef.current = null;
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current);
    };
  }, [wsUrl, token, reconnectTrigger]);

  const sendMessage = useCallback(
    (content: string, sessionKey?: string) => {
      sendRequest("chat.send", {
        content,
        ...(sessionKey ? { sessionKey } : {}),
      });
      setStreaming(true);
    },
    [sendRequest]
  );

  const abort = useCallback(() => {
    sendRequest("chat.abort");
    setStreaming(false);
  }, [sendRequest]);

  const loadHistory = useCallback(
    (sessionKey: string) => {
      sendRequest("chat.history", { sessionKey });
    },
    [sendRequest]
  );

  const listSessions = useCallback(() => {
    sendRequest("sessions.list");
  }, [sendRequest]);

  return { status, sendMessage, abort, loadHistory, listSessions, streaming };
}
