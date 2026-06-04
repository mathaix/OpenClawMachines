import { useCallback, useEffect, useRef, useState } from "react";

export type ConnectionStatus = "connecting" | "connected" | "disconnected" | "error";

interface UseReconnectingWebSocketOptions {
  url: string;
  /** Optional callback to build URL dynamically on each connect (e.g. to append session_id).
   *  If provided, takes precedence over `url` for reconnects. */
  getUrl?: () => string;
  onMessage: (event: MessageEvent) => void;
  onStatusChange?: (status: ConnectionStatus) => void;
  onOpen?: (ws: WebSocket) => void;
  maxRetries?: number;
  initialDelay?: number;
  maxDelay?: number;
  binaryType?: BinaryType;
}

interface UseReconnectingWebSocketReturn {
  send: (data: string | ArrayBuffer) => void;
  status: ConnectionStatus;
  reconnect: () => void;
}

export function useReconnectingWebSocket(
  options: UseReconnectingWebSocketOptions,
): UseReconnectingWebSocketReturn {
  const {
    url,
    getUrl,
    onMessage,
    onStatusChange,
    onOpen,
    maxRetries = 10,
    initialDelay = 1000,
    maxDelay = 30000,
    binaryType,
  } = options;

  const [status, setStatus] = useState<ConnectionStatus>("disconnected");
  const wsRef = useRef<WebSocket | null>(null);
  const retryCountRef = useRef(0);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  // Store latest callbacks in refs to avoid reconnect loops
  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;
  const onStatusChangeRef = useRef(onStatusChange);
  onStatusChangeRef.current = onStatusChange;
  const onOpenRef = useRef(onOpen);
  onOpenRef.current = onOpen;
  const getUrlRef = useRef(getUrl);
  getUrlRef.current = getUrl;

  const updateStatus = useCallback((s: ConnectionStatus) => {
    if (!mountedRef.current) return;
    setStatus(s);
    onStatusChangeRef.current?.(s);
  }, []);

  const connect = useCallback(() => {
    if (!mountedRef.current) return;
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }

    updateStatus("connecting");

    const connectUrl = getUrlRef.current ? getUrlRef.current() : url;
    const ws = new WebSocket(connectUrl);
    if (binaryType) ws.binaryType = binaryType;
    wsRef.current = ws;

    ws.onopen = () => {
      if (!mountedRef.current) return;
      retryCountRef.current = 0;
      updateStatus("connected");
      onOpenRef.current?.(ws);
    };

    ws.onclose = (event) => {
      if (!mountedRef.current) return;
      wsRef.current = null;

      if (event.code === 1000 || event.code === 1001) {
        updateStatus("disconnected");
        return;
      }

      // Schedule reconnect if under max retries
      if (retryCountRef.current < maxRetries) {
        const delay = Math.min(initialDelay * Math.pow(2, retryCountRef.current), maxDelay);
        retryCountRef.current++;
        updateStatus("connecting");
        retryTimerRef.current = setTimeout(() => {
          if (mountedRef.current) connect();
        }, delay);
      } else {
        updateStatus("error");
      }
    };

    ws.onerror = () => {
      // onclose will fire after onerror, so let onclose handle reconnect
      if (wsRef.current === ws) {
        ws.close();
      }
    };

    ws.onmessage = (event) => {
      onMessageRef.current(event);
    };
  }, [url, binaryType, maxRetries, initialDelay, maxDelay, updateStatus]);

  const send = useCallback((data: string | ArrayBuffer) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data);
    }
  }, []);

  const reconnect = useCallback(() => {
    retryCountRef.current = 0;
    if (retryTimerRef.current) {
      clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
    connect();
  }, [connect]);

  useEffect(() => {
    mountedRef.current = true;
    connect();
    return () => {
      mountedRef.current = false;
      if (retryTimerRef.current) {
        clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
    };
  }, [connect]);

  return { send, status, reconnect };
}
