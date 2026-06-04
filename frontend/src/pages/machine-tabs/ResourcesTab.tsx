import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Activity } from "lucide-react";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { getMachineMetrics, getBrowserVMMetrics } from "../../lib/api";
import type { VMMetricPoint, VMMetricsResponse } from "../../lib/types";

// Both Machine and BrowserVM share the fields ResourcesTab actually uses
// (id, vcpus, memory_mb). Component is kind-agnostic — it just needs to
// know which fetcher to call.
export type ResourcesSubjectKind = "machine" | "browser";

interface ResourcesTabProps {
  /** "machine" for OpenClaw VMs, "browser" for browser VMs. */
  kind: ResourcesSubjectKind;
  vm: { id: string; vcpus: number; memory_mb: number };
  accountId: number;
}

function fetcherFor(kind: ResourcesSubjectKind) {
  return kind === "browser" ? getBrowserVMMetrics : getMachineMetrics;
}

type SubView = "live" | "history";

type RangePreset = "5m" | "1h" | "24h" | "7d";

const PRESETS: { id: RangePreset; label: string; ms: number }[] = [
  { id: "5m", label: "Last 5 m", ms: 5 * 60 * 1000 },
  { id: "1h", label: "Last 1 h", ms: 60 * 60 * 1000 },
  { id: "24h", label: "Last 24 h", ms: 24 * 60 * 60 * 1000 },
  { id: "7d", label: "Last 7 d", ms: 7 * 24 * 60 * 60 * 1000 },
];

const POLL_INTERVAL_MS = 1000;
const STALE_THRESHOLD_MS = 30 * 1000;
const LIVE_BUFFER_MAX = 3000; // ~50 min of 1 Hz; bounded to keep memory predictable

interface ChartPoint {
  ts: number;
  cpu: number | null;
  memMB: number;
}

function pointsToChart(points: VMMetricPoint[]): ChartPoint[] {
  return points.map((p) => ({
    ts: new Date(p.ts).getTime(),
    cpu: p.cpu_pct,
    memMB: p.mem_bytes / (1024 * 1024),
  }));
}

function formatRelative(ms: number): string {
  if (ms < 1000) return "just now";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  return `${h}h ago`;
}

function formatTickRelative(ts: number, now: number): string {
  const delta = now - ts;
  if (delta < 1000) return "now";
  return `-${Math.round(delta / 1000)}s`;
}

function formatTickAbsolute(ts: number): string {
  return new Date(ts).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function ResourcesTab({ kind, vm, accountId }: ResourcesTabProps) {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawView = searchParams.get("view");
  const view: SubView = rawView === "history" ? "history" : "live";

  const setView = (next: SubView) => {
    const params = new URLSearchParams(searchParams);
    params.set("view", next);
    setSearchParams(params, { replace: true });
  };

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-2 border-b border-border pb-3">
        <Activity className="w-4 h-4 text-text-tertiary" />
        <SubTabButton active={view === "live"} onClick={() => setView("live")}>
          Live
        </SubTabButton>
        <SubTabButton active={view === "history"} onClick={() => setView("history")}>
          History
        </SubTabButton>
      </div>

      {view === "live" ? (
        <LiveView kind={kind} vm={vm} accountId={accountId} />
      ) : (
        <HistoryView kind={kind} vm={vm} accountId={accountId} />
      )}
    </div>
  );
}

function SubTabButton({
  active,
  children,
  onClick,
}: {
  active: boolean;
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`px-3 py-1.5 text-sm font-medium rounded-[var(--radius-sm)] transition-colors ${
        active
          ? "bg-brand-600 text-white"
          : "text-text-secondary hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)]"
      }`}
    >
      {children}
    </button>
  );
}

function LiveView({
  kind,
  vm,
  accountId,
}: {
  kind: ResourcesSubjectKind;
  vm: { id: string; vcpus: number; memory_mb: number };
  accountId: number;
}) {
  const [points, setPoints] = useState<VMMetricPoint[]>([]);
  const [reconnecting, setReconnecting] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const cursorRef = useRef<string | undefined>(undefined);

  const tick = useCallback(async () => {
    try {
      const params = cursorRef.current ? { since: cursorRef.current } : {};
      const resp: VMMetricsResponse = await fetcherFor(kind)(accountId, vm.id, params);
      setReconnecting(false);
      setNow(Date.now());
      if (resp.points.length === 0) return;
      const lastTs = resp.points[resp.points.length - 1].ts;
      cursorRef.current = lastTs;
      setPoints((prev) => {
        // First load (no cursor yet) seeds the buffer; subsequent ticks append.
        const merged = prev.length === 0 ? resp.points : [...prev, ...resp.points];
        return merged.length > LIVE_BUFFER_MAX ? merged.slice(-LIVE_BUFFER_MAX) : merged;
      });
    } catch {
      setReconnecting(true);
    }
  }, [kind, accountId, vm.id]);

  useEffect(() => {
    void tick();
    const id = setInterval(() => {
      void tick();
    }, POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, [tick]);

  const chart = pointsToChart(points);
  const last = chart[chart.length - 1];
  const lastTs = last?.ts;
  const isStale = lastTs !== undefined && now - lastTs > STALE_THRESHOLD_MS;
  const cpuMax = vm.vcpus * 100;

  if (points.length === 0) {
    return (
      <div className="space-y-4">
        {reconnecting && <ReconnectingBadge />}
        <div className="bg-card border border-border rounded-[var(--radius-lg)] p-8 text-center">
          <p className="text-sm text-text-secondary">Waiting for first sample…</p>
          <p className="text-xs text-text-muted mt-1">Live samples appear within ~10 s of the VM running.</p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {reconnecting && <ReconnectingBadge />}

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <BigNumberCard
          label="CPU"
          value={last && last.cpu !== null && !isStale ? `${last.cpu.toFixed(0)}%` : "—"}
          denominator={`of ${cpuMax}% allocated`}
          stalled={isStale}
        />
        <BigNumberCard
          label="Memory"
          value={last && !isStale ? `${last.memMB.toFixed(0)} MB` : "—"}
          denominator={`of ${vm.memory_mb.toLocaleString()} MB`}
          stalled={isStale}
        />
      </div>

      <ChartCard title="CPU usage  ·  last 5 minutes">
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={chart} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
            <CartesianGrid stroke="rgba(255,255,255,0.04)" vertical={false} />
            <XAxis
              dataKey="ts"
              type="number"
              domain={["dataMin", "dataMax"]}
              tickFormatter={(v) => formatTickRelative(Number(v), now)}
              stroke="rgba(255,255,255,0.3)"
              fontSize={10}
            />
            <YAxis domain={[0, cpuMax]} stroke="rgba(255,255,255,0.3)" fontSize={10} />
            <Tooltip
              contentStyle={{ background: "rgb(20,20,20)", border: "1px solid rgba(255,255,255,0.1)" }}
              labelFormatter={(v) => new Date(Number(v)).toLocaleTimeString()}
              formatter={(v) => [`${typeof v === "number" ? v.toFixed(1) : "—"}%`, "CPU"]}
            />
            <Line type="monotone" dataKey="cpu" stroke="#f97316" strokeWidth={2} dot={false} isAnimationActive={false} />
          </LineChart>
        </ResponsiveContainer>
      </ChartCard>

      <ChartCard title="Memory usage  ·  last 5 minutes">
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={chart} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
            <CartesianGrid stroke="rgba(255,255,255,0.04)" vertical={false} />
            <XAxis
              dataKey="ts"
              type="number"
              domain={["dataMin", "dataMax"]}
              tickFormatter={(v) => formatTickRelative(Number(v), now)}
              stroke="rgba(255,255,255,0.3)"
              fontSize={10}
            />
            <YAxis domain={[0, vm.memory_mb]} stroke="rgba(255,255,255,0.3)" fontSize={10} />
            <Tooltip
              contentStyle={{ background: "rgb(20,20,20)", border: "1px solid rgba(255,255,255,0.1)" }}
              labelFormatter={(v) => new Date(Number(v)).toLocaleTimeString()}
              formatter={(v) => [`${typeof v === "number" ? v.toFixed(0) : "—"} MB`, "Memory"]}
            />
            <Line type="monotone" dataKey="memMB" stroke="#3b82f6" strokeWidth={2} dot={false} isAnimationActive={false} />
          </LineChart>
        </ResponsiveContainer>
      </ChartCard>

      {lastTs !== undefined && (
        <p className={`text-xs ${isStale ? "text-amber-400" : "text-text-muted"}`}>
          {isStale ? "VM stopped — " : "Updated "}
          {formatRelative(now - lastTs)}
        </p>
      )}
    </div>
  );
}

function HistoryView({
  kind,
  vm,
  accountId,
}: {
  kind: ResourcesSubjectKind;
  vm: { id: string; vcpus: number; memory_mb: number };
  accountId: number;
}) {
  const [preset, setPreset] = useState<RangePreset>("24h");
  const [resp, setResp] = useState<VMMetricsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    const ms = PRESETS.find((p) => p.id === preset)!.ms;
    const to = new Date();
    const from = new Date(to.getTime() - ms);
    fetcherFor(kind)(accountId, vm.id, {
      from: from.toISOString(),
      to: to.toISOString(),
    })
      .then((data) => {
        if (cancelled) return;
        setResp(data);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load metrics");
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [kind, accountId, vm.id, preset]);

  const chart = resp ? pointsToChart(resp.points) : [];
  const cpuMax = vm.vcpus * 100;
  const resolutionLabel =
    resp?.resolution === "1m" ? "1-minute" : resp?.resolution === "mixed" ? "mixed" : "1-second";

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-text-tertiary mr-1">Range:</span>
        {PRESETS.map((p) => (
          <button
            key={p.id}
            type="button"
            onClick={() => setPreset(p.id)}
            className={`px-3 py-1.5 text-sm font-medium rounded-[var(--radius-sm)] transition-colors ${
              preset === p.id
                ? "bg-brand-600 text-white"
                : "text-text-secondary border border-border hover:bg-[rgba(255,255,255,0.04)]"
            }`}
          >
            {p.label}
          </button>
        ))}
        {resp && (
          <span className="ml-auto text-xs text-text-muted">{resolutionLabel} resolution</span>
        )}
      </div>

      {loading && !resp ? (
        <div className="h-48 bg-card border border-border rounded-[var(--radius-lg)] animate-pulse" />
      ) : error ? (
        <p className="text-sm text-red-400">Error: {error}</p>
      ) : chart.length === 0 ? (
        <p className="text-sm text-text-tertiary">No data in this range.</p>
      ) : (
        <>
          <ChartCard title="CPU usage">
            <ResponsiveContainer width="100%" height={220}>
              <LineChart data={chart} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
                <CartesianGrid stroke="rgba(255,255,255,0.04)" vertical={false} />
                <XAxis
                  dataKey="ts"
                  type="number"
                  domain={["dataMin", "dataMax"]}
                  tickFormatter={(v) => formatTickAbsolute(Number(v))}
                  stroke="rgba(255,255,255,0.3)"
                  fontSize={10}
                />
                <YAxis domain={[0, cpuMax]} stroke="rgba(255,255,255,0.3)" fontSize={10} />
                <Tooltip
                  contentStyle={{ background: "rgb(20,20,20)", border: "1px solid rgba(255,255,255,0.1)" }}
                  labelFormatter={(v) => new Date(Number(v)).toLocaleString()}
                  formatter={(v) => [`${typeof v === "number" ? v.toFixed(1) : "—"}%`, "CPU"]}
                />
                <Line type="monotone" dataKey="cpu" stroke="#f97316" strokeWidth={1.5} dot={false} isAnimationActive={false} />
              </LineChart>
            </ResponsiveContainer>
          </ChartCard>

          <ChartCard title="Memory usage">
            <ResponsiveContainer width="100%" height={220}>
              <LineChart data={chart} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
                <CartesianGrid stroke="rgba(255,255,255,0.04)" vertical={false} />
                <XAxis
                  dataKey="ts"
                  type="number"
                  domain={["dataMin", "dataMax"]}
                  tickFormatter={(v) => formatTickAbsolute(Number(v))}
                  stroke="rgba(255,255,255,0.3)"
                  fontSize={10}
                />
                <YAxis domain={[0, vm.memory_mb]} stroke="rgba(255,255,255,0.3)" fontSize={10} />
                <Tooltip
                  contentStyle={{ background: "rgb(20,20,20)", border: "1px solid rgba(255,255,255,0.1)" }}
                  labelFormatter={(v) => new Date(Number(v)).toLocaleString()}
                  formatter={(v) => [`${typeof v === "number" ? v.toFixed(0) : "—"} MB`, "Memory"]}
                />
                <Line type="monotone" dataKey="memMB" stroke="#3b82f6" strokeWidth={1.5} dot={false} isAnimationActive={false} />
              </LineChart>
            </ResponsiveContainer>
          </ChartCard>
        </>
      )}
    </div>
  );
}

function ChartCard({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="bg-card border border-border rounded-[var(--radius-lg)] p-4 md:p-5">
      <h3 className="text-xs font-medium text-text-tertiary mb-3 uppercase tracking-wide">{title}</h3>
      {children}
    </div>
  );
}

function BigNumberCard({
  label,
  value,
  denominator,
  stalled,
}: {
  label: string;
  value: string;
  denominator: string;
  stalled: boolean;
}) {
  return (
    <div className="bg-card border border-border rounded-[var(--radius-lg)] p-4">
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <div className={`text-2xl font-bold tabular-nums ${stalled ? "text-text-muted" : "text-text-primary"}`}>
        {value}
      </div>
      <div className="text-xs text-text-muted mt-0.5">
        {stalled ? "VM stopped" : denominator}
      </div>
    </div>
  );
}

function ReconnectingBadge() {
  return (
    <div className="text-xs text-amber-400 bg-[rgba(245,158,11,0.06)] border border-[rgba(245,158,11,0.2)] rounded-[var(--radius-sm)] px-2 py-1 inline-block">
      Reconnecting…
    </div>
  );
}
