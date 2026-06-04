import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { dataPlaneUrl } from "../lib/api";

interface LogConsoleProps {
  accountId: number;
  machineId: string;
  accountSlug?: string;
  machineSlug?: string;
  className?: string;
  onConnectionStatusChange?: (status: "connecting" | "connected" | "disconnected") => void;
  onReady?: (api: { clear: () => void }) => void;
}

interface ParsedLog {
  timestamp: string;
  level: "error" | "warn" | "info" | "debug" | "unknown";
  message: string;
}

function parseLogLine(raw: string): ParsedLog {
  // Try JSON: {"level":"error","timestamp":"...","message":"..."}
  try {
    const obj = JSON.parse(raw);
    if (obj.message) {
      return {
        timestamp: obj.timestamp || obj.time || obj.ts || new Date().toISOString(),
        level: (obj.level || "info").toLowerCase() as ParsedLog["level"],
        message: obj.message || obj.msg || raw,
      };
    }
  } catch { /* not JSON */ }

  // Try bracket pattern: [ERROR] message or [WARN] message
  const bracketMatch = raw.match(/\[(ERROR|WARN|WARNING|INFO|DEBUG)\]\s*(.*)/i);
  if (bracketMatch) {
    return {
      timestamp: new Date().toLocaleTimeString(),
      level: bracketMatch[1].toLowerCase().replace("warning", "warn") as ParsedLog["level"],
      message: bracketMatch[2],
    };
  }

  // Try slog pattern: level=error msg="..."
  const slogMatch = raw.match(/level=(\w+)\s+msg="([^"]+)"/);
  if (slogMatch) {
    return {
      timestamp: new Date().toLocaleTimeString(),
      level: slogMatch[1].toLowerCase() as ParsedLog["level"],
      message: slogMatch[2],
    };
  }

  // Fallback
  return {
    timestamp: new Date().toLocaleTimeString(),
    level: "unknown",
    message: raw,
  };
}

export function LogConsole({ accountId, machineId, accountSlug, machineSlug, className = "", onConnectionStatusChange, onReady }: LogConsoleProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const eventSourceRef = useRef<EventSource | null>(null);
  const hasContentRef = useRef(false);
  const [connectionStatus, setConnectionStatus] = useState<"connecting" | "connected" | "disconnected">("disconnected");
  const onConnectionStatusChangeRef = useRef(onConnectionStatusChange);
  onConnectionStatusChangeRef.current = onConnectionStatusChange;
  const onReadyRef = useRef(onReady);
  onReadyRef.current = onReady;

  useEffect(() => {
    onConnectionStatusChangeRef.current?.(connectionStatus);
  }, [connectionStatus]);

  useEffect(() => {
    if (!containerRef.current) return;

    const terminal = new Terminal({
      cursorBlink: false,
      disableStdin: true,
      fontSize: 13,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
      allowProposedApi: true,
      theme: {
        background: "#1a1b26",
        foreground: "#a9b1d6",
        cursor: "#a9b1d6",
      },
      convertEol: true,
      scrollback: 5000,
    });

    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(containerRef.current);
    fitAddon.fit();

    termRef.current = terminal;
    fitAddonRef.current = fitAddon;

    onReadyRef.current?.({ clear: () => terminal.clear() });

    // Connect to SSE log stream (auth via HttpOnly cookie)
    const url = dataPlaneUrl(accountSlug, machineSlug, "logs", accountId, machineId);

    setConnectionStatus("connecting");
    const es = new EventSource(url, { withCredentials: true });
    eventSourceRef.current = es;

    const writeParsedLog = (data: string) => {
      const parsed = parseLogLine(data);
      const levelColors: Record<string, string> = {
        error: "\x1b[31m",
        warn: "\x1b[33m",
        info: "\x1b[36m",
        debug: "\x1b[90m",
        unknown: "",
      };
      const color = levelColors[parsed.level] || "";
      const reset = "\x1b[0m";
      const dim = "\x1b[90m";
      const line = `${dim}${parsed.timestamp}${reset} ${color}${parsed.message}${reset}`;
      terminal.writeln(line);
    };

    es.onopen = () => {
      setConnectionStatus("connected");
    };

    // Live log messages
    es.onmessage = (event) => {
      setConnectionStatus("connected");
      hasContentRef.current = true;
      writeParsedLog(event.data);
    };

    // Replay messages (buffered history) — skip if terminal already has content
    es.addEventListener("replay", (event) => {
      setConnectionStatus("connected");
      if (!hasContentRef.current) {
        writeParsedLog((event as MessageEvent).data);
      }
    });

    es.onerror = () => {
      // EventSource readyState: 0=CONNECTING, 1=OPEN, 2=CLOSED
      if (es.readyState === EventSource.CLOSED) {
        setConnectionStatus("disconnected");
        terminal.writeln("\x1b[31m[Connection closed by server]\x1b[0m");
      } else if (es.readyState === EventSource.CONNECTING) {
        terminal.writeln("\x1b[33m[Reconnecting...]\x1b[0m");
      } else {
        terminal.writeln("\x1b[31m[Connection error]\x1b[0m");
      }
    };

    // Resize handler
    const handleResize = () => {
      fitAddon.fit();
    };
    window.addEventListener("resize", handleResize);

    // ResizeObserver for container size changes
    const observer = new ResizeObserver(() => {
      fitAddon.fit();
    });
    if (containerRef.current) {
      observer.observe(containerRef.current);
    }

    return () => {
      observer.disconnect();
      window.removeEventListener("resize", handleResize);
      es.close();
      terminal.dispose();
    };
  }, [accountId, machineId, accountSlug, machineSlug]);

  return (
    <div
      ref={containerRef}
      className={`overflow-hidden bg-[#1a1b26] ${className}`}
    />
  );
}
