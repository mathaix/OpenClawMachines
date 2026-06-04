import { useState, useEffect, useCallback, useRef } from "react";
import { useGateway, type GatewayMessage } from "./useGateway";
import { ChatMessageList } from "./ChatMessageList";
import { ChatInput } from "./ChatInput";
import { dataPlaneWsUrl, getGatewayHealth } from "../../lib/api";
import type { Machine } from "../../lib/types";

interface ChatContainerProps {
  machine: Machine;
  accountSlug: string;
}

type GatewayReadiness =
  | "checking"
  | "starting"
  | "ready"
  | "no_token"
  | "error";

export function ChatContainer({ machine, accountSlug }: ChatContainerProps) {
  const [messages, setMessages] = useState<GatewayMessage[]>([]);
  const [readiness, setReadiness] = useState<GatewayReadiness>("checking");
  const [wsUrl, setWsUrl] = useState<string | null>(null);
  const prevMachineIdRef = useRef<string | null>(null);

  // Clear messages when machine changes
  useEffect(() => {
    if (prevMachineIdRef.current && prevMachineIdRef.current !== machine.id) {
      setMessages([]);
    }
    prevMachineIdRef.current = machine.id;
  }, [machine.id]);

  // Check gateway health before connecting
  useEffect(() => {
    if (!machine.gateway_token) {
      setReadiness("no_token");
      setWsUrl(null);
      return;
    }

    let cancelled = false;
    let pollTimer: ReturnType<typeof setTimeout>;

    const checkHealth = async () => {
      try {
        const health = await getGatewayHealth(
          accountSlug,
          machine.slug,
          machine.account_id,
          machine.id
        );
        if (cancelled) return;

        if (health.gateway === "running") {
          setReadiness("ready");
          const url = dataPlaneWsUrl(
            accountSlug,
            machine.slug,
            "ws",
            machine.account_id,
            machine.id
          );
          setWsUrl(url);
        } else {
          setReadiness("starting");
          pollTimer = setTimeout(checkHealth, 5000);
        }
      } catch {
        if (cancelled) return;
        setReadiness("starting");
        pollTimer = setTimeout(checkHealth, 5000);
      }
    };

    setReadiness("checking");
    checkHealth();

    return () => {
      cancelled = true;
      clearTimeout(pollTimer);
    };
  }, [machine.id, machine.gateway_token, machine.slug, machine.account_id, accountSlug]);

  const handleMessage = useCallback((msg: GatewayMessage) => {
    setMessages((prev) => {
      // If streaming text update for same messageId, replace last
      if (
        msg.streaming &&
        msg.messageId &&
        prev.length > 0 &&
        prev[prev.length - 1].messageId === msg.messageId
      ) {
        return [...prev.slice(0, -1), msg];
      }
      return [...prev, msg];
    });
  }, []);

  // Track pending sessions to load history after gateway connects
  const [pendingSessions, setPendingSessions] = useState<
    { sessionKey: string; createdAt: string }[] | null
  >(null);

  const handleSessionList = useCallback(
    (sessions: { sessionKey: string; createdAt: string }[]) => {
      if (sessions.length > 0) {
        setPendingSessions(sessions);
      }
    },
    []
  );

  const gateway = useGateway({
    wsUrl: readiness === "ready" ? wsUrl : null,
    token: machine.gateway_token ?? "",
    onMessage: handleMessage,
    onSessionList: handleSessionList,
  });

  // Load history for the most recent session once gateway is connected
  useEffect(() => {
    if (pendingSessions && gateway.status === "connected") {
      const mostRecent = [...pendingSessions].sort(
        (a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()
      )[0];
      gateway.loadHistory(mostRecent.sessionKey);
      setPendingSessions(null);
    }
  }, [pendingSessions, gateway.status, gateway.loadHistory]);

  // Readiness states
  if (machine.status !== "running") {
    return (
      <div className="flex flex-1 items-center justify-center text-zinc-400">
        <p>Machine is not running</p>
      </div>
    );
  }

  if (readiness === "checking") {
    return (
      <div className="flex flex-1 items-center justify-center text-zinc-400">
        <div className="h-5 w-5 animate-spin rounded-full border-2 border-zinc-600 border-t-zinc-300" />
        <span className="ml-2">Checking gateway...</span>
      </div>
    );
  }

  if (readiness === "no_token" || readiness === "starting") {
    return (
      <div className="flex flex-1 items-center justify-center text-zinc-400">
        <div className="h-5 w-5 animate-spin rounded-full border-2 border-zinc-600 border-t-zinc-300" />
        <span className="ml-2">Gateway starting...</span>
      </div>
    );
  }

  const isConnected = gateway.status === "connected";

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      {/* Connection status banner */}
      {gateway.status === "connecting" || gateway.status === "authenticating" ? (
        <div className="bg-yellow-900/30 px-4 py-1 text-center text-xs text-yellow-300">
          Connecting to gateway...
        </div>
      ) : gateway.status === "error" ? (
        <div className="bg-red-900/30 px-4 py-1 text-center text-xs text-red-300">
          Connection error. Retrying...
        </div>
      ) : gateway.status === "disconnected" && wsUrl ? (
        <div className="bg-red-900/30 px-4 py-1 text-center text-xs text-red-300">
          Disconnected
        </div>
      ) : null}

      <ChatMessageList messages={messages} streaming={gateway.streaming} />
      <ChatInput
        onSend={gateway.sendMessage}
        onAbort={gateway.abort}
        disabled={!isConnected}
        streaming={gateway.streaming}
      />
    </div>
  );
}
