import { useEffect, useState, useCallback, useRef, useMemo } from "react";
import { useParams, useNavigate, useSearchParams, Link } from "react-router-dom";
import { Panel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { getMachine, stopMachine, getGatewayHealth, restartGateway } from "../lib/api";
import type { GatewayHealthResponse } from "../lib/api";
import { useAuth } from "../lib/auth";
import { machineWebchatUrl } from "../lib/machineInterface";
import { WebTerminal, clearTerminalSession } from "../components/Terminal";
import { CopyButton } from "../components/CopyButton";
import { useKeyboardShortcuts } from "../lib/useKeyboardShortcuts";
import type { Machine } from "../lib/types";

function hermesAwareTabs(kind?: Machine["kind"]) {
  const isHermes = kind === "hermes";
  return [
    { id: "shell", label: "Shell" },
    { id: "tui", label: isHermes ? "Hermes" : "TUI", command: isHermes ? "hermes" : "openclaw" },
  ];
}

export function MachineWorkspace() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { account } = useAuth();
  const [machine, setMachine] = useState<Machine | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [stopping, setStopping] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const [showSshInfo, setShowSshInfo] = useState(false);
  const [gatewayHealth, setGatewayHealth] = useState<GatewayHealthResponse["gateway"]>("unknown");
  const [restarting, setRestarting] = useState(false);
  const [termTabs, setTermTabs] = useState<{ id: string; label: string; command?: string }[]>(hermesAwareTabs());
  const [activeTab, setActiveTab] = useState("shell");
  const tabCounter = useRef(1);
  const accountId = account?.id;
  const [searchParams, setSearchParams] = useSearchParams();
  const [view, setView] = useState<"terminal" | "gateway">(
    searchParams.get("view") === "gateway" ? "gateway" : "terminal"
  );
  const [gatewayIframeLoaded, setGatewayIframeLoaded] = useState(false);
  // Track whether gateway iframe was ever requested (lazy mount)
  const [gatewayMounted, setGatewayMounted] = useState(
    searchParams.get("view") === "gateway"
  );

  const fetchMachine = useCallback(async () => {
    if (!id || !accountId) return;
    try {
      const data = await getMachine(accountId, id);
      setMachine(data);
      // If machine stopped, redirect to detail view
      if (data.status === "stopped") {
        navigate(`/machines/${id}`);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load machine");
    } finally {
      setLoading(false);
    }
  }, [id, accountId, navigate]);

  useEffect(() => {
    fetchMachine();
    // Poll for status changes
    pollRef.current = setInterval(fetchMachine, 10000);
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [fetchMachine]);

  const displayedTermTabs = useMemo(() => {
    if (!machine) return termTabs;
    const tuiTab = hermesAwareTabs(machine.kind)[1];
    return termTabs.map((tab) =>
      tab.id === "tui" ? { ...tab, label: tuiTab.label, command: tuiTab.command } : tab,
    );
  }, [machine, termTabs]);

  useEffect(() => {
    if (!machine) return;
    const tuiTab = hermesAwareTabs(machine.kind)[1];
    setTermTabs((prev) =>
      prev.map((tab) => (tab.id === "tui" ? { ...tab, label: tuiTab.label, command: tuiTab.command } : tab)),
    );
  }, [machine?.kind]);

  // Poll gateway health when machine is running
  useEffect(() => {
    if (!machine || machine.status !== "running" || !account || machine.kind === "hermes") {
      setGatewayHealth("unknown");
      return;
    }
    const poll = () => {
      getGatewayHealth(account.slug, machine.slug, account.id, machine.id)
        .then((r) => setGatewayHealth(r.gateway))
        .catch(() => setGatewayHealth("unreachable"));
    };
    poll();
    const iv = setInterval(poll, 10000);
    return () => clearInterval(iv);
  }, [machine?.status, machine?.id, machine?.slug, account]);

  const handleStop = async () => {
    if (!id || !accountId) return;
    setStopping(true);
    try {
      await stopMachine(accountId, id);
      navigate(`/machines/${id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to stop");
      setStopping(false);
    }
  };

  const handleRestartGateway = async () => {
    if (!account || !machine) return;
    setRestarting(true);
    try {
      await restartGateway(account.slug, machine.slug, account.id, machine.id);
      // Brief delay then poll health to show recovery
      setTimeout(() => {
        getGatewayHealth(account.slug, machine.slug, account.id, machine.id)
          .then((r) => setGatewayHealth(r.gateway))
          .catch(() => setGatewayHealth("unreachable"));
        setRestarting(false);
      }, 3000);
    } catch (err) {
      setRestarting(false);
      setError(err instanceof Error ? err.message : "Failed to restart gateway");
    }
  };

  const addTermTab = useCallback(() => {
    tabCounter.current += 1;
    const newId = `tab-${tabCounter.current}`;
    setTermTabs((prev) => [...prev, { id: newId, label: `Shell ${tabCounter.current}` }]);
    setActiveTab(newId);
  }, []);

  const closeTermTab = useCallback((tabId: string) => {
    if (!id) return;
    setTermTabs((prev) => {
      if (prev.length <= 1) return prev;
      const idx = prev.findIndex((t) => t.id === tabId);
      const next = prev.filter((t) => t.id !== tabId);
      if (tabId === activeTab) {
        const newIdx = Math.min(idx, next.length - 1);
        setActiveTab(next[newIdx].id);
      }
      return next;
    });
    clearTerminalSession(id, tabId);
  }, [id, activeTab]);

  const shortcuts = useMemo(() => [
    { key: "t", ctrl: true, action: addTermTab, description: "New terminal tab" },
  ], [addTermTab]);

  useKeyboardShortcuts(shortcuts);

  const toggleView = useCallback((v: "terminal" | "gateway") => {
    setView(v);
    if (v === "gateway") setGatewayMounted(true);
    setSearchParams(v === "gateway" ? { view: "gateway" } : {}, { replace: true });
  }, [setSearchParams]);

  const dashboardUrl = useMemo(() => {
    if (!machine || !account || machine.status !== "running") return null;
    return machineWebchatUrl(account.slug, account.id, machine);
  }, [machine, account]);

  if (!account) return null;
  if (loading) {
    return (
      <div className="fixed inset-0 bg-gray-900 flex items-center justify-center">
        <p className="text-gray-400">Loading...</p>
      </div>
    );
  }
  if (error) {
    return (
      <div className="fixed inset-0 bg-gray-900 flex items-center justify-center">
        <p className="text-red-400">Error: {error}</p>
      </div>
    );
  }
  if (!machine) {
    return (
      <div className="fixed inset-0 bg-gray-900 flex items-center justify-center">
        <p className="text-gray-400">Machine not found</p>
      </div>
    );
  }

  const isRunning = machine.status === "running";
  const isHermesMachine = machine.kind === "hermes";
  const interfaceLabel = isHermesMachine ? "Webchat" : "Chat";
  const interfaceTitle = isHermesMachine ? "Hermes Webchat" : "Gateway Dashboard";
  const interfaceLoadingLabel = isHermesMachine ? "Loading Hermes Webchat..." : "Loading Gateway Dashboard...";
  const interfaceUnavailableLabel = isHermesMachine ? "Webchat unavailable" : "Gateway unavailable";

  return (
    <div className="fixed inset-0 bg-gray-900 flex flex-col">
      {/* Header bar */}
      <div className="flex items-center justify-between px-4 py-2 bg-gray-800 border-b border-gray-700">
        <div className="flex items-center gap-4">
          <Link
            to={`/machines/${id}`}
            className="text-gray-400 hover:text-white text-sm"
          >
            &larr; Back
          </Link>
          <div>
            <h1 className="text-white font-medium">{machine.name}</h1>
            <p className="text-gray-500 text-xs">{machine.slug}</p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => toggleView("terminal")}
            disabled={!isRunning}
            className={`text-sm ${
              view === "terminal"
                ? "text-white font-medium"
                : isRunning ? "text-gray-400 hover:text-white" : "text-gray-600"
            } disabled:text-gray-600 disabled:hover:text-gray-600`}
          >
            Terminal
          </button>
          <button
            onClick={() => toggleView("gateway")}
            disabled={!isRunning}
            className={`text-sm ${
              view === "gateway"
                ? "text-white font-medium"
                : isRunning ? "text-gray-400 hover:text-white" : "text-gray-600"
            } disabled:text-gray-600 disabled:hover:text-gray-600`}
          >
            {interfaceLabel}
          </button>
          <button
            onClick={() => setShowSshInfo(!showSshInfo)}
            disabled={!isRunning}
            className="text-sm disabled:text-gray-600 text-gray-400 hover:text-white disabled:hover:text-gray-600"
          >
            SSH
          </button>
          {isRunning && !isHermesMachine && (
            <div
              className={`flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium ${
                gatewayHealth === "running"
                  ? "bg-green-900/50 text-green-400"
                  : gatewayHealth === "crash-loop"
                    ? "bg-red-900/50 text-red-400"
                    : "bg-yellow-900/50 text-yellow-400"
              }`}
              title={`Gateway: ${gatewayHealth}`}
            >
              <span
                className={`inline-block w-2 h-2 rounded-full ${
                  gatewayHealth === "running"
                    ? "bg-green-400"
                    : gatewayHealth === "crash-loop"
                      ? "bg-red-400 animate-pulse"
                      : "bg-yellow-400"
                }`}
              />
              Gateway
            </div>
          )}
          {!isHermesMachine && (
            <button
              onClick={handleRestartGateway}
              disabled={!isRunning || restarting}
              className="text-sm disabled:text-gray-600 disabled:opacity-50 text-gray-400 hover:text-white disabled:hover:text-gray-600"
            >
              {restarting ? "Restarting..." : "Restart Gateway"}
            </button>
          )}
          <div
            className={`flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium ${
              machine.status === "running"
                ? "bg-green-900/50 text-green-400"
                : (machine.status === "provisioning" || machine.status === "starting")
                  ? "bg-yellow-900/50 text-yellow-400"
                  : "bg-gray-700 text-gray-400"
            }`}
          >
            {machine.status}
          </div>
          <button
            onClick={handleStop}
            disabled={!isRunning || stopping}
            className="bg-red-600 text-white px-3 py-1 rounded text-sm font-medium hover:bg-red-700 disabled:opacity-50"
          >
            {stopping ? "Stopping..." : "Stop"}
          </button>
        </div>
      </div>

      {/* SSH Access info panel */}
      {showSshInfo && isRunning && (
        <div className="px-4 py-2 bg-gray-800 border-b border-gray-700 flex items-center gap-4">
          <span className="text-xs text-gray-400 font-medium uppercase tracking-wide shrink-0">SSH</span>
          <a
            href={`https://ssh-${machine.slug}.openclawmachines.com`}
            target="_blank"
            rel="noreferrer"
            className="text-xs text-blue-400 hover:text-blue-300 hover:underline shrink-0"
          >
            Open in browser
          </a>
          <div className="flex items-center gap-2 bg-gray-900 rounded px-2 py-0.5 min-w-0">
            <code className="text-sm text-green-400 font-mono truncate">
              ocm machines ssh {machine.name}
            </code>
            <CopyButton text={`ocm machines ssh "${machine.name}"`} />
          </div>
        </div>
      )}

      {/* Main content */}
      <div className="flex-1 min-h-0 relative">
        {/* Terminal panels — hidden (not unmounted) when gateway view is active */}
        <div className={`absolute inset-0 ${view === "terminal" ? "" : "invisible"}`}>
          <PanelGroup direction="vertical" autoSaveId="workspace-main" className="h-full">
            {/* Top panel - Terminal tabs (Shell, TUI, +) */}
            <Panel defaultSize={65} minSize={20}>
              <div className="h-full flex flex-col min-h-0">
                <div className="flex items-center bg-gray-800 border-b border-gray-700">
                  <div className="flex items-center overflow-x-auto min-w-0">
                    {displayedTermTabs.map((tab) => (
                      <button
                        key={tab.id}
                        onClick={() => setActiveTab(tab.id)}
                        className={`group flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium border-r border-gray-700 shrink-0 ${
                          activeTab === tab.id
                            ? "text-white bg-gray-700"
                            : "text-gray-400 hover:text-gray-300 hover:bg-gray-750"
                        }`}
                      >
                        <span>{tab.label}</span>
                        {termTabs.length > 1 && (
                          <span
                            onClick={(e) => {
                              e.stopPropagation();
                              closeTermTab(tab.id);
                            }}
                            className="opacity-0 group-hover:opacity-100 text-gray-500 hover:text-gray-300 ml-0.5"
                          >
                            &times;
                          </span>
                        )}
                      </button>
                    ))}
                  </div>
                  <button
                    onClick={addTermTab}
                    className="px-2 py-1.5 text-gray-500 hover:text-gray-300 text-sm shrink-0"
                    title="New terminal (Ctrl+T)"
                  >
                    +
                  </button>
                </div>
                <div className="flex-1 min-h-0 relative">
                  {isRunning && accountId && id ? (
                    displayedTermTabs.map((tab) => (
                      <div
                        key={tab.id}
                        className={`absolute inset-0 ${activeTab === tab.id ? "" : "invisible"}`}
                      >
                        <WebTerminal
                          accountId={accountId}
                          machineId={id}
                          accountSlug={account.slug}
                          machineSlug={machine.slug}
                          className="h-full"
                          tabId={tab.id}
                          command={tab.command}
                          visible={activeTab === tab.id && view === "terminal"}
                        />
                      </div>
                    ))
                  ) : (
                    <div className="h-full flex items-center justify-center bg-[#1a1a2e]">
                      <p className="text-gray-500 text-sm">
                        {(machine.status === "provisioning" || machine.status === "starting")
                          ? "Waiting for machine to start..."
                          : "Machine not running"}
                      </p>
                    </div>
                  )}
                </div>
              </div>
            </Panel>

            <PanelResizeHandle className="h-1.5 bg-gray-700 hover:bg-blue-500 transition-colors flex items-center justify-center">
              <div className="w-8 h-0.5 rounded-full bg-gray-500" />
            </PanelResizeHandle>

            {/* Bottom panel - service logs */}
            <Panel defaultSize={35} minSize={10} collapsible>
              <div className="h-full flex flex-col min-h-0">
                <div className="px-3 py-1.5 bg-gray-800 border-b border-gray-700">
                  <span className="text-xs text-gray-400 font-medium uppercase tracking-wide">
                    Logs
                  </span>
                </div>
                <div className="flex-1 min-h-0">
                  {isRunning && accountId && id ? (
                    <WebTerminal
                      accountId={accountId}
                      machineId={id}
                      accountSlug={account.slug}
                      machineSlug={machine.slug}
                      className="h-full"
                      tabId="logs"
                      command="logs"
                      visible={view === "terminal"}
                    />
                  ) : (
                    <div className="h-full flex items-center justify-center bg-[#1a1a2e]">
                      <p className="text-gray-500 text-sm">
                        {(machine.status === "provisioning" || machine.status === "starting")
                          ? "Waiting for machine to start..."
                          : "Machine not running"}
                      </p>
                    </div>
                  )}
                </div>
              </div>
            </Panel>
          </PanelGroup>
        </div>

        {/* Gateway iframe — stays mounted once opened, hidden when terminal view is active */}
        {gatewayMounted && (
          <div className={`absolute inset-0 ${view === "gateway" ? "" : "invisible"}`}>
            {isRunning && dashboardUrl ? (
              <>
                {!gatewayIframeLoaded && view === "gateway" && (
                  <div className="absolute inset-0 flex items-center justify-center bg-gray-900 z-10">
                    <div className="text-center">
                      <div className="inline-block h-8 w-8 border-2 border-gray-600 border-t-gray-300 rounded-full animate-spin mb-3" />
                      <p className="text-gray-400 text-sm">{interfaceLoadingLabel}</p>
                    </div>
                  </div>
                )}
                <iframe
                  src={dashboardUrl}
                  className="w-full h-full border-0"
                  onLoad={() => setGatewayIframeLoaded(true)}
                  title={interfaceTitle}
                  sandbox="allow-same-origin allow-scripts allow-forms allow-popups"
                />
              </>
            ) : (
              <div className="flex items-center justify-center h-full">
                <p className="text-gray-400 text-sm">
                  {(machine.status === "provisioning" || machine.status === "starting")
                    ? "Machine is starting..."
                    : machine.status === "stopped"
                      ? "Machine is stopped"
                      : interfaceUnavailableLabel}
                </p>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
