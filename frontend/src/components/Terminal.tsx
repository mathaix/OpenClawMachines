import { useEffect, useRef, useState, useCallback } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { ClipboardAddon } from "@xterm/addon-clipboard";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import "@xterm/xterm/css/xterm.css";
import { dataPlaneWsUrl } from "../lib/api";
import { useReconnectingWebSocket } from "../lib/useReconnectingWebSocket";
import { parseFrame } from "../lib/ws/protocol";
import { resolveTerminalAction } from "../lib/ws/terminalPolicy";

interface WebTerminalProps {
  accountId: number;
  machineId: string;
  accountSlug?: string;
  machineSlug?: string;
  className?: string;
  tabId?: string;
  command?: string;
  visible?: boolean;
}

export function clearTerminalSession(machineId: string, tabId: string) {
  sessionStorage.removeItem(`pty-session:${machineId}:${tabId}`);
}

export function WebTerminal({
  accountId,
  machineId,
  accountSlug,
  machineSlug,
  className = "",
  tabId,
  command,
  visible = true,
}: WebTerminalProps) {
  const termRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const searchAddonRef = useRef<SearchAddon | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const searchInputRef = useRef<HTMLInputElement>(null);
  // Persist session_id in sessionStorage so it survives component unmount/remount
  // (e.g. navigating away from terminal tab and back)
  const sessionKey = tabId ? `pty-session:${machineId}:${tabId}` : `pty-session:${machineId}`;
  const sessionIdRef = useRef<string | null>(
    sessionStorage.getItem(sessionKey),
  );
  const hasContentRef = useRef(false);
  const resizeFrameRef = useRef<number | null>(null);
  const sendRef = useRef<(data: string | ArrayBuffer) => void>(() => {});

  const rawBaseUrl = dataPlaneWsUrl(accountSlug, machineSlug, "terminal/ws", accountId, machineId);
  const baseUrl = command
    ? rawBaseUrl + (rawBaseUrl.includes("?") ? "&" : "?") + "command=" + encodeURIComponent(command)
    : rawBaseUrl;

  // Build URL with session_id on reconnect so PTY server reattaches to existing session
  const getUrl = useCallback(() => {
    const sid = sessionIdRef.current;
    if (sid) {
      const sep = baseUrl.includes("?") ? "&" : "?";
      return baseUrl + sep + "session_id=" + encodeURIComponent(sid);
    }
    return baseUrl;
  }, [baseUrl]);

  const handleMessage = useCallback((event: MessageEvent) => {
    const terminal = terminalRef.current;
    if (!terminal) return;

    const frame = parseFrame(event);

    // Handle binary frames (ArrayBuffer) directly — needs TextDecoder,
    // so not covered by the string-based terminalPolicy
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

  const sendResize = useCallback((sendFn: (data: string) => void) => {
    const terminal = terminalRef.current;
    if (!terminal) return;

    const { cols, rows } = terminal;
    if (cols <= 0 || rows <= 0) return;

    sendFn("1" + JSON.stringify({ columns: cols, rows: rows }));
  }, []);

  const fitAndSendResize = useCallback(() => {
    const fitAddon = fitAddonRef.current;
    if (!fitAddon || !terminalRef.current) return;

    fitAddon.fit();
    sendResize((data) => sendRef.current(data));
  }, [sendResize]);

  const scheduleFitAndSendResize = useCallback(() => {
    if (resizeFrameRef.current !== null) {
      cancelAnimationFrame(resizeFrameRef.current);
    }
    resizeFrameRef.current = requestAnimationFrame(() => {
      resizeFrameRef.current = null;
      fitAndSendResize();
    });
  }, [fitAndSendResize]);

  const handleOpen = useCallback((ws: WebSocket) => {
    sendResize((data) => ws.send(data));
  }, [sendResize]);

  const { send, status, reconnect } = useReconnectingWebSocket({
    url: baseUrl,
    getUrl,
    onMessage: handleMessage,
    onOpen: handleOpen,
  });

  sendRef.current = send;

  useEffect(() => {
    if (!termRef.current) return;

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      allowProposedApi: true,
      theme: {
        background: "#1a1a2e",
        foreground: "#eaeaea",
        cursor: "#eaeaea",
        selectionBackground: "#3d3d5c",
      },
    });

    const fitAddon = new FitAddon();
    const searchAddon = new SearchAddon();
    const unicode11 = new Unicode11Addon();

    terminal.loadAddon(fitAddon);
    terminal.loadAddon(new ClipboardAddon());
    terminal.loadAddon(new WebLinksAddon());
    terminal.loadAddon(searchAddon);
    terminal.loadAddon(unicode11);
    terminal.unicode.activeVersion = "11";

    terminal.attachCustomKeyEventHandler((event: KeyboardEvent) => {
      // Let browser handle Ctrl+Shift+C/V (copy/paste)
      if (event.ctrlKey && event.shiftKey && (event.key === 'C' || event.key === 'V')) {
        return false;
      }
      // Let browser handle Cmd+C/V on Mac
      if (event.metaKey && (event.key === 'c' || event.key === 'v')) {
        return false;
      }
      // Ctrl+Shift+F: toggle search
      if (event.ctrlKey && event.shiftKey && event.key === 'F' && event.type === 'keydown') {
        setSearchOpen((prev) => !prev);
        return false;
      }
      return true;
    });

    terminal.open(termRef.current);
    fitAddon.fit();

    const termElement = termRef.current;
    const handleContextMenu = (e: MouseEvent) => {
      e.preventDefault();
      const selection = terminal.getSelection();
      if (selection) {
        navigator.clipboard.writeText(selection).catch(() => {});
      } else {
        navigator.clipboard.readText().then((text) => {
          sendRef.current("0" + text);
        }).catch(() => {});
      }
    };
    termElement.addEventListener("contextmenu", handleContextMenu);

    terminalRef.current = terminal;
    fitAddonRef.current = fitAddon;
    searchAddonRef.current = searchAddon;

    // Handle user input
    terminal.onData((data) => {
      sendRef.current("0" + data);
    });

    // Handle resize
    const handleResize = () => {
      fitAndSendResize();
    };

    window.addEventListener("resize", handleResize);

    return () => {
      if (resizeFrameRef.current !== null) {
        cancelAnimationFrame(resizeFrameRef.current);
        resizeFrameRef.current = null;
      }
      termElement.removeEventListener("contextmenu", handleContextMenu);
      window.removeEventListener("resize", handleResize);
      terminal.dispose();
    };
  }, [fitAndSendResize]);

  // Re-fit when container size changes
  useEffect(() => {
    const observer = new ResizeObserver(() => {
      scheduleFitAndSendResize();
    });

    if (termRef.current) {
      observer.observe(termRef.current);
    }

    return () => observer.disconnect();
  }, [scheduleFitAndSendResize]);

  // Re-fit terminal when tab becomes visible (xterm.js can't measure hidden containers)
  useEffect(() => {
    if (visible && fitAddonRef.current && terminalRef.current) {
      scheduleFitAndSendResize();
    }
  }, [visible, scheduleFitAndSendResize]);

  // Focus search input when opened
  useEffect(() => {
    if (searchOpen) {
      searchInputRef.current?.focus();
    }
  }, [searchOpen]);

  const handleSearchKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      setSearchOpen(false);
      setSearchQuery("");
      searchAddonRef.current?.clearDecorations();
    } else if (e.key === "Enter") {
      if (e.shiftKey) {
        searchAddonRef.current?.findPrevious(searchQuery);
      } else {
        searchAddonRef.current?.findNext(searchQuery);
      }
    }
  };

  const statusDot =
    status === "connected"
      ? "bg-green-500"
      : status === "connecting"
        ? "bg-yellow-500 animate-pulse"
        : status === "error"
          ? "bg-red-500"
          : "bg-gray-500";

  const statusColor =
    status === "connected"
      ? "text-green-400"
      : status === "connecting"
        ? "text-yellow-400"
        : status === "error"
          ? "text-red-400"
          : "text-gray-500";

  return (
    <div className={`flex flex-col bg-[#1a1a2e] rounded-lg overflow-hidden ${className}`}>
      <div className="flex items-center justify-between px-3 py-1.5 bg-[#16162a] border-b border-gray-700">
        <div className="flex items-center gap-2">
          <span className={`inline-block h-2 w-2 rounded-full ${statusDot}`} />
          <span className={`text-xs font-medium capitalize ${statusColor}`}>
            {status}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setSearchOpen((v) => !v)}
            className="text-xs text-gray-400 hover:text-gray-300 font-medium"
            title="Search (Ctrl+Shift+F)"
          >
            Search
          </button>
          {status === "error" && (
            <button
              onClick={reconnect}
              className="text-xs text-blue-400 hover:text-blue-300 font-medium"
            >
              Reconnect
            </button>
          )}
        </div>
      </div>
      {searchOpen && (
        <div className="flex items-center gap-2 px-3 py-1.5 bg-[#16162a] border-b border-gray-700">
          <input
            ref={searchInputRef}
            type="text"
            value={searchQuery}
            onChange={(e) => {
              setSearchQuery(e.target.value);
              searchAddonRef.current?.findNext(e.target.value);
            }}
            onKeyDown={handleSearchKeyDown}
            className="flex-1 bg-gray-800 text-gray-200 text-xs rounded px-2 py-1 outline-none border border-gray-600 focus:border-blue-500"
            placeholder="Find..."
          />
          <button
            onClick={() => searchAddonRef.current?.findPrevious(searchQuery)}
            className="text-xs text-gray-400 hover:text-gray-300 px-1"
            title="Previous (Shift+Enter)"
          >
            &uarr;
          </button>
          <button
            onClick={() => searchAddonRef.current?.findNext(searchQuery)}
            className="text-xs text-gray-400 hover:text-gray-300 px-1"
            title="Next (Enter)"
          >
            &darr;
          </button>
          <button
            onClick={() => {
              setSearchOpen(false);
              setSearchQuery("");
              searchAddonRef.current?.clearDecorations();
            }}
            className="text-xs text-gray-400 hover:text-gray-300 px-1"
          >
            &times;
          </button>
        </div>
      )}
      <div ref={termRef} className="flex-1 p-1" />
    </div>
  );
}
