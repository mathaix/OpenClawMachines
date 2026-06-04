import { useCallback, useEffect, useRef, useState } from "react";
import {
  listAdminEvents,
  type ActivityLogEntry,
} from "../../lib/api";

const CATEGORIES = [
  "all",
  "auth",
  "account",
  "machine",
  "config",
  "billing",
  "backup",
  "host",
  "agent",
  "vm",
  "gateway",
] as const;

const STATUSES = ["all", "success", "failure"] as const;

const ACTOR_TYPES = ["all", "user", "system", "agent"] as const;

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

function StatusDot({ status }: { status: string }) {
  return (
    <span
      className={`inline-block w-2 h-2 rounded-full ${
        status === "success"
          ? "bg-green-500"
          : "bg-red-500"
      }`}
      title={status}
    />
  );
}

function categoryBadge(category: string) {
  const styles: Record<string, string> = {
    auth: "bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300",
    account: "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
    machine: "bg-cyan-100 text-cyan-700 dark:bg-cyan-900/40 dark:text-cyan-300",
    config: "bg-yellow-100 text-yellow-700 dark:bg-yellow-900/40 dark:text-yellow-300",
    billing: "bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300",
    backup: "bg-orange-100 text-orange-700 dark:bg-orange-900/40 dark:text-orange-300",
    host: "bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300",
    agent: "bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-300",
    vm: "bg-teal-100 text-teal-700 dark:bg-teal-900/40 dark:text-teal-300",
    gateway: "bg-pink-100 text-pink-700 dark:bg-pink-900/40 dark:text-pink-300",
  };
  return (
    <span
      className={`px-2 py-0.5 text-xs font-medium rounded-full ${
        styles[category] || styles.host
      }`}
    >
      {category}
    </span>
  );
}

function EventRow({
  event,
  expanded,
  onToggle,
}: {
  event: ActivityLogEntry;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <>
      <tr
        onClick={onToggle}
        className="cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-800/50 border-b border-gray-100 dark:border-gray-800"
      >
        <td className="px-3 py-2 text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap">
          <span title={new Date(event.started_at).toLocaleString()}>
            {relativeTime(event.started_at)}
          </span>
        </td>
        <td className="px-3 py-2 text-center">
          <StatusDot status={event.status} />
        </td>
        <td className="px-3 py-2">{categoryBadge(event.category)}</td>
        <td className="px-3 py-2 text-xs font-mono text-gray-600 dark:text-gray-300">
          {event.action}
        </td>
        <td className="px-3 py-2 text-xs text-gray-600 dark:text-gray-300">
          {event.actor_name || event.actor_type}
        </td>
        <td className="px-3 py-2 text-xs text-gray-600 dark:text-gray-300">
          {event.account_name || (event.account_id ? `#${event.account_id}` : "-")}
        </td>
        <td className="px-3 py-2 text-xs text-gray-600 dark:text-gray-300 max-w-[140px] truncate">
          {event.machine_name || event.machine_id || "-"}
        </td>
        <td className="px-3 py-2 text-xs text-gray-600 dark:text-gray-300">
          {event.host_name || (event.host_id ? `#${event.host_id}` : "-")}
        </td>
        <td className="px-3 py-2 text-sm text-gray-700 dark:text-gray-200 max-w-[300px] truncate">
          {event.summary}
        </td>
        <td className="px-3 py-2 text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap">
          {event.duration_ms != null ? `${event.duration_ms}ms` : "-"}
        </td>
      </tr>
      {expanded && (
        <tr className="bg-gray-50 dark:bg-gray-800/30">
          <td colSpan={10} className="px-6 py-3">
            <div className="grid grid-cols-2 gap-4 text-xs">
              <div>
                <p className="text-gray-500 dark:text-gray-400 mb-1 font-medium">
                  Event Details
                </p>
                <dl className="space-y-1">
                  <div className="flex gap-2">
                    <dt className="text-gray-400 dark:text-gray-500">Event ID:</dt>
                    <dd className="font-mono text-gray-600 dark:text-gray-300">
                      {event.event_id}
                    </dd>
                  </div>
                  <div className="flex gap-2">
                    <dt className="text-gray-400 dark:text-gray-500">Time:</dt>
                    <dd className="text-gray-600 dark:text-gray-300">
                      {new Date(event.started_at).toLocaleString()}
                    </dd>
                  </div>
                  {event.agent_version && (
                    <div className="flex gap-2">
                      <dt className="text-gray-400 dark:text-gray-500">Agent:</dt>
                      <dd className="text-gray-600 dark:text-gray-300">
                        {event.agent_version}
                      </dd>
                    </div>
                  )}
                  {event.rootfs_version && (
                    <div className="flex gap-2">
                      <dt className="text-gray-400 dark:text-gray-500">Rootfs:</dt>
                      <dd className="text-gray-600 dark:text-gray-300">
                        {event.rootfs_version}
                      </dd>
                    </div>
                  )}
                </dl>
              </div>
              <div>
                {event.error_message && (
                  <div className="mb-2">
                    <p className="text-red-500 font-medium mb-1">Error</p>
                    <p className="text-red-600 dark:text-red-400 font-mono bg-red-50 dark:bg-red-900/20 px-2 py-1 rounded">
                      {event.error_code && (
                        <span className="font-bold">[{event.error_code}] </span>
                      )}
                      {event.error_message}
                    </p>
                  </div>
                )}
                {event.detail && Object.keys(event.detail).length > 0 && (
                  <div>
                    <p className="text-gray-500 dark:text-gray-400 mb-1 font-medium">
                      Detail
                    </p>
                    <pre className="text-gray-600 dark:text-gray-300 font-mono bg-gray-100 dark:bg-gray-900 px-2 py-1 rounded overflow-x-auto max-h-40">
                      {JSON.stringify(event.detail, null, 2)}
                    </pre>
                  </div>
                )}
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

export function AdminEvents() {
  const [events, setEvents] = useState<ActivityLogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [expandedId, setExpandedId] = useState<number | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(false);
  const intervalRef = useRef<ReturnType<typeof setInterval>>(undefined);

  // Filters
  const [category, setCategory] = useState<string>("all");
  const [status, setStatus] = useState<string>("all");
  const [actorType, setActorType] = useState<string>("all");

  // Pagination
  const [cursorTime, setCursorTime] = useState<string | undefined>();
  const [cursorId, setCursorId] = useState<number | undefined>();
  const [hasMore, setHasMore] = useState(false);

  const fetchEvents = useCallback(
    async (append = false) => {
      try {
        if (!append) setLoading(true);
        const result = await listAdminEvents({
          limit: 50,
          category: category !== "all" ? category : undefined,
          status: status !== "all" ? status : undefined,
          actor_type: actorType !== "all" ? actorType : undefined,
          cursor_time: append ? cursorTime : undefined,
          cursor_id: append ? cursorId : undefined,
        });
        if (append) {
          setEvents((prev) => [...prev, ...result.events]);
        } else {
          setEvents(result.events);
        }
        setCursorTime(result.next_cursor_time ?? undefined);
        setCursorId(result.next_cursor_id ?? undefined);
        setHasMore(!!result.next_cursor_time);
      } catch (e) {
        console.error("Failed to fetch events:", e);
      } finally {
        setLoading(false);
      }
    },
    [category, status, actorType, cursorTime, cursorId]
  );

  // Initial fetch + filter change
  useEffect(() => {
    setCursorTime(undefined);
    setCursorId(undefined);
    setHasMore(false);
    fetchEvents(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [category, status, actorType]);

  // Auto-refresh
  useEffect(() => {
    if (autoRefresh) {
      intervalRef.current = setInterval(() => fetchEvents(false), 30_000);
    }
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, category, status, actorType]);

  const failureCount = events.filter((e) => e.status === "failure").length;

  return (
    <div className="space-y-4">
      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-3">
        <select
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          className="text-sm border border-gray-300 dark:border-gray-600 rounded-md px-2 py-1.5 bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300"
        >
          {CATEGORIES.map((c) => (
            <option key={c} value={c}>
              {c === "all" ? "All categories" : c}
            </option>
          ))}
        </select>

        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          className="text-sm border border-gray-300 dark:border-gray-600 rounded-md px-2 py-1.5 bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300"
        >
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s === "all" ? "All statuses" : s}
            </option>
          ))}
        </select>

        <select
          value={actorType}
          onChange={(e) => setActorType(e.target.value)}
          className="text-sm border border-gray-300 dark:border-gray-600 rounded-md px-2 py-1.5 bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300"
        >
          {ACTOR_TYPES.map((a) => (
            <option key={a} value={a}>
              {a === "all" ? "All actors" : a}
            </option>
          ))}
        </select>

        <button
          onClick={() => fetchEvents(false)}
          className="text-sm px-3 py-1.5 rounded-md border border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-800 text-gray-600 dark:text-gray-300"
        >
          Refresh
        </button>

        <label className="flex items-center gap-1.5 text-sm text-gray-500 dark:text-gray-400 ml-auto">
          <input
            type="checkbox"
            checked={autoRefresh}
            onChange={(e) => setAutoRefresh(e.target.checked)}
            className="rounded"
          />
          Auto-refresh (30s)
        </label>

        {failureCount > 0 && (
          <span className="text-xs text-red-500 font-medium">
            {failureCount} failure{failureCount !== 1 ? "s" : ""}
          </span>
        )}
      </div>

      {/* Events table */}
      <div className="overflow-x-auto border border-gray-200 dark:border-gray-700 rounded-lg">
        <table className="w-full text-left">
          <thead>
            <tr className="bg-gray-50 dark:bg-gray-800 text-xs font-medium text-gray-500 dark:text-gray-400 uppercase tracking-wider">
              <th className="px-3 py-2">Time</th>
              <th className="px-3 py-2 text-center">Status</th>
              <th className="px-3 py-2">Category</th>
              <th className="px-3 py-2">Action</th>
              <th className="px-3 py-2">Actor</th>
              <th className="px-3 py-2">Account</th>
              <th className="px-3 py-2">Machine</th>
              <th className="px-3 py-2">Host</th>
              <th className="px-3 py-2">Summary</th>
              <th className="px-3 py-2">Duration</th>
            </tr>
          </thead>
          <tbody>
            {loading && events.length === 0 ? (
              <tr>
                <td
                  colSpan={10}
                  className="px-3 py-8 text-center text-gray-400 dark:text-gray-500"
                >
                  Loading events...
                </td>
              </tr>
            ) : events.length === 0 ? (
              <tr>
                <td
                  colSpan={10}
                  className="px-3 py-8 text-center text-gray-400 dark:text-gray-500"
                >
                  No events found
                </td>
              </tr>
            ) : (
              events.map((event) => (
                <EventRow
                  key={event.id}
                  event={event}
                  expanded={expandedId === event.id}
                  onToggle={() =>
                    setExpandedId(expandedId === event.id ? null : event.id)
                  }
                />
              ))
            )}
          </tbody>
        </table>
      </div>

      {/* Load more */}
      {hasMore && (
        <div className="text-center">
          <button
            onClick={() => fetchEvents(true)}
            className="text-sm px-4 py-2 rounded-md border border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-800 text-gray-600 dark:text-gray-300"
          >
            Load more
          </button>
        </div>
      )}
    </div>
  );
}
