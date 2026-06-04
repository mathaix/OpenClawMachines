import { useState, useEffect, useCallback, useRef } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { listMachines } from "../lib/api";
import { canOpenWebchat, machineWebchatUrl } from "../lib/machineInterface";
import { MachinePicker } from "../components/MachinePicker";
import type { Machine } from "../lib/types";

const STORAGE_KEY_PREFIX = "ocm_chat_machine_";
const HERMES_SESSION_KEY_PREFIX = "ocm_hermes_chat_session_";

interface HermesSessionMessage {
  type: "ocm.hermes.session";
  sessionId: string;
  lastActive?: number | null;
}

function hermesSessionStorageKey(accountId: number, machineId: string): string {
  return `${HERMES_SESSION_KEY_PREFIX}${accountId}:${machineId}`;
}

function appendResumeParam(url: string, sessionId: string | null): string {
  if (!sessionId) return url;
  const parsed = new URL(url, window.location.origin);
  parsed.searchParams.set("resume", sessionId);
  return parsed.toString();
}

function originForUrl(url: string | null): string | null {
  if (!url) return null;
  try {
    return new URL(url, window.location.origin).origin;
  } catch {
    return null;
  }
}

function isHermesSessionMessage(value: unknown): value is HermesSessionMessage {
  if (!value || typeof value !== "object") return false;
  const data = value as Record<string, unknown>;
  return data.type === "ocm.hermes.session"
    && typeof data.sessionId === "string"
    && data.sessionId.trim().length > 0
    && (
      data.lastActive === undefined
      || data.lastActive === null
      || typeof data.lastActive === "number"
    );
}

export default function ChatPage() {
  const { account } = useAuth();
  const navigate = useNavigate();
  const { machineId: urlMachineId } = useParams<{ machineId?: string }>();

  const [machines, setMachines] = useState<Machine[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedId, setSelectedId] = useState<string | null>(urlMachineId ?? null);
  const [iframeLoading, setIframeLoading] = useState(true);
  const [hermesSessionId, setHermesSessionId] = useState<string | null>(null);
  const [hermesChatReloadKey, setHermesChatReloadKey] = useState(0);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const hermesSessionCutoffRef = useRef<number | null>(null);

  // Fetch running machines
  const fetchMachines = useCallback(async () => {
    if (!account) return;
    try {
      const all = await listMachines(account.id);
      const running = (all ?? []).filter(canOpenWebchat);
      setMachines(running);
    } catch (err) {
      console.error("Failed to fetch machines:", err);
    } finally {
      setLoading(false);
    }
  }, [account]);

  useEffect(() => {
    fetchMachines();
    const interval = setInterval(fetchMachines, 10000);
    return () => clearInterval(interval);
  }, [fetchMachines]);

  // Auto-select: URL param takes priority, then session storage, then single machine
  useEffect(() => {
    if (urlMachineId) {
      setSelectedId(urlMachineId);
      return;
    }
    if (machines.length === 0 || selectedId) return;

    // Single machine: auto-select
    if (machines.length === 1) {
      setSelectedId(machines[0].id);
      return;
    }

    // Restore from session storage
    const stored = account
      ? sessionStorage.getItem(STORAGE_KEY_PREFIX + account.id)
      : null;
    const match = machines.find((m) => m.id === stored);
    if (match) {
      setSelectedId(match.id);
    }
  }, [machines, urlMachineId, selectedId, account]);

  // Persist selection and reset iframe loading state on change
  const handleSelect = useCallback(
    (id: string) => {
      setSelectedId(id);
      setIframeLoading(true);
      if (account) {
        sessionStorage.setItem(STORAGE_KEY_PREFIX + account.id, id);
      }
    },
    [account]
  );

  const handleBack = useCallback(() => {
    navigate(-1);
  }, [navigate]);

  const selectedMachine = machines.find((m) => m.id === selectedId);
  const isHermesChat = selectedMachine?.kind === "hermes";

  useEffect(() => {
    if (!account || !selectedMachine || selectedMachine.kind !== "hermes") {
      setHermesSessionId(null);
      hermesSessionCutoffRef.current = null;
      return;
    }

    const stored = localStorage.getItem(hermesSessionStorageKey(account.id, selectedMachine.id));
    setHermesSessionId(stored);
    hermesSessionCutoffRef.current = null;
  }, [account, selectedMachine?.id, selectedMachine?.kind]);

  const handleNewHermesChat = useCallback(() => {
    if (!account || !selectedMachine || selectedMachine.kind !== "hermes") return;
    localStorage.removeItem(hermesSessionStorageKey(account.id, selectedMachine.id));
    hermesSessionCutoffRef.current = Date.now();
    setHermesSessionId(null);
    setIframeLoading(true);
    setHermesChatReloadKey((key) => key + 1);
  }, [account, selectedMachine?.id, selectedMachine?.kind]);

  const baseChatUrl = selectedMachine && account?.slug
    ? machineWebchatUrl(account.slug, account.id, selectedMachine)
    : null;
  const chatUrl = baseChatUrl && isHermesChat
    ? appendResumeParam(baseChatUrl, hermesSessionId)
    : baseChatUrl;

  useEffect(() => {
    if (!account || !selectedMachine || selectedMachine.kind !== "hermes") return;
    const expectedOrigin = originForUrl(baseChatUrl);
    if (!expectedOrigin) return;

    const onMessage = (event: MessageEvent) => {
      if (event.origin !== expectedOrigin || !isHermesSessionMessage(event.data)) return;

      const cutoff = hermesSessionCutoffRef.current;
      if (cutoff && (!event.data.lastActive || event.data.lastActive < cutoff)) {
        return;
      }

      localStorage.setItem(
        hermesSessionStorageKey(account.id, selectedMachine.id),
        event.data.sessionId.trim(),
      );
    };

    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [account, baseChatUrl, selectedMachine?.id, selectedMachine?.kind]);

  // --- Chat view ---
  if (selectedMachine && chatUrl) {
    return (
      <div className="flex flex-col" style={{ height: "calc(100vh - 56px)" }}>
        {/* Header bar */}
        <div className="flex items-center gap-3 border-b border-border px-4 py-2 flex-shrink-0">
          <button
            onClick={handleBack}
            className="text-sm text-text-secondary hover:text-text-primary transition-colors"
          >
            &larr; Back
          </button>
          <div className="flex items-center gap-2 min-w-0">
            <span className="h-2 w-2 rounded-full bg-green-500 flex-shrink-0" />
            <span className="text-sm font-medium text-text-primary truncate">
              {selectedMachine.name}
            </span>
          </div>
          {isHermesChat && (
            <button
              onClick={handleNewHermesChat}
              className="ml-auto text-sm text-text-secondary hover:text-text-primary transition-colors"
            >
              New chat
            </button>
          )}
        </div>

        {/* Iframe area */}
        <div className="relative flex-1 min-h-0">
          {iframeLoading && (
            <div className="absolute inset-0 flex items-center justify-center bg-deep z-10">
              <div className="flex flex-col items-center gap-3">
                <div className="h-6 w-6 rounded-full border-2 border-brand-500 border-t-transparent animate-spin" />
                <span className="text-sm text-text-secondary">Connecting…</span>
              </div>
            </div>
          )}
          <iframe
            ref={iframeRef}
            key={`${chatUrl}:${hermesChatReloadKey}`}
            src={chatUrl}
            allow="clipboard-read; clipboard-write; microphone; camera"
            onLoad={() => {
              setIframeLoading(false);
            }}
            className="w-full h-full border-0"
            title={`Chat – ${selectedMachine.name}`}
          />
        </div>
      </div>
    );
  }

  // --- Machine picker view ---
  return (
    <div>
      <div className="mx-auto max-w-md w-full py-8">
        <h1 className="text-lg font-semibold text-text-primary mb-1">
          Select a machine
        </h1>
        <p className="text-sm text-text-secondary mb-5">
          Choose which running machine to chat with.
        </p>
        <MachinePicker
          machines={machines}
          onSelect={handleSelect}
          loading={loading}
        />
      </div>
    </div>
  );
}
