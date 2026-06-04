import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle,
  Braces,
  ChevronDown,
  Clock3,
  Filter,
  GitBranch,
  RefreshCw,
  Route,
  Search,
  X,
} from "lucide-react";
import { TraceFeedbackPanel } from "../components/TraceFeedbackPanel";
import { TraceTagsPanel, displayTraceTags } from "../components/TraceTagsPanel";
import { getAccountTrace, getAccountTraces, listMachines, type AccountTraceFilters } from "../lib/api";
import { useAuth } from "../lib/auth";
import type { Machine, OpikTraceDetail, OpikTraceListItem, OpikTraceSpan } from "../lib/types";

type TraceRange = "24h" | "7d" | "30d";
type TraceStatus = "" | "ok" | "error";

interface SpanNode {
  span: OpikTraceSpan;
  children: SpanNode[];
}

interface SpanDebugCue {
  key: string;
  label: string;
  span: OpikTraceSpan | null;
  value: string;
  tone: "danger" | "normal" | "muted";
}

const RANGE_OPTIONS: { id: TraceRange; label: string; days: number }[] = [
  { id: "24h", label: "24h", days: 1 },
  { id: "7d", label: "7d", days: 7 },
  { id: "30d", label: "30d", days: 30 },
];

const UNTRUSTED_METADATA_BLOCK_RE =
  /^(?:[^\n]*\(untrusted metadata\):\s*```json\s*\n[\s\S]*?```\s*)+/;
const LEADING_TIMESTAMP_RE = /^\[[\w\s\-:+/]+\]\s*/;
const ERROR_ICON_CLASS = "text-red-200";
const ERROR_PANEL_CLASS = "border border-red-400/50 bg-red-950/60 text-red-50";
const ERROR_PAYLOAD_CLASS = "border-red-400/50 bg-red-950/50 text-red-50";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasPayload(value: unknown): boolean {
  if (value === undefined || value === null) return false;
  if (typeof value === "string") return value.trim().length > 0;
  if (Array.isArray(value)) return value.length > 0;
  if (isRecord(value)) return Object.keys(value).length > 0;
  return true;
}

function prettifyOpenClawPayload(value: unknown, type: "input" | "output"): string | undefined {
  if (!isRecord(value)) return undefined;

  if (
    type === "input" &&
    typeof value.prompt === "string" &&
    typeof value.systemPrompt === "string"
  ) {
    const stripped = value.prompt
      .replace(UNTRUSTED_METADATA_BLOCK_RE, "")
      .replace(LEADING_TIMESTAMP_RE, "")
      .trim();
    return stripped.length > 0 ? stripped : value.prompt;
  }

  if (
    type === "output" &&
    typeof value.output === "string" &&
    isRecord(value.lastAssistant)
  ) {
    return value.output;
  }

  return undefined;
}

function stringifyPayload(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function truncateText(value: string, max = 10_000): string {
  if (value.length <= max) return value;
  return `${value.slice(0, max)}\n[TRUNCATED IN VIEW]`;
}

function sinceForRange(range: TraceRange): string {
  const option = RANGE_OPTIONS.find((item) => item.id === range) ?? RANGE_OPTIONS[1];
  const date = new Date();
  date.setUTCDate(date.getUTCDate() - option.days);
  return date.toISOString();
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatDurationMS(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "--";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.round(seconds % 60)}s`;
}

function formatDuration(startIso: string, endIso?: string): string {
  if (!endIso) return "running";
  return formatDurationMS(new Date(endIso).getTime() - new Date(startIso).getTime());
}

function formatTokens(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(2)}M`;
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(1)}K`;
  return tokens.toLocaleString();
}

function formatFeedbackScore(value?: number): string {
  if (value === undefined || value === null || !Number.isFinite(value)) return "unreviewed";
  return `${Math.round(value * 100)}% score`;
}

function shortId(id: string): string {
  return id.length > 12 ? id.slice(0, 8) : id;
}

function traceTitle(trace: OpikTraceListItem): string {
  return trace.name || trace.thread_id || shortId(trace.id);
}

function spanTitle(span: OpikTraceSpan): string {
  return span.name || span.type || shortId(span.id);
}

function spanDurationMS(span: OpikTraceSpan): number {
  const start = new Date(span.start_time).getTime();
  const end = span.end_time ? new Date(span.end_time).getTime() : Date.now();
  const ms = end - start;
  return Number.isFinite(ms) && ms > 0 ? ms : 0;
}

function maxSpanBy(spans: OpikTraceSpan[], value: (span: OpikTraceSpan) => number): OpikTraceSpan | null {
  let best: OpikTraceSpan | null = null;
  let bestValue = 0;
  for (const span of spans) {
    const nextValue = value(span);
    if (!best || nextValue > bestValue) {
      best = span;
      bestValue = nextValue;
    }
  }
  return best;
}

function buildSpanDebugCues(spans: OpikTraceSpan[]): SpanDebugCue[] {
  if (spans.length === 0) return [];

  const firstError = spans.find((span) => hasPayload(span.error_info)) ?? null;
  const slowest = maxSpanBy(spans, spanDurationMS);
  const mostTokens = maxSpanBy(spans, (span) => span.total_tokens);

  return [
    {
      key: "error",
      label: firstError ? "First error" : "Span errors",
      span: firstError,
      value: firstError ? spanTitle(firstError) : "None",
      tone: firstError ? "danger" : "muted",
    },
    {
      key: "slow",
      label: "Slowest span",
      span: slowest,
      value: slowest ? `${spanTitle(slowest)} · ${formatDurationMS(spanDurationMS(slowest))}` : "--",
      tone: "normal",
    },
    {
      key: "tokens",
      label: "Most tokens",
      span: mostTokens,
      value: mostTokens ? `${spanTitle(mostTokens)} · ${formatTokens(mostTokens.total_tokens)}` : "--",
      tone: "normal",
    },
  ];
}

function parseOptionalNumber(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined;
}

function parseTags(raw: string): string[] {
  const seen = new Set<string>();
  return raw
    .split(",")
    .map((tag) => tag.trim().toLowerCase().replace(/\s+/g, "-"))
    .filter((tag) => {
      if (!tag || seen.has(tag)) return false;
      seen.add(tag);
      return true;
    });
}

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const index = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1);
  return sorted[Math.max(index, 0)];
}

function buildSpanTree(spans: OpikTraceSpan[]): SpanNode[] {
  const nodes = new Map<string, SpanNode>();
  for (const span of spans) {
    nodes.set(span.id, { span, children: [] });
  }

  const roots: SpanNode[] = [];
  for (const span of spans) {
    const node = nodes.get(span.id);
    if (!node) continue;
    const parent = span.parent_span_id ? nodes.get(span.parent_span_id) : undefined;
    if (parent) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }

  return roots;
}

export function Observability() {
  const { account } = useAuth();
  const [machines, setMachines] = useState<Machine[]>([]);
  const [range, setRange] = useState<TraceRange>("7d");
  const [query, setQuery] = useState("");
  const [machineId, setMachineId] = useState("");
  const [status, setStatus] = useState<TraceStatus>("");
  const [feedbackFilter, setFeedbackFilter] = useState<"" | "reviewed" | "unreviewed" | "low">("");
  const [project, setProject] = useState("");
  const [threadId, setThreadId] = useState("");
  const [tags, setTags] = useState("");
  const [minDuration, setMinDuration] = useState("");
  const [minTokens, setMinTokens] = useState("");
  const [traces, setTraces] = useState<OpikTraceListItem[]>([]);
  const [selectedTraceId, setSelectedTraceId] = useState<string | null>(null);
  const [detail, setDetail] = useState<OpikTraceDetail | null>(null);
  const [selectedSpanId, setSelectedSpanId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const traceRequestRef = useRef(0);

  const machineNameById = useMemo(() => {
    const map = new Map<string, string>();
    for (const machine of machines) map.set(machine.id, machine.name);
    return map;
  }, [machines]);

  const loadMachines = useCallback(async () => {
    if (!account) return;
    try {
      const nextMachines = await listMachines(account.id);
      setMachines(nextMachines ?? []);
    } catch {
      setMachines([]);
    }
  }, [account]);

  const loadTraces = useCallback(async () => {
    if (!account) return;
    const requestID = traceRequestRef.current + 1;
    traceRequestRef.current = requestID;
    setLoading(true);
    setError(null);

    const minDurationSeconds = parseOptionalNumber(minDuration);
    const filters: AccountTraceFilters = {
      since: sinceForRange(range),
      limit: 100,
      q: query.trim() || undefined,
      machine_id: machineId || undefined,
      project: project.trim() || undefined,
      status,
      feedback: feedbackFilter,
      max_feedback_score: feedbackFilter === "low" ? 0.5 : undefined,
      thread_id: threadId.trim() || undefined,
      tags: parseTags(tags),
      min_tokens: parseOptionalNumber(minTokens),
      min_duration_ms: minDurationSeconds === undefined ? undefined : Math.round(minDurationSeconds * 1000),
    };

    try {
      const response = await getAccountTraces(account.id, filters);
      if (requestID !== traceRequestRef.current) return;
      const nextTraces = response.traces ?? [];
      setTraces(nextTraces);
      setSelectedTraceId((current) => {
        if (current && nextTraces.some((trace) => trace.id === current)) return current;
        return nextTraces[0]?.id ?? null;
      });
    } catch (err) {
      if (requestID !== traceRequestRef.current) return;
      setError(err instanceof Error ? err.message : "Failed to load traces");
      setTraces([]);
      setSelectedTraceId(null);
    } finally {
      if (requestID === traceRequestRef.current) setLoading(false);
    }
  }, [account, feedbackFilter, machineId, minDuration, minTokens, project, query, range, status, tags, threadId]);

  useEffect(() => {
    loadMachines();
  }, [loadMachines]);

  useEffect(() => {
    loadTraces();
  }, [loadTraces]);

  const loadTraceDetail = useCallback(async (traceId: string, preserveSelectedSpan = false) => {
    if (!account) return;
    setDetailLoading(true);
    setDetailError(null);
    try {
      const nextDetail = await getAccountTrace(account.id, traceId);
      setDetail(nextDetail);
      setSelectedSpanId((current) => {
        if (preserveSelectedSpan && current && nextDetail.spans?.some((span) => span.id === current)) {
          return current;
        }
        return nextDetail.spans?.[0]?.id ?? null;
      });
    } catch (err) {
      setDetail(null);
      setSelectedSpanId(null);
      setDetailError(err instanceof Error ? err.message : "Failed to load trace detail");
    } finally {
      setDetailLoading(false);
    }
  }, [account]);

  useEffect(() => {
    if (!account || !selectedTraceId) {
      setDetail(null);
      setSelectedSpanId(null);
      setDetailError(null);
      return;
    }

    let cancelled = false;
    const traceId = selectedTraceId;
    setDetailLoading(true);
    setDetailError(null);
    getAccountTrace(account.id, traceId)
      .then((nextDetail) => {
        if (!cancelled) {
          setDetail(nextDetail);
          setSelectedSpanId(nextDetail.spans?.[0]?.id ?? null);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setDetail(null);
          setSelectedSpanId(null);
          setDetailError(err instanceof Error ? err.message : "Failed to load trace detail");
        }
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [account, selectedTraceId]);

  const selectedDetail = detail?.trace.id === selectedTraceId ? detail : null;
  const selectedTrace = selectedDetail?.trace ?? traces.find((trace) => trace.id === selectedTraceId) ?? null;
  const spanTree = useMemo(() => buildSpanTree(selectedDetail?.spans ?? []), [selectedDetail?.spans]);
  const selectedSpan = useMemo(
    () => selectedDetail?.spans.find((span) => span.id === selectedSpanId) ?? selectedDetail?.spans[0] ?? null,
    [selectedDetail?.spans, selectedSpanId],
  );

  const stats = useMemo(() => {
    const durationValues = traces.map((trace) => trace.duration_ms).filter((value) => value > 0);
    return {
      traces: traces.length,
      spans: traces.reduce((sum, trace) => sum + trace.span_count, 0),
      errors: traces.reduce((sum, trace) => sum + trace.error_count, 0),
      tokens: traces.reduce((sum, trace) => sum + trace.total_tokens, 0),
      p95: percentile(durationValues, 95),
    };
  }, [traces]);

  const clearFilters = () => {
    setQuery("");
    setMachineId("");
    setStatus("");
    setFeedbackFilter("");
    setProject("");
    setThreadId("");
    setTags("");
    setMinDuration("");
    setMinTokens("");
  };

  if (!account) {
    return (
      <div className="max-w-6xl mx-auto">
        <div className="h-8 w-48 bg-[rgba(255,255,255,0.05)] rounded-[var(--radius-sm)] animate-pulse" />
      </div>
    );
  }

  return (
    <div className="max-w-6xl mx-auto space-y-5">
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl md:text-3xl font-bold text-text-primary">Observability</h1>
          <p className="text-sm text-text-tertiary mt-1">Account-wide traces, spans, payloads, and performance signals.</p>
        </div>
        <button
          onClick={loadTraces}
          title="Refresh traces"
          className="inline-flex items-center justify-center gap-2 h-10 px-3 border border-border rounded-[var(--radius-sm)] text-sm font-medium text-text-secondary hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)] transition-colors"
        >
          <RefreshCw className={`w-4 h-4 ${loading ? "animate-spin" : ""}`} />
          Refresh
        </button>
      </div>

      <section className="bg-card border border-border rounded-[var(--radius-lg)] p-4 space-y-4">
        <div className="flex items-center gap-2 text-sm font-semibold text-text-primary">
          <Filter className="w-4 h-4 text-text-tertiary" />
          Filters
        </div>
        <div className="grid md:grid-cols-[minmax(0,1.6fr)_150px_150px_150px_120px] gap-3">
          <label className="block">
            <span className="text-xs text-text-muted">Search</span>
            <div className="mt-1 flex items-center gap-2 border border-border rounded-[var(--radius-sm)] px-3 h-10 bg-[rgba(0,0,0,0.12)]">
              <Search className="w-4 h-4 text-text-muted shrink-0" />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Trace, span, payload, tag"
                className="w-full bg-transparent outline-none text-sm text-text-primary placeholder:text-text-muted"
              />
            </div>
          </label>
          <label className="block">
            <span className="text-xs text-text-muted">Machine</span>
            <select
              value={machineId}
              onChange={(event) => setMachineId(event.target.value)}
              className="mt-1 h-10 w-full border border-border rounded-[var(--radius-sm)] bg-card px-3 text-sm text-text-primary"
            >
              <option value="">All machines</option>
              {machines.map((machine) => (
                <option key={machine.id} value={machine.id}>
                  {machine.name}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <span className="text-xs text-text-muted">Status</span>
            <select
              value={status}
              onChange={(event) => setStatus(event.target.value as TraceStatus)}
              className="mt-1 h-10 w-full border border-border rounded-[var(--radius-sm)] bg-card px-3 text-sm text-text-primary"
            >
              <option value="">Any status</option>
              <option value="ok">OK</option>
              <option value="error">Error</option>
            </select>
          </label>
          <label className="block">
            <span className="text-xs text-text-muted">Range</span>
            <select
              value={range}
              onChange={(event) => setRange(event.target.value as TraceRange)}
              className="mt-1 h-10 w-full border border-border rounded-[var(--radius-sm)] bg-card px-3 text-sm text-text-primary"
            >
              {RANGE_OPTIONS.map((option) => (
                <option key={option.id} value={option.id}>
                  {option.label}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <span className="text-xs text-text-muted">Review</span>
            <select
              value={feedbackFilter}
              onChange={(event) => setFeedbackFilter(event.target.value as "" | "reviewed" | "unreviewed" | "low")}
              className="mt-1 h-10 w-full border border-border rounded-[var(--radius-sm)] bg-card px-3 text-sm text-text-primary"
            >
              <option value="">Any</option>
              <option value="unreviewed">Unreviewed</option>
              <option value="reviewed">Reviewed</option>
              <option value="low">Low score</option>
            </select>
          </label>
        </div>
        <div className="grid md:grid-cols-5 gap-3">
          <FilterInput label="Project" value={project} onChange={setProject} placeholder="default" />
          <FilterInput label="Thread" value={threadId} onChange={setThreadId} placeholder="thread id" />
          <FilterInput label="Tags" value={tags} onChange={setTags} placeholder="eval,prod" />
          <FilterInput label="Min seconds" value={minDuration} onChange={setMinDuration} placeholder="2" type="number" />
          <FilterInput label="Min tokens" value={minTokens} onChange={setMinTokens} placeholder="1000" type="number" />
        </div>
        <button
          onClick={clearFilters}
          className="inline-flex items-center gap-2 text-xs text-text-tertiary hover:text-text-primary transition-colors"
        >
          <X className="w-3.5 h-3.5" />
          Clear filters
        </button>
      </section>

      {error && (
        <div className={`${ERROR_PANEL_CLASS} rounded-[var(--radius-md)] px-4 py-3 text-sm`}>
          {error}
        </div>
      )}

      <section className="grid grid-cols-2 md:grid-cols-5 gap-3">
        <Metric label="Traces" value={stats.traces.toLocaleString()} />
        <Metric label="Spans" value={stats.spans.toLocaleString()} />
        <Metric label="Errors" value={stats.errors.toLocaleString()} danger={stats.errors > 0} />
        <Metric label="P95 latency" value={formatDurationMS(stats.p95)} />
        <Metric label="Tokens" value={formatTokens(stats.tokens)} />
      </section>

      <div className="grid lg:grid-cols-[minmax(300px,420px)_minmax(0,1fr)] gap-4">
        <TraceList
          traces={traces}
          loading={loading}
          selectedTraceId={selectedTraceId}
          machineNameById={machineNameById}
          onSelect={setSelectedTraceId}
        />
        <TraceDetail
          trace={selectedTrace}
          detail={selectedDetail}
          spanTree={spanTree}
          selectedSpan={selectedSpan}
          selectedSpanId={selectedSpanId}
          loading={detailLoading}
          error={detailError}
          onSelectSpan={setSelectedSpanId}
          accountId={account.id}
          onTagsSaved={(traceId, nextTags) => {
            setDetail((current) => current && current.trace.id === traceId
              ? { ...current, trace: { ...current.trace, tags: nextTags } }
              : current);
            setTraces((current) => current.map((trace) => (trace.id === traceId ? { ...trace, tags: nextTags } : trace)));
          }}
          onFeedbackCreated={async () => {
            if (selectedTraceId) {
              await loadTraceDetail(selectedTraceId, true);
              await loadTraces();
            }
          }}
        />
      </div>
    </div>
  );
}

function FilterInput({
  label,
  value,
  onChange,
  placeholder,
  type = "text",
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  type?: "text" | "number";
}) {
  return (
    <label className="block">
      <span className="text-xs text-text-muted">{label}</span>
      <input
        value={value}
        type={type}
        min={type === "number" ? "0" : undefined}
        step={type === "number" ? "any" : undefined}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className="mt-1 h-10 w-full border border-border rounded-[var(--radius-sm)] bg-[rgba(0,0,0,0.12)] px-3 text-sm text-text-primary placeholder:text-text-muted outline-none"
      />
    </label>
  );
}

function Metric({ label, value, danger = false }: { label: string; value: string; danger?: boolean }) {
  return (
    <div className="bg-card border border-border rounded-[var(--radius-md)] px-3 py-3">
      <p className="text-[11px] uppercase text-text-muted">{label}</p>
      <p className={`text-lg font-semibold mt-0.5 truncate ${danger ? "text-red-100" : "text-text-primary"}`}>{value}</p>
    </div>
  );
}

function TraceList({
  traces,
  loading,
  selectedTraceId,
  machineNameById,
  onSelect,
}: {
  traces: OpikTraceListItem[];
  loading: boolean;
  selectedTraceId: string | null;
  machineNameById: Map<string, string>;
  onSelect: (traceID: string) => void;
}) {
  return (
    <section className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden">
      <div className="px-4 py-3 border-b border-border flex items-center justify-between">
        <span className="text-sm font-semibold text-text-primary">Traces</span>
        <span className="text-xs text-text-muted">{traces.length}</span>
      </div>
      {loading ? (
        <div className="p-4 space-y-3">
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="h-20 bg-[rgba(255,255,255,0.04)] rounded-[var(--radius-sm)] animate-pulse" />
          ))}
        </div>
      ) : traces.length === 0 ? (
        <div className="p-6 text-sm text-text-tertiary">No traces match the current filters.</div>
      ) : (
        <div className="divide-y divide-border-subtle max-h-[760px] overflow-y-auto">
          {traces.map((trace) => {
            const machineName = trace.machine_name || (trace.machine_id ? machineNameById.get(trace.machine_id) : "") || trace.machine_id || "unknown";
            const hasError = trace.error_count > 0 || hasPayload(trace.error_info);
            const tags = displayTraceTags(trace.tags);
            return (
              <button
                key={trace.id}
                onClick={() => onSelect(trace.id)}
                className={`w-full text-left px-4 py-3 transition-colors ${
                  selectedTraceId === trace.id
                    ? "bg-[rgba(249,115,22,0.08)]"
                    : "hover:bg-[rgba(255,255,255,0.03)]"
                }`}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      {hasError && <AlertTriangle className={`w-3.5 h-3.5 ${ERROR_ICON_CLASS} shrink-0`} />}
                      <p className="text-sm font-medium text-text-primary truncate">{traceTitle(trace)}</p>
                    </div>
                    <p className="text-xs text-text-muted font-mono truncate mt-0.5">{shortId(trace.id)}</p>
                  </div>
                  <span className="text-xs text-text-tertiary shrink-0">{formatDate(trace.start_time)}</span>
                </div>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-2 text-xs text-text-tertiary">
                  <span>{machineName}</span>
                  <span>{trace.span_count} spans</span>
                  <span>{formatDurationMS(trace.duration_ms)}</span>
                  <span>{formatTokens(trace.total_tokens)} tokens</span>
                  <span className={trace.feedback_count > 0 && (trace.avg_feedback_score ?? 1) < 0.5 ? "text-red-100" : ""}>
                    {formatFeedbackScore(trace.avg_feedback_score)}
                  </span>
                </div>
                {tags.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1">
                    {tags.slice(0, 4).map((tag) => (
                      <span key={tag} className="text-[11px] px-1.5 py-0.5 rounded bg-[rgba(255,255,255,0.06)] text-text-tertiary">
                        {tag}
                      </span>
                    ))}
                  </div>
                )}
              </button>
            );
          })}
        </div>
      )}
    </section>
  );
}

function TraceDetail({
  accountId,
  trace,
  detail,
  spanTree,
  selectedSpan,
  selectedSpanId,
  loading,
  error,
  onSelectSpan,
  onTagsSaved,
  onFeedbackCreated,
}: {
  accountId: number;
  trace: OpikTraceListItem | null;
  detail: OpikTraceDetail | null;
  spanTree: SpanNode[];
  selectedSpan: OpikTraceSpan | null;
  selectedSpanId: string | null;
  loading: boolean;
  error: string | null;
  onSelectSpan: (spanID: string) => void;
  onTagsSaved: (traceId: string, tags: string[]) => Promise<void> | void;
  onFeedbackCreated: () => Promise<void> | void;
}) {
  if (!trace) {
    return (
      <section className="bg-card border border-border rounded-[var(--radius-lg)] min-h-[520px] flex items-center justify-center p-6 text-sm text-text-tertiary">
        Select a trace to inspect its execution tree.
      </section>
    );
  }

  return (
    <section className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden min-h-[520px]">
      <div className="px-4 md:px-5 py-4 border-b border-border">
        <div className="flex flex-col md:flex-row md:items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <Route className="w-4 h-4 text-brand-500 shrink-0" />
              <h2 className="text-base font-semibold text-text-primary truncate">{traceTitle(trace)}</h2>
            </div>
            <p className="text-xs text-text-muted font-mono mt-1 truncate">{trace.id}</p>
          </div>
          <div className="flex items-center gap-2 text-xs text-text-tertiary shrink-0">
            {loading && <RefreshCw className="w-3.5 h-3.5 animate-spin" />}
            <Clock3 className="w-3.5 h-3.5" />
            <span>{formatDuration(trace.start_time, trace.end_time)}</span>
          </div>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mt-4">
          <TinyMetric label="Machine" value={trace.machine_name || trace.machine_id || "unknown"} />
          <TinyMetric label="Project" value={trace.project_name || trace.project_id || "default"} />
          <TinyMetric label="Spans" value={trace.span_count.toLocaleString()} />
          <TinyMetric label="Tokens" value={formatTokens(trace.total_tokens)} />
        </div>
      </div>

      <div className="p-4 md:p-5 space-y-5">
        {error && (
          <p className={`${ERROR_PANEL_CLASS} text-sm rounded-[var(--radius-md)] p-3`}>
            {error}
          </p>
        )}

        <DebugCues
          spans={detail?.spans ?? []}
          selectedSpanId={selectedSpanId}
          loading={loading}
          onSelectSpan={onSelectSpan}
        />

        <TraceTagsPanel
          accountId={accountId}
          trace={trace}
          onSaved={onTagsSaved}
        />

        <TraceFeedbackPanel
          accountId={accountId}
          traceId={trace.id}
          selectedSpan={selectedSpan}
          feedback={detail?.feedback ?? []}
          onCreated={onFeedbackCreated}
        />

        <div className="grid xl:grid-cols-[minmax(240px,340px)_minmax(0,1fr)] gap-4">
          <section>
            <div className="flex items-center gap-2 mb-2">
              <GitBranch className="w-4 h-4 text-text-tertiary" />
              <h3 className="text-sm font-semibold text-text-primary">Execution tree</h3>
            </div>
            {loading ? (
              <div className="h-40 bg-[rgba(255,255,255,0.04)] rounded-[var(--radius-md)] animate-pulse" />
            ) : detail && detail.spans.length === 0 ? (
              <p className="text-sm text-text-tertiary border border-border rounded-[var(--radius-md)] p-3">No spans recorded for this trace.</p>
            ) : (
              <div className="border border-border rounded-[var(--radius-md)] overflow-hidden">
                {spanTree.map((node) => (
                  <SpanTreeRow
                    key={node.span.id}
                    node={node}
                    depth={0}
                    selectedSpanId={selectedSpanId}
                    onSelect={onSelectSpan}
                  />
                ))}
              </div>
            )}
          </section>

          <section className="min-w-0">
            <div className="flex items-center gap-2 mb-2">
              <Braces className="w-4 h-4 text-text-tertiary" />
              <h3 className="text-sm font-semibold text-text-primary">Trace payload</h3>
            </div>
            <div className="space-y-3">
              <PayloadSection title="Input" value={trace.input} type="input" />
              <PayloadSection title="Output" value={trace.output} type="output" />
              <PayloadSection title="Metadata" value={trace.metadata} type="metadata" />
              <PayloadSection title="Error" value={trace.error_info} type="metadata" danger />
            </div>
          </section>
        </div>

        <SpanInspector span={selectedSpan} />
      </div>
    </section>
  );
}

function TinyMetric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-[11px] uppercase text-text-muted">{label}</p>
      <p className="text-sm font-semibold text-text-primary truncate mt-0.5">{value}</p>
    </div>
  );
}

function DebugCues({
  spans,
  selectedSpanId,
  loading,
  onSelectSpan,
}: {
  spans: OpikTraceSpan[];
  selectedSpanId: string | null;
  loading: boolean;
  onSelectSpan: (spanID: string) => void;
}) {
  const cues = buildSpanDebugCues(spans);
  if (loading) {
    return <div className="h-20 bg-[rgba(255,255,255,0.04)] rounded-[var(--radius-md)] animate-pulse" />;
  }
  if (cues.length === 0) return null;

  return (
    <section>
      <div className="flex items-center gap-2 mb-2">
        <AlertTriangle className="w-4 h-4 text-text-tertiary" />
        <h3 className="text-sm font-semibold text-text-primary">Debug cues</h3>
      </div>
      <div className="grid md:grid-cols-3 gap-3">
        {cues.map((cue) => {
          const selected = cue.span?.id === selectedSpanId;
          const selectable = Boolean(cue.span);
          const toneClass = cue.tone === "danger"
            ? "border-red-400/50 bg-red-950/40"
            : selected
              ? "border-brand-500/50 bg-[rgba(249,115,22,0.08)]"
              : "border-border bg-[rgba(255,255,255,0.02)]";
          return (
            <button
              key={cue.key}
              type="button"
              disabled={!selectable}
              onClick={() => cue.span && onSelectSpan(cue.span.id)}
              className={`text-left rounded-[var(--radius-md)] border p-3 transition-colors ${toneClass} ${
                selectable ? "hover:bg-[rgba(255,255,255,0.05)]" : "cursor-default"
              }`}
            >
              <p className="text-[11px] uppercase text-text-muted">{cue.label}</p>
              <p className={`text-sm font-semibold mt-1 truncate ${cue.tone === "danger" ? "text-red-50" : "text-text-primary"}`}>
                {cue.value}
              </p>
              {cue.span && (
                <p className="text-xs text-text-muted font-mono mt-1 truncate">{shortId(cue.span.id)}</p>
              )}
            </button>
          );
        })}
      </div>
    </section>
  );
}

function SpanTreeRow({
  node,
  depth,
  selectedSpanId,
  onSelect,
}: {
  node: SpanNode;
  depth: number;
  selectedSpanId: string | null;
  onSelect: (spanID: string) => void;
}) {
  const span = node.span;
  const selected = span.id === selectedSpanId;
  const hasError = hasPayload(span.error_info);

  return (
    <div>
      <button
        onClick={() => onSelect(span.id)}
        className={`w-full text-left px-3 py-2 border-b border-border-subtle last:border-b-0 transition-colors ${
          selected ? "bg-[rgba(249,115,22,0.08)]" : "hover:bg-[rgba(255,255,255,0.03)]"
        }`}
        style={{ paddingLeft: `${12 + depth * 16}px` }}
      >
        <div className="flex items-center justify-between gap-2">
          <div className="min-w-0 flex items-center gap-2">
            {node.children.length > 0 ? (
              <ChevronDown className="w-3.5 h-3.5 text-text-tertiary shrink-0" />
            ) : (
              <span className="w-3.5 h-3.5 shrink-0" />
            )}
            {hasError && <AlertTriangle className={`w-3.5 h-3.5 ${ERROR_ICON_CLASS} shrink-0`} />}
            <span className="text-sm text-text-primary truncate">{spanTitle(span)}</span>
          </div>
          <span className="text-xs text-text-tertiary shrink-0">{formatDuration(span.start_time, span.end_time)}</span>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-text-muted">
          <span>{span.type || "span"}</span>
          <span>{span.machine_name || shortId(span.machine_id)}</span>
          <span>{formatTokens(span.total_tokens)}</span>
        </div>
      </button>
      {node.children.map((child) => (
        <SpanTreeRow
          key={child.span.id}
          node={child}
          depth={depth + 1}
          selectedSpanId={selectedSpanId}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}

function SpanInspector({ span }: { span: OpikTraceSpan | null }) {
  if (!span) {
    return (
      <section>
        <div className="flex items-center gap-2 mb-2">
          <Route className="w-4 h-4 text-text-tertiary" />
          <h3 className="text-sm font-semibold text-text-primary">Span</h3>
        </div>
        <p className="text-sm text-text-tertiary border border-border rounded-[var(--radius-md)] p-3">Select a span to inspect its payload.</p>
      </section>
    );
  }

  const hasDetails =
    hasPayload(span.input) ||
    hasPayload(span.output) ||
    hasPayload(span.metadata) ||
    hasPayload(span.usage) ||
    hasPayload(span.error_info);

  return (
    <section>
      <div className="flex items-center gap-2 mb-2">
        <Route className="w-4 h-4 text-text-tertiary" />
        <h3 className="text-sm font-semibold text-text-primary">Span</h3>
      </div>
      <div className="border border-border rounded-[var(--radius-md)] p-3 space-y-3">
        <div className="flex flex-col md:flex-row md:items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="text-sm font-semibold text-text-primary truncate">{spanTitle(span)}</p>
            <p className="text-xs text-text-muted font-mono truncate mt-0.5">{span.id}</p>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-xs text-text-tertiary">
            <span>{span.type || "span"}</span>
            <span>{formatDuration(span.start_time, span.end_time)}</span>
          </div>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <TinyMetric label="Machine" value={span.machine_name || span.machine_id} />
          <TinyMetric label="Provider" value={span.provider || "--"} />
          <TinyMetric label="Model" value={span.model || "--"} />
          <TinyMetric label="Tokens" value={formatTokens(span.total_tokens)} />
        </div>
        {hasDetails ? (
          <div className="space-y-3">
            <PayloadSection title="Input" value={span.input} type="input" />
            <PayloadSection title="Output" value={span.output} type="output" />
            <PayloadSection title="Metadata" value={span.metadata} type="metadata" />
            <PayloadSection title="Usage" value={span.usage} type="metadata" />
            <PayloadSection title="Error" value={span.error_info} type="metadata" danger />
          </div>
        ) : (
          <p className="text-sm text-text-tertiary border border-border rounded-[var(--radius-md)] p-3">
            No payload data recorded for this span.
          </p>
        )}
      </div>
    </section>
  );
}

function PayloadSection({
  title,
  value,
  type,
  danger = false,
}: {
  title: string;
  value: unknown;
  type: "input" | "output" | "metadata";
  danger?: boolean;
}) {
  if (!hasPayload(value)) return null;

  const pretty = type === "metadata" ? undefined : prettifyOpenClawPayload(value, type);
  const raw = stringifyPayload(value);
  const display = truncateText(pretty ?? raw);

  return (
    <section>
      <div className="flex items-center gap-2 mb-1.5">
        <Braces className={`w-4 h-4 ${danger ? ERROR_ICON_CLASS : "text-text-tertiary"}`} />
        <h4 className="text-sm font-semibold text-text-primary">{title}</h4>
      </div>
      <pre className={`text-xs leading-5 rounded-[var(--radius-md)] border p-3 overflow-x-auto whitespace-pre-wrap break-words ${
        danger
          ? ERROR_PAYLOAD_CLASS
          : "border-border bg-[rgba(0,0,0,0.18)] text-text-secondary"
      }`}>
        {display}
      </pre>
      {pretty && raw !== pretty && (
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-text-tertiary hover:text-text-secondary">Raw payload</summary>
          <pre className="mt-2 text-xs leading-5 rounded-[var(--radius-md)] border border-border bg-[rgba(0,0,0,0.18)] text-text-secondary p-3 overflow-x-auto whitespace-pre-wrap break-words">
            {truncateText(raw)}
          </pre>
        </details>
      )}
    </section>
  );
}
