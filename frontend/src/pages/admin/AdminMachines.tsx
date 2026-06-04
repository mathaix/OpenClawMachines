import { useEffect, useState, useCallback } from "react";
import {
  listAllMachines,
  listHosts,
  adminStartMachine,
  adminStopMachine,
  migrateMachine,
  adminResetMachine,
  adminClearMigration,
  adminFlagMigration,
  type Host,
} from "../../lib/api";
import type { Machine } from "../../lib/types";
import { useToast } from "../../components/Toast";

function relativeTime(date: string): string {
  const ms = Date.now() - new Date(date).getTime();
  const minutes = Math.floor(ms / 60000);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

function MetaRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between">
      <span className="text-gray-500 dark:text-gray-400">{label}</span>
      <span className="font-mono text-xs text-gray-900 dark:text-gray-100">{value}</span>
    </div>
  );
}

function statusColor(status: string) {
  switch (status) {
    case "running":
      return "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400";
    case "provisioning":
    case "starting":
      return "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400";
    case "error":
      return "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400";
    case "stopped":
      return "bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-300";
    default:
      return "bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-300";
  }
}

function statusDotColor(status: string) {
  switch (status) {
    case "running":
      return "bg-green-500 animate-pulse";
    case "provisioning":
    case "starting":
      return "bg-yellow-500 animate-pulse";
    case "error":
      return "bg-red-500 animate-pulse";
    case "stopped":
      return "bg-gray-500";
    default:
      return "bg-gray-500";
  }
}

function StatusBadge({ status }: { status: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`w-2 h-2 rounded-full ${statusDotColor(status)}`} />
      <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${statusColor(status)}`}>
        {status}
      </span>
    </span>
  );
}

export function AdminMachines() {
  const [machines, setMachines] = useState<Machine[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [hostFilter, setHostFilter] = useState("all");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [detailMachine, setDetailMachine] = useState<Machine | null>(null);
  const [actionInProgress, setActionInProgress] = useState<Set<string>>(new Set());
  const [migrateTarget, setMigrateTarget] = useState<Machine | null>(null);
  const [migrateHostId, setMigrateHostId] = useState<number | "">("");
  const [migrateForce, setMigrateForce] = useState(false);
  const [migrating, setMigrating] = useState(false);
  const { toast } = useToast();

  const fetchData = useCallback(async () => {
    try {
      const [machineData, hostData] = await Promise.all([
        listAllMachines(),
        listHosts(),
      ]);
      setMachines(machineData || []);
      setHosts(hostData || []);
    } catch (err) {
      console.error("Failed to load admin machines:", err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
    const interval = setInterval(fetchData, 5000);
    return () => clearInterval(interval);
  }, [fetchData]);

  const hostName = useCallback(
    (hostId?: number): string => {
      if (!hostId) return "\u2014";
      const host = hosts.find((h) => h.id === hostId);
      return host?.vm_name || String(hostId);
    },
    [hosts],
  );

  const activeHostIds = new Set(
    hosts.filter((h) => h.status === "ready" || h.status === "provisioning").map((h) => h.id)
  );

  const filtered = machines.filter((m) => {
    // Only show machines on active hosts (unless filtering by a specific host)
    if (hostFilter === "all" && m.host_id && !activeHostIds.has(m.host_id)) return false;
    if (statusFilter !== "all" && m.status !== statusFilter) return false;
    if (hostFilter !== "all" && String(m.host_id) !== hostFilter) return false;
    if (search) {
      const q = search.toLowerCase();
      const hn = hosts.find((h) => h.id === m.host_id)?.vm_name || "";
      return (
        m.name.toLowerCase().includes(q) ||
        m.slug.toLowerCase().includes(q) ||
        m.id.toLowerCase().includes(q) ||
        hn.toLowerCase().includes(q)
      );
    }
    return true;
  });

  const allSelected = filtered.length > 0 && filtered.every((m) => selected.has(m.id));

  const toggleSelectAll = () => {
    if (allSelected) {
      setSelected(new Set());
    } else {
      setSelected(new Set(filtered.map((m) => m.id)));
    }
  };

  const toggleSelect = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const handleStart = async (m: Machine) => {
    setActionInProgress((prev) => new Set(prev).add(m.id));
    try {
      await adminStartMachine(m.id);
      toast({ title: "Machine starting", description: m.name });
      await fetchData();
    } catch (err) {
      toast({
        title: "Start failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionInProgress((prev) => {
        const next = new Set(prev);
        next.delete(m.id);
        return next;
      });
    }
  };

  const handleReset = async (m: Machine) => {
    setActionInProgress((prev) => new Set(prev).add(m.id));
    try {
      await adminResetMachine(m.id);
      toast({ title: "Machine reset to stopped", description: m.name });
      await fetchData();
    } catch (err) {
      toast({
        title: "Reset failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionInProgress((prev) => {
        const next = new Set(prev);
        next.delete(m.id);
        return next;
      });
    }
  };

  const handleClearMigration = async (m: Machine) => {
    setActionInProgress((prev) => new Set(prev).add(m.id));
    try {
      await adminClearMigration(m.id);
      toast({ title: "Migration flag cleared", description: m.name });
      await fetchData();
    } catch (err) {
      toast({
        title: "Clear migration failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionInProgress((prev) => {
        const next = new Set(prev);
        next.delete(m.id);
        return next;
      });
    }
  };

  const handleFlagMigration = async (m: Machine) => {
    setActionInProgress((prev) => new Set(prev).add(m.id));
    try {
      await adminFlagMigration(m.id);
      toast({ title: "Machine flagged for migration", description: m.name });
      await fetchData();
    } catch (err) {
      toast({
        title: "Flag migration failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionInProgress((prev) => {
        const next = new Set(prev);
        next.delete(m.id);
        return next;
      });
    }
  };

  const isMigrationRequired = (m: Machine) =>
    m.status === "error" && (m.status_message || "").toLowerCase().includes("migration required");

  const handleStop = async (m: Machine) => {
    setActionInProgress((prev) => new Set(prev).add(m.id));
    try {
      await adminStopMachine(m.id);
      toast({ title: "Machine stopping", description: m.name });
      await fetchData();
    } catch (err) {
      toast({
        title: "Stop failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionInProgress((prev) => {
        const next = new Set(prev);
        next.delete(m.id);
        return next;
      });
    }
  };

  const bulkStart = async () => {
    const targets = machines.filter((m) => selected.has(m.id) && m.status === "stopped");
    if (targets.length === 0) {
      toast({ title: "No stopped machines selected", variant: "error" });
      return;
    }
    for (const m of targets) {
      await handleStart(m);
    }
    setSelected(new Set());
  };

  const bulkStop = async () => {
    const targets = machines.filter((m) => selected.has(m.id) && m.status === "running");
    if (targets.length === 0) {
      toast({ title: "No running machines selected", variant: "error" });
      return;
    }
    for (const m of targets) {
      await handleStop(m);
    }
    setSelected(new Set());
  };

  const handleMigrate = async () => {
    if (!migrateTarget || !migrateHostId) return;
    const machineName = migrateTarget.name;
    const hostId = migrateHostId;
    setMigrating(true);
    try {
      await migrateMachine(migrateTarget.id, Number(migrateHostId), migrateForce);
      toast({
        title: "Migration started",
        description: `${machineName} is migrating to host ${hostId}. Status will update automatically.`,
      });
      setMigrateTarget(null);
      setMigrateHostId("");
      setMigrateForce(false);
      await fetchData();
    } catch (err) {
      toast({
        title: "Migration failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setMigrating(false);
    }
  };

  const renderUptime = (m: Machine) => {
    if (m.status === "running" && m.started_at) {
      return relativeTime(m.started_at);
    }
    if (m.status === "stopped" && m.stopped_at) {
      return `stopped ${relativeTime(m.stopped_at)} ago`;
    }
    return "\u2014";
  };

  const renderActionButton = (m: Machine) => {
    const inProgress = actionInProgress.has(m.id);
    if (inProgress) {
      return (
        <span className="inline-flex items-center gap-1 text-gray-400 text-xs">
          <svg className="animate-spin h-3 w-3" viewBox="0 0 24 24" fill="none">
            <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
            <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
          </svg>
        </span>
      );
    }
    switch (m.status) {
      case "running":
        return (
          <button
            onClick={() => handleStop(m)}
            className="text-xs font-medium px-2 py-1 rounded bg-red-100 text-red-700 hover:bg-red-200 dark:bg-red-900/30 dark:text-red-400 dark:hover:bg-red-900/50"
          >
            Stop
          </button>
        );
      case "stopped":
        return (
          <div className="flex gap-1">
            <button
              onClick={() => handleStart(m)}
              className="text-xs font-medium px-2 py-1 rounded bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400 dark:hover:bg-green-900/50"
            >
              Start
            </button>
            <button
              onClick={() => handleFlagMigration(m)}
              className="text-xs font-medium px-2 py-1 rounded bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400 dark:hover:bg-orange-900/50"
              title="Flag this machine as requiring migration"
            >
              Flag Migration
            </button>
          </div>
        );
      case "error":
      case "provisioning":
      case "starting":
        return (
          <div className="flex gap-1">
            <button
              onClick={() => handleReset(m)}
              className="text-xs font-medium px-2 py-1 rounded bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400 dark:hover:bg-yellow-900/50"
            >
              Reset
            </button>
            {isMigrationRequired(m) && (
              <button
                onClick={() => handleClearMigration(m)}
                className="text-xs font-medium px-2 py-1 rounded bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-300 dark:hover:bg-indigo-900/50"
              >
                Clear Migration
              </button>
            )}
          </div>
        );
      default:
        return <span className="text-xs text-gray-400">{"\u2014"}</span>;
    }
  };

  // Keep detail panel in sync with refreshed machine data
  const detailMachineData = detailMachine
    ? machines.find((m) => m.id === detailMachine.id) || detailMachine
    : null;

  if (loading) return <p className="text-gray-500 dark:text-gray-400">Loading machines...</p>;

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Machine Management</h1>
      </div>

      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <input
          placeholder="Search machines..."
          className="border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-800 dark:text-gray-200"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className="border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-800 dark:text-gray-200"
        >
          <option value="all">All Status</option>
          <option value="running">Running</option>
          <option value="stopped">Stopped</option>
          <option value="provisioning">Provisioning</option>
          <option value="starting">Starting</option>
          <option value="error">Error</option>
        </select>
        <select
          value={hostFilter}
          onChange={(e) => setHostFilter(e.target.value)}
          className="border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-800 dark:text-gray-200"
        >
          <option value="all">All Hosts</option>
          {hosts.filter((h) => h.status === "ready" || h.status === "provisioning").map((h) => (
            <option key={h.id} value={h.id}>
              {h.vm_name}
            </option>
          ))}
        </select>
        <span className="text-xs text-gray-500">{filtered.length} machines</span>
      </div>

      {/* Machine table */}
      {filtered.length === 0 ? (
        <div className="text-center py-16">
          <p className="text-gray-500 dark:text-gray-400">
            {machines.length === 0 ? "No machines found." : "No machines match the current filters."}
          </p>
        </div>
      ) : (
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 overflow-hidden overflow-x-auto">
          <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
            <thead className="bg-gray-50 dark:bg-gray-900">
              <tr>
                <th className="px-3 py-3 w-8">
                  <input type="checkbox" checked={allSelected} onChange={toggleSelectAll} />
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Status
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Name
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Domain
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Account
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Host
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  vCPU/RAM
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Version
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Uptime
                </th>
                <th className="px-3 py-3 text-left text-xs font-medium text-gray-500 dark:text-gray-400 uppercase">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 dark:divide-gray-700">
              {filtered.map((m) => (
                <tr key={m.id} className={selected.has(m.id) ? "bg-teal-50/50 dark:bg-teal-900/10" : ""}>
                  <td className="px-3 py-3 text-sm">
                    <input
                      type="checkbox"
                      checked={selected.has(m.id)}
                      onChange={() => toggleSelect(m.id)}
                    />
                  </td>
                  <td className="px-3 py-3 text-sm text-gray-700 dark:text-gray-300">
                    <StatusBadge status={m.status} />
                    {m.status === "error" && m.status_message && (
                      <p
                        className="text-xs text-red-600 dark:text-red-400 mt-1 max-w-xs truncate"
                        title={m.status_message}
                      >
                        {m.status_message}
                      </p>
                    )}
                  </td>
                  <td className="px-3 py-3 text-sm text-gray-700 dark:text-gray-300">
                    <button
                      onClick={() => setDetailMachine(m)}
                      className="text-left hover:underline"
                    >
                      <span className="font-mono text-sm text-teal-600 dark:text-teal-400">{m.name}</span>
                      {m.slug && m.slug !== m.name && (
                        <span className="block font-mono text-xs text-gray-400 dark:text-gray-500">{m.slug}</span>
                      )}
                    </button>
                  </td>
                  <td className="px-3 py-3 font-mono text-xs text-gray-700 dark:text-gray-300">
                    {m.custom_domain || "\u2014"}
                  </td>
                  <td className="px-3 py-3 text-sm text-gray-700 dark:text-gray-300">
                    {m.account_id}
                  </td>
                  <td className="px-3 py-3 font-mono text-xs text-gray-700 dark:text-gray-300">
                    {hostName(m.host_id)}
                  </td>
                  <td className="px-3 py-3 text-sm text-gray-700 dark:text-gray-300 font-mono text-xs">
                    {m.vcpus} / {Math.round(m.memory_mb / 1024)}GB
                  </td>
                  <td className="px-3 py-3 font-mono text-xs text-gray-700 dark:text-gray-300">
                    {m.openclaw_version || "\u2014"}
                  </td>
                  <td className="px-3 py-3 text-sm text-gray-700 dark:text-gray-300 font-mono text-xs">
                    {renderUptime(m)}
                  </td>
                  <td className="px-3 py-3 text-sm text-gray-700 dark:text-gray-300">
                    <div className="flex items-center gap-1.5">
                      {renderActionButton(m)}
                      {m.host_id && (
                        <button
                          onClick={() => {
                            setMigrateTarget(m);
                            setMigrateHostId("");
                            setMigrateForce(false);
                          }}
                          className="text-xs font-medium px-2 py-1 rounded bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400 dark:hover:bg-indigo-900/50"
                          title="Migrate to another host"
                        >
                          Migrate
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Slide-out detail panel */}
      {detailMachineData && (
        <>
          <div className="fixed inset-0 bg-black/50 z-40" onClick={() => setDetailMachine(null)} />
          <div className="fixed top-0 right-0 h-full w-96 bg-white dark:bg-gray-800 border-l border-gray-200 dark:border-gray-700 z-50 overflow-y-auto shadow-xl">
            <div className="flex items-center justify-between p-4 border-b border-gray-200 dark:border-gray-700">
              <h3 className="font-mono text-sm font-semibold text-gray-900 dark:text-gray-100">
                {detailMachineData.slug || detailMachineData.name}
              </h3>
              <button
                onClick={() => setDetailMachine(null)}
                className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 text-xl"
              >
                &times;
              </button>
            </div>
            <div className="p-4 space-y-4">
              <StatusBadge status={detailMachineData.status} />
              {detailMachineData.status === "error" && detailMachineData.status_message && (
                <p className="text-xs text-red-600 dark:text-red-400">{detailMachineData.status_message}</p>
              )}
              <div className="space-y-2 text-sm">
                <MetaRow label="ID" value={detailMachineData.id} />
                <MetaRow label="Account" value={String(detailMachineData.account_id)} />
                <MetaRow label="Host" value={hostName(detailMachineData.host_id)} />
                <MetaRow
                  label="vCPU / RAM"
                  value={`${detailMachineData.vcpus} vCPU / ${Math.round(detailMachineData.memory_mb / 1024)} GB`}
                />
                <MetaRow label="Domain" value={detailMachineData.custom_domain || "\u2014"} />
                <MetaRow label="Version" value={detailMachineData.openclaw_version || "\u2014"} />
                <MetaRow label="Created" value={new Date(detailMachineData.created_at).toLocaleString()} />
                {detailMachineData.started_at && (
                  <MetaRow label="Started" value={new Date(detailMachineData.started_at).toLocaleString()} />
                )}
                {detailMachineData.stopped_at && (
                  <MetaRow label="Stopped" value={new Date(detailMachineData.stopped_at).toLocaleString()} />
                )}
                {detailMachineData.vm_ip && <MetaRow label="VM IP" value={detailMachineData.vm_ip} />}
                {detailMachineData.tunnel_hostname && (
                  <MetaRow label="Tunnel" value={detailMachineData.tunnel_hostname} />
                )}
              </div>
              <div className="flex flex-wrap gap-2 pt-2">
                {detailMachineData.status === "running" && (
                  <button
                    onClick={() => handleStop(detailMachineData)}
                    disabled={actionInProgress.has(detailMachineData.id)}
                    className="text-sm font-medium px-3 py-1.5 rounded bg-red-600 text-white hover:bg-red-700 disabled:opacity-50"
                  >
                    Stop Machine
                  </button>
                )}
                {detailMachineData.status === "stopped" && (
                  <>
                    <button
                      onClick={() => handleStart(detailMachineData)}
                      disabled={actionInProgress.has(detailMachineData.id)}
                      className="text-sm font-medium px-3 py-1.5 rounded bg-green-600 text-white hover:bg-green-700 disabled:opacity-50"
                    >
                      Start Machine
                    </button>
                    <button
                      onClick={() => handleFlagMigration(detailMachineData)}
                      disabled={actionInProgress.has(detailMachineData.id)}
                      className="text-sm font-medium px-3 py-1.5 rounded bg-orange-600 text-white hover:bg-orange-700 disabled:opacity-50"
                      title="Flag this machine as requiring migration to a new host"
                    >
                      Flag Migration
                    </button>
                  </>
                )}
                {(detailMachineData.status === "error" || detailMachineData.status === "provisioning" || detailMachineData.status === "starting") && (
                  <button
                    onClick={() => handleReset(detailMachineData)}
                    disabled={actionInProgress.has(detailMachineData.id)}
                    className="text-sm font-medium px-3 py-1.5 rounded bg-yellow-600 text-white hover:bg-yellow-700 disabled:opacity-50"
                  >
                    Reset Machine
                  </button>
                )}
                {isMigrationRequired(detailMachineData) && (
                  <button
                    onClick={() => handleClearMigration(detailMachineData)}
                    disabled={actionInProgress.has(detailMachineData.id)}
                    className="text-sm font-medium px-3 py-1.5 rounded bg-indigo-600 text-white hover:bg-indigo-700 disabled:opacity-50"
                  >
                    Clear Migration Flag
                  </button>
                )}
                {detailMachineData.host_id && (
                  <button
                    onClick={() => {
                      setMigrateTarget(detailMachineData);
                      setMigrateHostId("");
                      setMigrateForce(false);
                      setDetailMachine(null);
                    }}
                    className="text-sm font-medium px-3 py-1.5 rounded bg-indigo-600 text-white hover:bg-indigo-700"
                  >
                    Migrate
                  </button>
                )}
              </div>
            </div>
          </div>
        </>
      )}

      {/* Migrate modal */}
      {migrateTarget && (
        <>
          <div className="fixed inset-0 bg-black/50 z-40" onClick={() => !migrating && setMigrateTarget(null)} />
          <div className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[28rem] bg-white dark:bg-gray-800 rounded-xl shadow-xl z-50 border border-gray-200 dark:border-gray-700">
            <div className="p-5 border-b border-gray-200 dark:border-gray-700">
              <h3 className="text-lg font-semibold text-gray-900 dark:text-gray-100">Migrate Machine</h3>
              <p className="text-sm text-gray-500 dark:text-gray-400 mt-1">
                Move <span className="font-mono font-medium text-gray-700 dark:text-gray-300">{migrateTarget.slug || migrateTarget.name}</span> to another host.
              </p>
            </div>
            <div className="p-5 space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Current Host</label>
                <p className="font-mono text-sm text-gray-900 dark:text-gray-100">
                  {hostName(migrateTarget.host_id)} (ID: {migrateTarget.host_id})
                </p>
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Target Host</label>
                <select
                  value={migrateHostId}
                  onChange={(e) => setMigrateHostId(e.target.value ? Number(e.target.value) : "")}
                  className="w-full border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-900 dark:text-gray-200"
                >
                  <option value="">Select a host...</option>
                  {hosts
                    .filter((h) => h.id !== migrateTarget.host_id)
                    .map((h) => (
                      <option key={h.id} value={h.id} disabled={h.status !== "ready"}>
                        {h.vm_name} ({h.provider || "gcp"}) — {h.status === "ready" ? `${h.capacity_vcpus - h.used_vcpus} vCPU / ${Math.round((h.capacity_memory_mb - h.used_memory_mb) / 1024)}GB free` : h.status}
                      </option>
                    ))}
                </select>
              </div>
              {migrateTarget.backups_enabled && (
                <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-lg p-3">
                  <p className="text-xs text-blue-700 dark:text-blue-300">
                    Backups are enabled. The migration will automatically create a backup, move the machine, and restore data on the target host.
                  </p>
                </div>
              )}
              {!migrateTarget.backups_enabled && (
                <div className="bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 rounded-lg p-3">
                  <p className="text-xs text-yellow-700 dark:text-yellow-300">
                    Backups are not enabled. Migration will lose the data volume unless force is checked.
                  </p>
                </div>
              )}
              <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input
                  type="checkbox"
                  checked={migrateForce}
                  onChange={(e) => setMigrateForce(e.target.checked)}
                  className="rounded"
                />
                Force migration (proceed even if backup fails or is unavailable)
              </label>
            </div>
            <div className="flex justify-end gap-3 p-5 border-t border-gray-200 dark:border-gray-700">
              <button
                onClick={() => setMigrateTarget(null)}
                disabled={migrating}
                className="text-sm font-medium px-4 py-2 rounded-lg border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700 disabled:opacity-50"
              >
                Cancel
              </button>
              <button
                onClick={handleMigrate}
                disabled={!migrateHostId || migrating}
                className="text-sm font-medium px-4 py-2 rounded-lg bg-indigo-600 text-white hover:bg-indigo-700 disabled:opacity-50 inline-flex items-center gap-2"
              >
                {migrating && (
                  <svg className="animate-spin h-4 w-4" viewBox="0 0 24 24" fill="none">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                  </svg>
                )}
                {migrating ? "Migrating..." : "Migrate"}
              </button>
            </div>
          </div>
        </>
      )}

      {/* Bulk action bar */}
      {selected.size > 0 && (
        <div className="fixed bottom-4 left-1/2 -translate-x-1/2 bg-gray-900 dark:bg-gray-700 text-white px-6 py-3 rounded-xl shadow-lg flex items-center gap-4 z-50">
          <span className="text-sm font-medium">{selected.size} selected</span>
          <button
            onClick={bulkStart}
            className="text-sm px-3 py-1 rounded bg-green-600 hover:bg-green-700"
          >
            Start
          </button>
          <button
            onClick={bulkStop}
            className="text-sm px-3 py-1 rounded bg-red-600 hover:bg-red-700"
          >
            Stop
          </button>
        </div>
      )}
    </div>
  );
}
