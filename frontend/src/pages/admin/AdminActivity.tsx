import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  listAdminWorkflows,
  listAdminWorkflowEvents,
  listHosts,
  type AdminWorkflowRun,
  type WorkflowEvent,
  type Host,
} from "../../lib/api";

const MIGRATION_PHASES = [
  "prepare",
  "stop_source",
  "backup_source",
  "destroy_source_vm",
  "release_source",
  "start_target",
  "wait_target_running",
  "stop_for_restore",
  "wait_stopped",
  "restore_backup",
  "restart_restored",
  "wait_restored_running",
  "complete",
];

function statusBadge(status: string) {
  const styles: Record<string, string> = {
    queued: "bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300",
    running: "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
    waiting: "bg-yellow-100 text-yellow-700 dark:bg-yellow-900/40 dark:text-yellow-300",
    completed: "bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300",
    failed: "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
    cancelled: "bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-400",
    manual_action_required: "bg-orange-100 text-orange-700 dark:bg-orange-900/40 dark:text-orange-300",
  };
  return (
    <span className={`px-2 py-0.5 text-xs font-medium rounded-full ${styles[status] || styles.queued}`}>
      {status.replace(/_/g, " ")}
    </span>
  );
}

function phaseBadge(phase: string | undefined) {
  if (!phase) return null;
  return (
    <span className="px-2 py-0.5 text-xs font-mono rounded bg-gray-100 dark:bg-gray-700 text-gray-600 dark:text-gray-300">
      {phase.replace(/_/g, " ")}
    </span>
  );
}

function relativeTime(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime();
  const secs = Math.floor(diff / 1000);
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

function duration(startStr?: string, endStr?: string): string {
  if (!startStr) return "-";
  const start = new Date(startStr).getTime();
  const end = endStr ? new Date(endStr).getTime() : Date.now();
  const secs = Math.floor((end - start) / 1000);
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  const remSecs = secs % 60;
  return `${mins}m ${remSecs}s`;
}

function PhaseTimeline({
  workflow,
  events,
}: {
  workflow: AdminWorkflowRun;
  events: WorkflowEvent[];
}) {
  const phases = workflow.kind === "migration" ? MIGRATION_PHASES : [];

  // Build phase → event mapping
  const phaseEvents = new Map<string, WorkflowEvent[]>();
  for (const e of events) {
    if (e.phase) {
      const existing = phaseEvents.get(e.phase) || [];
      existing.push(e);
      phaseEvents.set(e.phase, existing);
    }
  }

  // Determine which phases are completed, active, or pending
  const currentPhase = workflow.current_phase;
  const currentIdx = currentPhase ? phases.indexOf(currentPhase) : -1;
  const isTerminal = workflow.status === "completed" || workflow.status === "failed" || workflow.status === "cancelled";

  if (phases.length === 0) {
    // Non-migration workflow — just show events as a log
    return (
      <div className="space-y-1">
        {events.length === 0 ? (
          <div className="text-xs text-gray-500">No events recorded.</div>
        ) : (
          events.map((e) => (
            <div key={e.id} className="flex items-start gap-2 text-xs">
              <span className="text-gray-500 dark:text-gray-400 font-mono shrink-0 w-16">
                {new Date(e.created_at).toLocaleTimeString()}
              </span>
              <span className={`shrink-0 w-10 font-medium ${
                e.level === "error" ? "text-red-500" :
                e.level === "warn" ? "text-yellow-500" : "text-gray-500"
              }`}>
                {e.level}
              </span>
              {e.phase && (
                <span className="font-mono text-gray-400 shrink-0">
                  [{e.phase}]
                </span>
              )}
              <span className="text-gray-700 dark:text-gray-300">{e.message}</span>
            </div>
          ))
        )}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {/* Phase progress bar */}
      <div className="flex items-center gap-0.5">
        {phases.map((phase, idx) => {
          let bg = "bg-gray-200 dark:bg-gray-700"; // pending
          if (isTerminal && workflow.status === "completed") {
            bg = "bg-green-500"; // all completed
          } else if (isTerminal && workflow.status === "failed") {
            if (idx < currentIdx) bg = "bg-green-500";
            else if (idx === currentIdx) bg = "bg-red-500";
          } else if (idx < currentIdx) {
            bg = "bg-green-500"; // completed
          } else if (idx === currentIdx) {
            bg = "bg-blue-500 animate-pulse"; // active
          }
          return (
            <div
              key={phase}
              className={`h-2 flex-1 rounded-sm ${bg}`}
              title={phase.replace(/_/g, " ")}
            />
          );
        })}
      </div>

      {/* Phase labels */}
      <div className="grid grid-cols-1 gap-1">
        {phases.map((phase, idx) => {
          const evts = phaseEvents.get(phase) || [];
          const isActive = idx === currentIdx && !isTerminal;
          const isDone = isTerminal ? (workflow.status === "completed" || idx < currentIdx) : idx < currentIdx;
          const isFailed = isTerminal && workflow.status === "failed" && idx === currentIdx;

          if (evts.length === 0 && !isActive && !isDone && !isFailed) return null;

          return (
            <div key={phase} className="flex items-start gap-2 text-xs">
              <span className={`w-4 h-4 flex items-center justify-center shrink-0 mt-0.5 rounded-full text-[10px] font-bold ${
                isFailed ? "bg-red-500 text-white" :
                isActive ? "bg-blue-500 text-white" :
                isDone ? "bg-green-500 text-white" :
                "bg-gray-200 dark:bg-gray-700 text-gray-400"
              }`}>
                {isFailed ? "!" : isDone ? "\u2713" : isActive ? "\u25CF" : ""}
              </span>
              <div className="flex-1 min-w-0">
                <div className={`font-medium ${
                  isFailed ? "text-red-600 dark:text-red-400" :
                  isActive ? "text-blue-600 dark:text-blue-400" :
                  isDone ? "text-gray-700 dark:text-gray-300" :
                  "text-gray-400"
                }`}>
                  {phase.replace(/_/g, " ")}
                </div>
                {evts.map((e) => (
                  <div key={e.id} className="text-gray-500 dark:text-gray-400 mt-0.5">
                    <span className="font-mono text-gray-400 mr-1">
                      {new Date(e.created_at).toLocaleTimeString()}
                    </span>
                    {e.message}
                  </div>
                ))}
              </div>
            </div>
          );
        })}
      </div>

      {/* Error display */}
      {workflow.error_message && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-2 text-xs text-red-700 dark:text-red-300">
          <span className="font-medium">Error: </span>
          {workflow.error_code && (
            <span className="font-mono text-red-500 mr-1">[{workflow.error_code}]</span>
          )}
          {workflow.error_message}
        </div>
      )}
    </div>
  );
}

export function AdminActivity() {
  const [workflows, setWorkflows] = useState<AdminWorkflowRun[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [loading, setLoading] = useState(true);
  const [kindFilter, setKindFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [search, setSearch] = useState("");
  const [hostFilter, setHostFilter] = useState("");
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [events, setEvents] = useState<Map<string, WorkflowEvent[]>>(new Map());
  const [autoRefresh, setAutoRefresh] = useState(true);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchData = useCallback(async () => {
    try {
      const [wfs, hostList] = await Promise.all([
        listAdminWorkflows({ kind: kindFilter || undefined, status: statusFilter || undefined, limit: 100 }),
        listHosts(),
      ]);
      setWorkflows(wfs);
      setHosts(hostList);
    } catch (e) {
      console.error("Failed to fetch workflows:", e);
    } finally {
      setLoading(false);
    }
  }, [kindFilter, statusFilter]);

  useEffect(() => {
    setLoading(true);
    fetchData();
  }, [fetchData]);

  // Auto-refresh
  useEffect(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    if (autoRefresh) {
      intervalRef.current = setInterval(fetchData, 5000);
    }
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [autoRefresh, fetchData]);

  const toggleExpand = async (id: string) => {
    if (expandedId === id) {
      setExpandedId(null);
      return;
    }
    setExpandedId(id);
    if (!events.has(id)) {
      try {
        const evts = await listAdminWorkflowEvents(id);
        setEvents((prev) => new Map(prev).set(id, evts));
      } catch (e) {
        console.error("Failed to fetch events:", e);
      }
    }
  };

  // Refresh events for expanded workflow
  useEffect(() => {
    if (!expandedId || !autoRefresh) return;
    const wf = workflows.find((w) => w.id === expandedId);
    if (!wf || wf.status === "completed" || wf.status === "failed" || wf.status === "cancelled") return;

    const refreshEvents = async () => {
      try {
        const evts = await listAdminWorkflowEvents(expandedId);
        setEvents((prev) => new Map(prev).set(expandedId, evts));
      } catch {
        // ignore
      }
    };

    const interval = setInterval(refreshEvents, 3000);
    return () => clearInterval(interval);
  }, [expandedId, autoRefresh, workflows]);

  const hostMap = useMemo(() => {
    const m = new Map<number, Host>();
    for (const h of hosts) m.set(h.id, h);
    return m;
  }, [hosts]);

  const filtered = useMemo(() => {
    return workflows.filter((w) => {
      if (search) {
        const q = search.toLowerCase();
        const matchName = w.machine_name?.toLowerCase().includes(q);
        const matchSlug = w.machine_slug?.toLowerCase().includes(q);
        const matchId = w.scope_id.toLowerCase().includes(q);
        const matchWfId = w.id.toLowerCase().includes(q);
        if (!matchName && !matchSlug && !matchId && !matchWfId) return false;
      }
      if (hostFilter) {
        const hid = parseInt(hostFilter, 10);
        if (w.host_id !== hid) return false;
      }
      return true;
    });
  }, [workflows, search, hostFilter]);

  // Get unique kinds from data
  const kinds = useMemo(() => {
    const s = new Set(workflows.map((w) => w.kind));
    return Array.from(s).sort();
  }, [workflows]);

  if (loading) {
    return <div className="text-gray-500 dark:text-gray-400">Loading workflows...</div>;
  }

  return (
    <div>
      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <input
          placeholder="Search by machine name, slug, or ID..."
          className="px-3 py-1.5 text-xs rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100 placeholder-gray-400 w-64"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />

        <select
          value={kindFilter}
          onChange={(e) => setKindFilter(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
        >
          <option value="">All Types</option>
          {kinds.map((k) => (
            <option key={k} value={k}>{k}</option>
          ))}
        </select>

        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
        >
          <option value="">All Statuses</option>
          <option value="queued">Queued</option>
          <option value="running">Running</option>
          <option value="waiting">Waiting</option>
          <option value="completed">Completed</option>
          <option value="failed">Failed</option>
          <option value="cancelled">Cancelled</option>
        </select>

        <select
          value={hostFilter}
          onChange={(e) => setHostFilter(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
        >
          <option value="">All Hosts</option>
          {hosts.map((h) => (
            <option key={h.id} value={h.id}>{h.vm_name}</option>
          ))}
        </select>

        <label className="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400 cursor-pointer ml-auto">
          <span>Auto-refresh</span>
          <button
            onClick={() => setAutoRefresh(!autoRefresh)}
            className={`w-8 h-4 rounded-full transition-colors ${
              autoRefresh ? "bg-green-500" : "bg-gray-400"
            }`}
          >
            <span
              className={`block w-3 h-3 rounded-full bg-white transition-transform ${
                autoRefresh ? "translate-x-4" : "translate-x-0.5"
              }`}
            />
          </button>
        </label>

        <span className="text-xs text-gray-500 dark:text-gray-400">
          {filtered.length} workflow{filtered.length !== 1 ? "s" : ""}
        </span>
      </div>

      {/* Workflow list */}
      {filtered.length === 0 ? (
        <div className="text-gray-500 dark:text-gray-400 text-sm py-8 text-center">
          No workflows found.
        </div>
      ) : (
        <div className="space-y-2">
          {filtered.map((w) => {
            const isExpanded = expandedId === w.id;
            const host = w.host_id ? hostMap.get(w.host_id) : undefined;

            return (
              <div
                key={w.id}
                className="border border-gray-200 dark:border-gray-700 rounded-xl bg-white dark:bg-gray-800 overflow-hidden"
              >
                {/* Row header */}
                <button
                  onClick={() => toggleExpand(w.id)}
                  className="w-full px-4 py-3 flex items-center gap-3 text-left hover:bg-gray-50 dark:hover:bg-gray-750 transition-colors"
                >
                  <span className={`transition-transform ${isExpanded ? "rotate-90" : ""}`}>
                    &#9654;
                  </span>
                  {statusBadge(w.status)}
                  <span className="text-xs font-medium text-gray-500 dark:text-gray-400 bg-gray-100 dark:bg-gray-700 px-1.5 py-0.5 rounded">
                    {w.kind}
                  </span>
                  {phaseBadge(w.current_phase)}
                  <div className="flex-1 min-w-0">
                    {w.machine_name ? (
                      <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                        {w.machine_name}
                        {w.machine_slug && w.machine_slug !== w.machine_name && (
                          <span className="text-gray-400 ml-1">({w.machine_slug})</span>
                        )}
                      </span>
                    ) : (
                      <span className="text-sm text-gray-500 font-mono">{w.scope_id.slice(0, 8)}</span>
                    )}
                  </div>
                  {host && (
                    <span className="text-xs text-gray-400 dark:text-gray-500">
                      {host.vm_name}
                    </span>
                  )}
                  <span className="text-xs text-gray-400 dark:text-gray-500 font-mono shrink-0" title={w.created_at}>
                    {relativeTime(w.created_at)}
                  </span>
                  <span className="text-xs text-gray-400 dark:text-gray-500 shrink-0 w-16 text-right">
                    {duration(w.started_at, w.completed_at)}
                  </span>
                </button>

                {/* Expanded detail */}
                {isExpanded && (
                  <div className="border-t border-gray-200 dark:border-gray-700 px-4 py-3 bg-gray-50 dark:bg-gray-850">
                    <div className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs mb-3">
                      <div>
                        <span className="text-gray-400">Workflow ID:</span>{" "}
                        <span className="font-mono text-gray-600 dark:text-gray-300">{w.id}</span>
                      </div>
                      <div>
                        <span className="text-gray-400">Machine ID:</span>{" "}
                        <span className="font-mono text-gray-600 dark:text-gray-300">{w.scope_id}</span>
                      </div>
                      <div>
                        <span className="text-gray-400">Started:</span>{" "}
                        <span className="text-gray-600 dark:text-gray-300">
                          {w.started_at ? new Date(w.started_at).toLocaleString() : "-"}
                        </span>
                      </div>
                      <div>
                        <span className="text-gray-400">Completed:</span>{" "}
                        <span className="text-gray-600 dark:text-gray-300">
                          {w.completed_at ? new Date(w.completed_at).toLocaleString() : "-"}
                        </span>
                      </div>
                      <div>
                        <span className="text-gray-400">Priority:</span>{" "}
                        <span className="text-gray-600 dark:text-gray-300">{w.priority}</span>
                      </div>
                      {host && (
                        <div>
                          <span className="text-gray-400">Host:</span>{" "}
                          <span className="text-gray-600 dark:text-gray-300">{host.vm_name} (#{host.id})</span>
                        </div>
                      )}
                    </div>

                    <PhaseTimeline
                      workflow={w}
                      events={events.get(w.id) || []}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
