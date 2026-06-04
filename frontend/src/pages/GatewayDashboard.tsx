import { useEffect, useState, useCallback, useMemo, useRef } from "react";
import { useParams, Link } from "react-router-dom";
import { getMachine, stopMachine } from "../lib/api";
import { machineWebchatUrl } from "../lib/machineInterface";
import { useAuth } from "../lib/auth";
import type { Machine } from "../lib/types";

export function GatewayDashboard() {
  const { id } = useParams<{ id: string }>();
  const { account } = useAuth();
  const [machine, setMachine] = useState<Machine | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [stopping, setStopping] = useState(false);
  const [iframeLoaded, setIframeLoaded] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const accountId = account?.id;

  const fetchMachine = useCallback(async () => {
    if (!id || !accountId) return;
    try {
      const data = await getMachine(accountId, id);
      setMachine(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load machine");
    } finally {
      setLoading(false);
    }
  }, [id, accountId]);

  useEffect(() => {
    fetchMachine();
    pollRef.current = setInterval(fetchMachine, 15000);
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [fetchMachine]);

  const handleStop = async () => {
    if (!id || !accountId) return;
    setStopping(true);
    try {
      await stopMachine(accountId, id);
      await fetchMachine();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to stop");
      setStopping(false);
    }
  };

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

  if (error && !machine) {
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

  return (
    <div className="fixed inset-0 bg-gray-900 flex flex-col">
      {/* Header bar — matches workspace pattern */}
      <div className="flex items-center justify-between px-4 py-2 bg-gray-800 border-b border-gray-700">
        <div className="flex items-center gap-4">
          <Link
            to="/dashboard"
            className="text-gray-400 hover:text-white text-sm"
          >
            &larr; Dashboard
          </Link>
          <div>
            <h1 className="text-white font-medium">{machine.name}</h1>
            <p className="text-gray-500 text-xs">Gateway Dashboard</p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <Link
            to={`/workspace/${id}`}
            className="text-gray-400 hover:text-white text-sm"
          >
            Workspace
          </Link>
          <Link
            to={`/machines/${id}`}
            className="text-gray-400 hover:text-white text-sm"
          >
            Settings
          </Link>
          <span
            className={`px-2 py-0.5 rounded text-xs font-medium ${
              machine.status === "running"
                ? "bg-green-900/50 text-green-400"
                : (machine.status === "provisioning" || machine.status === "starting")
                  ? "bg-yellow-900/50 text-yellow-400"
                  : "bg-gray-700 text-gray-400"
            }`}
          >
            {machine.status}
          </span>
          {isRunning && (
            <button
              onClick={handleStop}
              disabled={stopping}
              className="bg-red-600 text-white px-3 py-1 rounded text-sm font-medium hover:bg-red-700 disabled:opacity-50"
            >
              {stopping ? "Stopping..." : "Stop"}
            </button>
          )}
        </div>
      </div>

      {/* Dashboard iframe or status message */}
      <div className="flex-1 min-h-0 relative">
        {isRunning && dashboardUrl ? (
          <>
            {!iframeLoaded && (
              <div className="absolute inset-0 flex items-center justify-center bg-gray-900">
                <div className="text-center">
                  <div className="inline-block h-8 w-8 border-2 border-gray-600 border-t-gray-300 rounded-full animate-spin mb-3" />
                  <p className="text-gray-400 text-sm">Loading Gateway Dashboard...</p>
                </div>
              </div>
            )}
            <iframe
              src={dashboardUrl}
              className="w-full h-full border-0"
              onLoad={() => setIframeLoaded(true)}
              title="OpenClawMachines Gateway Dashboard"
              sandbox="allow-same-origin allow-scripts allow-forms allow-popups"
            />
          </>
        ) : (
          <div className="flex items-center justify-center h-full">
            <div className="text-center">
              <p className="text-gray-400 text-lg mb-2">
                {(machine.status === "provisioning" || machine.status === "starting")
                  ? "Machine is starting..."
                  : machine.status === "stopped"
                    ? "Machine is stopped"
                    : "Gateway unavailable"}
              </p>
              <p className="text-gray-500 text-sm">
                {machine.status === "stopped"
                  ? "Start the machine to access the Gateway Dashboard."
                  : "The Gateway Dashboard will be available once the machine is running."}
              </p>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
