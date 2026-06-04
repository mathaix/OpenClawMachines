import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { listHosts, getHostLogs, type Host } from "../../lib/api";

interface LogEntry {
  raw: string;
  timestamp: string;
  level: "info" | "warn" | "error" | "debug";
  source: string;
  message: string;
  hostId: number;
  hostName: string;
}

function parseLogLine(
  line: string,
  hostId: number,
  hostName: string
): LogEntry | null {
  const trimmed = line.trim();
  if (!trimmed) return null;

  // Try to match "Mon DD HH:MM:SS source: message"
  const match = trimmed.match(
    /^(\w{3}\s+\d+\s+\d+:\d+:\d+)\s+(\S+?):\s+(.*)$/
  );
  const timestamp = match ? match[1] : "";
  const source = match ? match[2] : "";
  const message = match ? match[3] : trimmed;

  let level: LogEntry["level"] = "info";
  const lower = trimmed.toLowerCase();
  if (lower.includes("error") || lower.includes("failed")) level = "error";
  else if (lower.includes("warn") || lower.includes("slow_boot"))
    level = "warn";
  else if (lower.includes("debug")) level = "debug";

  return { raw: trimmed, timestamp, level, source, message, hostId, hostName };
}

const levelStyles = {
  info: {
    active:
      "border-green-500 text-green-600 dark:text-green-400 bg-green-50 dark:bg-green-900/20",
  },
  warn: {
    active:
      "border-yellow-500 text-yellow-600 dark:text-yellow-400 bg-yellow-50 dark:bg-yellow-900/20",
  },
  error: {
    active:
      "border-red-500 text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-900/20",
  },
  debug: {
    active:
      "border-gray-500 text-gray-600 dark:text-gray-400 bg-gray-50 dark:bg-gray-800",
  },
};

function levelTextColor(level: string): string {
  switch (level) {
    case "error":
      return "text-red-400";
    case "warn":
      return "text-yellow-400";
    case "debug":
      return "text-gray-500";
    default:
      return "text-green-400";
  }
}

export function AdminLogs() {
  const [hosts, setHosts] = useState<Host[]>([]);
  const [allEntries, setAllEntries] = useState<LogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState("");
  const [hostFilter, setHostFilter] = useState("all");
  const [activeLevels, setActiveLevels] = useState<Set<string>>(
    new Set(["info", "warn", "error"])
  );
  const [autoRefresh, setAutoRefresh] = useState(true);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchLogs = useCallback(async (hostList: Host[]) => {
    const activeHosts = hostList.filter(
      (h) => h.status === "ready" || h.status === "running"
    );
    if (activeHosts.length === 0) {
      setAllEntries([]);
      setLoading(false);
      return;
    }

    const results = await Promise.allSettled(
      activeHosts.map((h) => getHostLogs(h.id, 100).then((r) => ({ host: h, logs: r.logs })))
    );

    const entries: LogEntry[] = [];
    for (const result of results) {
      if (result.status === "fulfilled") {
        const { host, logs } = result.value;
        const lines = logs.split("\n");
        for (const line of lines) {
          const entry = parseLogLine(line, host.id, host.vm_name);
          if (entry) entries.push(entry);
        }
      }
    }

    setAllEntries(entries);
    setLoading(false);
  }, []);

  // Initial load
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const hostList = await listHosts();
        if (cancelled) return;
        setHosts(hostList);
        await fetchLogs(hostList);
      } catch {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [fetchLogs]);

  // Auto-refresh
  useEffect(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    if (autoRefresh && hosts.length > 0) {
      intervalRef.current = setInterval(() => {
        fetchLogs(hosts);
      }, 5000);
    }
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [autoRefresh, hosts, fetchLogs]);

  const toggleLevel = (level: string) => {
    setActiveLevels((prev) => {
      const next = new Set(prev);
      if (next.has(level)) {
        next.delete(level);
      } else {
        next.add(level);
      }
      return next;
    });
  };

  const filtered = useMemo(
    () =>
      allEntries.filter((e) => {
        if (!activeLevels.has(e.level)) return false;
        if (hostFilter !== "all" && String(e.hostId) !== hostFilter)
          return false;
        if (search && !e.raw.toLowerCase().includes(search.toLowerCase()))
          return false;
        return true;
      }),
    [allEntries, activeLevels, hostFilter, search]
  );

  const counts = useMemo(() => {
    const c = { info: 0, warn: 0, error: 0, debug: 0 };
    for (const e of allEntries) {
      c[e.level]++;
    }
    return c;
  }, [allEntries]);

  const hostCounts = useMemo(() => {
    const m = new Map<number, { name: string; count: number }>();
    for (const e of allEntries) {
      const existing = m.get(e.hostId);
      if (existing) {
        existing.count++;
      } else {
        m.set(e.hostId, { name: e.hostName, count: 1 });
      }
    }
    return Array.from(m.entries());
  }, [allEntries]);

  const activeHosts = hosts.filter(
    (h) => h.status === "ready" || h.status === "running"
  );

  if (loading) {
    return (
      <div className="text-gray-500 dark:text-gray-400">Loading logs...</div>
    );
  }

  if (activeHosts.length === 0) {
    return (
      <div className="text-gray-500 dark:text-gray-400">
        No active hosts to fetch logs from.
      </div>
    );
  }

  return (
    <div>
      {/* Controls bar */}
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <select
          value={hostFilter}
          onChange={(e) => setHostFilter(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"
        >
          <option value="all">All Hosts</option>
          {hosts.map((h) => (
            <option key={h.id} value={h.id}>
              {h.vm_name}
            </option>
          ))}
        </select>

        {(["info", "warn", "error", "debug"] as const).map((level) => (
          <button
            key={level}
            onClick={() => toggleLevel(level)}
            className={`px-3 py-1.5 text-xs font-medium rounded-lg border transition-colors ${
              activeLevels.has(level)
                ? levelStyles[level].active
                : "border-gray-300 dark:border-gray-600 text-gray-400 bg-transparent"
            }`}
          >
            {level.charAt(0).toUpperCase() + level.slice(1)}
          </button>
        ))}

        <input
          placeholder="Search logs..."
          className="px-3 py-1.5 text-xs rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100 placeholder-gray-400 w-48"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />

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
          {filtered.length} lines
        </span>
      </div>

      {/* Main layout — log viewer + sidebar */}
      <div className="flex gap-4">
        <div className="flex-1 min-w-0">
          <div className="bg-gray-900 rounded-xl border border-gray-700 overflow-hidden">
            <div className="overflow-y-auto max-h-[600px] p-3 font-mono text-xs space-y-0">
              {filtered.length === 0 ? (
                <div className="text-gray-500 py-4 text-center">
                  {allEntries.length === 0
                    ? "No log entries found."
                    : "No matching log entries."}
                </div>
              ) : (
                filtered.map((entry, i) => (
                  <div key={i} className="flex gap-3 py-0.5">
                    <span className="text-gray-600 whitespace-nowrap shrink-0">
                      {entry.timestamp}
                    </span>
                    <span
                      className={`w-12 shrink-0 font-semibold ${levelTextColor(entry.level)}`}
                    >
                      {entry.level.toUpperCase().padEnd(5)}
                    </span>
                    <span className="text-gray-500 w-24 shrink-0 truncate">
                      {entry.source}
                    </span>
                    <span className="text-gray-300 break-all">
                      {entry.message}
                    </span>
                  </div>
                ))
              )}
            </div>
          </div>
        </div>

        {/* Sidebar */}
        <div className="w-48 shrink-0 space-y-3">
          <div className="bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-xl p-3">
            <div className="text-xs font-medium text-gray-500 dark:text-gray-400 uppercase mb-2">
              Log Volume
            </div>
            <div className="font-mono text-xl font-semibold text-gray-900 dark:text-gray-100 mb-3">
              {allEntries.length}
            </div>
            <div className="flex items-center gap-2 text-xs mb-1.5">
              <span className="w-2 h-2 rounded-full bg-green-500" />
              <span className="text-gray-400">INFO</span>
              <span className="font-mono text-gray-500 ml-auto">
                {counts.info}
              </span>
            </div>
            <div className="flex items-center gap-2 text-xs mb-1.5">
              <span className="w-2 h-2 rounded-full bg-yellow-500" />
              <span className="text-gray-400">WARN</span>
              <span className="font-mono text-gray-500 ml-auto">
                {counts.warn}
              </span>
            </div>
            <div className="flex items-center gap-2 text-xs mb-1.5">
              <span className="w-2 h-2 rounded-full bg-red-500" />
              <span className="text-gray-400">ERROR</span>
              <span className="font-mono text-gray-500 ml-auto">
                {counts.error}
              </span>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <span className="w-2 h-2 rounded-full bg-gray-500" />
              <span className="text-gray-400">DEBUG</span>
              <span className="font-mono text-gray-500 ml-auto">
                {counts.debug}
              </span>
            </div>
          </div>

          <div className="bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-xl p-3">
            <div className="text-xs font-medium text-gray-500 dark:text-gray-400 uppercase mb-2">
              By Host
            </div>
            {hostCounts.length === 0 ? (
              <div className="text-xs text-gray-500">No data</div>
            ) : (
              hostCounts.map(([id, { name, count }]) => (
                <div
                  key={id}
                  className="flex items-center gap-2 text-xs mb-1.5"
                >
                  <span className="text-gray-400 truncate flex-1">{name}</span>
                  <span className="font-mono text-gray-500">{count}</span>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
