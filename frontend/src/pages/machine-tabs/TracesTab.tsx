import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Braces, ChevronDown, Clock3, GitBranch, RefreshCw, Route } from "lucide-react";
import { TraceFeedbackPanel } from "../../components/TraceFeedbackPanel";
import { TraceTagsPanel } from "../../components/TraceTagsPanel";
import type { Machine, OpikTraceDetail, OpikTraceListItem, OpikTraceSpan } from "../../lib/types";
import { getMachineTrace, getMachineTraces } from "../../lib/api";

interface TracesTabProps {
  machine: Machine;
  accountId: number;
}

interface SpanDebugCue {
  key: string;
  label: string;
  span: OpikTraceSpan | null;
  value: string;
  tone: "danger" | "normal" | "muted";
}

type TraceRange = "24h" | "7d" | "30d";

const RANGE_OPTIONS: { id: TraceRange; label: string }[] = [
  { id: "24h", label: "24h" },
  { id: "7d", label: "7d" },
  { id: "30d", label: "30d" },
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

function isString(value: unknown): value is string {
  return typeof value === "string";
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
    isString(value.prompt) &&
    isString(value.systemPrompt)
  ) {
    const stripped = value.prompt
      .replace(UNTRUSTED_METADATA_BLOCK_RE, "")
      .replace(LEADING_TIMESTAMP_RE, "")
      .trim();
    return stripped.length > 0 ? stripped : value.prompt;
  }

  if (
    type === "output" &&
    isString(value.output) &&
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

function truncateText(value: string, max = 8_000): string {
  if (value.length <= max) return value;
  return `${value.slice(0, max)}\n[TRUNCATED IN VIEW]`;
}

function sinceForRange(range: TraceRange): string {
  const now = new Date();
  const days = range === "24h" ? 1 : range === "7d" ? 7 : 30;
  now.setUTCDate(now.getUTCDate() - days);
  return now.toISOString();
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatDuration(startIso: string, endIso?: string): string {
  if (!endIso) return "running";
  const ms = new Date(endIso).getTime() - new Date(startIso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "--";
  if (ms < 1000) return `${ms}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
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

export function TracesTab({ machine, accountId }: TracesTabProps) {
  const [range, setRange] = useState<TraceRange>("7d");
  const [traces, setTraces] = useState<OpikTraceListItem[]>([]);
  const [selectedTraceId, setSelectedTraceId] = useState<string | null>(null);
  const [selectedSpanId, setSelectedSpanId] = useState<string | null>(null);
  const [detail, setDetail] = useState<OpikTraceDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);

  const loadTraces = () => {
    setLoading(true);
    setError(null);
    getMachineTraces(accountId, machine.id, sinceForRange(range), 50)
      .then((response) => {
        const nextTraces = response.traces ?? [];
        setTraces(nextTraces);
        setSelectedTraceId((current) => {
          if (current && nextTraces.some((trace) => trace.id === current)) return current;
          return nextTraces[0]?.id ?? null;
        });
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : "Failed to load traces");
        setTraces([]);
        setSelectedTraceId(null);
      })
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    loadTraces();
  }, [accountId, machine.id, range]);

  useEffect(() => {
    if (!selectedTraceId) {
      setDetail(null);
      setSelectedSpanId(null);
      setDetailError(null);
      return;
    }

    let cancelled = false;
    setDetailLoading(true);
    setDetailError(null);
    getMachineTrace(accountId, machine.id, selectedTraceId)
      .then((nextDetail) => {
        if (!cancelled) {
          setDetail(nextDetail);
          setSelectedSpanId(nextDetail.spans?.[0]?.id ?? null);
          setDetailError(null);
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
  }, [accountId, machine.id, selectedTraceId]);

  const selectedDetail = detail?.trace.id === selectedTraceId ? detail : null;
  const selectedTrace = selectedDetail?.trace ?? traces.find((trace) => trace.id === selectedTraceId) ?? null;
  const selectedSpan = useMemo(
    () => selectedDetail?.spans.find((span) => span.id === selectedSpanId) ?? selectedDetail?.spans[0] ?? null,
    [selectedDetail?.spans, selectedSpanId],
  );
  const emittingMachines = useMemo(() => {
    const ids = new Set(selectedDetail?.spans.map((span) => span.machine_id) ?? []);
    return [...ids];
  }, [selectedDetail?.spans]);

  const refreshSelectedTraceDetail = async () => {
    if (!selectedTraceId) return;
    const nextDetail = await getMachineTrace(accountId, machine.id, selectedTraceId);
    setDetail(nextDetail);
    setSelectedSpanId((current) => {
      if (current && nextDetail.spans?.some((span) => span.id === current)) return current;
      return nextDetail.spans?.[0]?.id ?? null;
    });
    loadTraces();
  };

  const handleTagsSaved = (traceId: string, nextTags: string[]) => {
    setDetail((current) => current && current.trace.id === traceId
      ? { ...current, trace: { ...current.trace, tags: nextTags } }
      : current);
    setTraces((current) => current.map((trace) => (trace.id === traceId ? { ...trace, tags: nextTags } : trace)));
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-text-primary">Traces</h2>
          <p className="text-sm text-text-tertiary">
            Recent Opik traces recorded by this machine or spans attached to it.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="inline-flex border border-border rounded-[var(--radius-sm)] overflow-hidden">
            {RANGE_OPTIONS.map((option) => (
              <button
                key={option.id}
                onClick={() => setRange(option.id)}
                className={`px-3 py-1.5 text-sm font-medium transition-colors ${
                  range === option.id
                    ? "bg-brand-600 text-white"
                    : "text-text-secondary hover:bg-[rgba(255,255,255,0.04)]"
                }`}
              >
                {option.label}
              </button>
            ))}
          </div>
          <button
            onClick={loadTraces}
            title="Refresh traces"
            className="inline-flex items-center justify-center w-9 h-9 border border-border rounded-[var(--radius-sm)] text-text-secondary hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)] transition-colors"
          >
            <RefreshCw className={`w-4 h-4 ${loading ? "animate-spin" : ""}`} />
          </button>
        </div>
      </div>

      {error && (
        <div className={`${ERROR_PANEL_CLASS} rounded-[var(--radius-md)] px-4 py-3 text-sm`}>
          {error}
        </div>
      )}

      <div className="grid lg:grid-cols-[minmax(280px,420px)_minmax(0,1fr)] gap-4">
        <div className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden">
          <div className="px-4 py-3 border-b border-border flex items-center justify-between">
            <span className="text-sm font-semibold text-text-primary">Recent traces</span>
            <span className="text-xs text-text-muted">{traces.length}</span>
          </div>
          {loading ? (
            <div className="p-4 space-y-3">
              {[0, 1, 2].map((i) => (
                <div key={i} className="h-16 bg-[rgba(255,255,255,0.04)] rounded-[var(--radius-sm)] animate-pulse" />
              ))}
            </div>
          ) : traces.length === 0 ? (
            <div className="p-6 text-sm text-text-tertiary">
              No traces for this period. Run an agent task with Opik observability enabled to populate this panel.
            </div>
          ) : (
            <div className="divide-y divide-border-subtle max-h-[640px] overflow-y-auto">
              {traces.map((trace) => (
                <button
                  key={trace.id}
                  onClick={() => setSelectedTraceId(trace.id)}
                  className={`w-full text-left px-4 py-3 transition-colors ${
                    selectedTraceId === trace.id
                      ? "bg-[rgba(249,115,22,0.08)]"
                      : "hover:bg-[rgba(255,255,255,0.03)]"
                  }`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        {hasPayload(trace.error_info) && <AlertTriangle className={`w-3.5 h-3.5 ${ERROR_ICON_CLASS} shrink-0`} />}
                        <p className="text-sm font-medium text-text-primary truncate">{traceTitle(trace)}</p>
                      </div>
                      <p className="text-xs text-text-muted font-mono truncate mt-0.5">{shortId(trace.id)}</p>
                    </div>
                    <span className="text-xs text-text-tertiary shrink-0">{formatDate(trace.start_time)}</span>
                  </div>
                  <div className="flex items-center gap-3 mt-2 text-xs text-text-tertiary">
                    <span>{trace.span_count} spans</span>
                    <span>{formatTokens(trace.total_tokens)} tokens</span>
                    <span className={trace.feedback_count > 0 && (trace.avg_feedback_score ?? 1) < 0.5 ? "text-red-100" : ""}>
                      {formatFeedbackScore(trace.avg_feedback_score)}
                    </span>
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>

        <div className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden min-h-[420px]">
          {!selectedTrace ? (
            <div className="h-full min-h-[420px] flex items-center justify-center p-6 text-sm text-text-tertiary">
              Select a trace to inspect spans.
            </div>
          ) : (
            <div className="min-w-0">
              <TraceHeader trace={selectedTrace} machine={machine} emittingMachines={emittingMachines} loading={detailLoading} />
              <div className="p-4 md:p-5 space-y-5">
                <PayloadSection title="Input" value={selectedTrace.input} type="input" />
                <PayloadSection title="Output" value={selectedTrace.output} type="output" />
                <PayloadSection title="Metadata" value={selectedTrace.metadata} type="metadata" />
                <PayloadSection title="Error" value={selectedTrace.error_info} type="metadata" danger />
                <DebugCues
                  spans={selectedDetail?.spans ?? []}
                  selectedSpanId={selectedSpanId}
                  loading={detailLoading}
                  onSelectSpan={setSelectedSpanId}
                />
                <TraceTagsPanel
                  accountId={accountId}
                  trace={selectedTrace}
                  onSaved={handleTagsSaved}
                />
                <TraceFeedbackPanel
                  accountId={accountId}
                  traceId={selectedTrace.id}
                  selectedSpan={selectedSpan}
                  feedback={selectedDetail?.feedback ?? []}
                  onCreated={refreshSelectedTraceDetail}
                />
                <SpansList
                  spans={selectedDetail?.spans ?? []}
                  currentMachineId={machine.id}
                  selectedSpanId={selectedSpanId}
                  onSelectSpan={setSelectedSpanId}
                  loading={detailLoading}
                  error={detailError}
                />
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function TraceHeader({
  trace,
  machine,
  emittingMachines,
  loading,
}: {
  trace: OpikTraceListItem;
  machine: Machine;
  emittingMachines: string[];
  loading: boolean;
}) {
  return (
    <div className="px-4 md:px-5 py-4 border-b border-border">
      <div className="flex flex-col md:flex-row md:items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Route className="w-4 h-4 text-brand-500 shrink-0" />
            <h3 className="text-base font-semibold text-text-primary truncate">{traceTitle(trace)}</h3>
          </div>
          <p className="text-xs text-text-muted font-mono mt-1 truncate">{trace.id}</p>
        </div>
        <div className="flex items-center gap-2 text-xs text-text-tertiary shrink-0">
          {loading && <RefreshCw className="w-3.5 h-3.5 animate-spin" />}
          <Clock3 className="w-3.5 h-3.5" />
          <span>{formatDuration(trace.start_time, trace.end_time)}</span>
        </div>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-3 gap-3 mt-4">
        <TraceMetric label="Spans" value={trace.span_count.toLocaleString()} />
        <TraceMetric label="Tokens" value={formatTokens(trace.total_tokens)} />
        <TraceMetric label="Machines" value={emittingMachines.length > 1 ? `${emittingMachines.length}` : machine.name} />
      </div>
    </div>
  );
}

function TraceMetric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-[11px] uppercase text-text-muted">{label}</p>
      <p className="text-sm font-semibold text-text-primary truncate mt-0.5">{value}</p>
    </div>
  );
}

function formatDurationMS(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "--";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
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
        <h4 className="text-sm font-semibold text-text-primary">Debug cues</h4>
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
      <div className="flex items-center gap-2 mb-2">
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

function SpansList({
  spans,
  currentMachineId,
  selectedSpanId,
  onSelectSpan,
  loading,
  error,
}: {
  spans: OpikTraceSpan[];
  currentMachineId: string;
  selectedSpanId: string | null;
  onSelectSpan: (spanID: string) => void;
  loading: boolean;
  error: string | null;
}) {
  return (
    <section>
      <div className="flex items-center gap-2 mb-2">
        <GitBranch className="w-4 h-4 text-text-tertiary" />
        <h4 className="text-sm font-semibold text-text-primary">Spans</h4>
      </div>
      {loading ? (
        <div className="h-24 bg-[rgba(255,255,255,0.04)] rounded-[var(--radius-md)] animate-pulse" />
      ) : error ? (
        <p className={`${ERROR_PANEL_CLASS} text-sm rounded-[var(--radius-md)] p-3`}>
          {error}
        </p>
      ) : spans.length === 0 ? (
        <p className="text-sm text-text-tertiary border border-border rounded-[var(--radius-md)] p-3">No spans recorded for this trace.</p>
      ) : (
        <div className="border border-border rounded-[var(--radius-md)] overflow-hidden divide-y divide-border-subtle">
          {spans.map((span) => (
            <SpanRow
              key={span.id}
              span={span}
              currentMachineId={currentMachineId}
              selected={span.id === selectedSpanId}
              onSelect={() => onSelectSpan(span.id)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function SpanRow({
  span,
  currentMachineId,
  selected,
  onSelect,
}: {
  span: OpikTraceSpan;
  currentMachineId: string;
  selected: boolean;
  onSelect: () => void;
}) {
  const title = spanTitle(span);
  const otherMachine = span.machine_id !== currentMachineId;
  const hasDetails =
    hasPayload(span.input) ||
    hasPayload(span.output) ||
    hasPayload(span.metadata) ||
    hasPayload(span.usage) ||
    hasPayload(span.error_info);

  return (
    <details className={`group px-3 py-3 ${selected ? "bg-[rgba(249,115,22,0.08)]" : ""}`} open={selected}>
      <summary className="list-none cursor-pointer [&::-webkit-details-marker]:hidden" onClick={onSelect}>
        <div className="flex flex-col md:flex-row md:items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <ChevronDown className="w-3.5 h-3.5 text-text-tertiary transition-transform group-open:rotate-180" />
              <span className="text-sm font-medium text-text-primary truncate">{title}</span>
              <span className="text-[11px] px-1.5 py-0.5 rounded bg-[rgba(255,255,255,0.06)] text-text-tertiary uppercase">
                {span.type || "general"}
              </span>
              {otherMachine && (
                <span className="text-[11px] px-1.5 py-0.5 rounded bg-[rgba(59,130,246,0.12)] text-blue-300">
                  {shortId(span.machine_id)}
                </span>
              )}
              {hasPayload(span.error_info) && <AlertTriangle className={`w-3.5 h-3.5 ${ERROR_ICON_CLASS}`} />}
            </div>
            {(span.model || span.provider) && (
              <p className="text-xs text-text-muted mt-1">
                {[span.provider, span.model].filter(Boolean).join(" / ")}
              </p>
            )}
          </div>
          <div className="flex items-center gap-3 text-xs text-text-tertiary shrink-0">
            <span>{formatDate(span.start_time)}</span>
            <span>{formatDuration(span.start_time, span.end_time)}</span>
          </div>
        </div>
        <div className="flex items-center gap-3 mt-2 text-xs text-text-tertiary">
          <span>{formatTokens(span.total_tokens)} tokens</span>
          {span.parent_span_id && <span className="font-mono">parent {shortId(span.parent_span_id)}</span>}
        </div>
      </summary>
      <div className="mt-3 pt-3 border-t border-border-subtle space-y-3">
        <div className="grid md:grid-cols-3 gap-3 text-xs">
          <TraceMetric label="Span ID" value={span.id} />
          <TraceMetric label="Trace ID" value={span.trace_id} />
          <TraceMetric label="Machine" value={span.machine_id} />
        </div>
        {hasDetails ? (
          <>
            <PayloadSection title="Input" value={span.input} type="input" />
            <PayloadSection title="Output" value={span.output} type="output" />
            <PayloadSection title="Metadata" value={span.metadata} type="metadata" />
            <PayloadSection title="Usage" value={span.usage} type="metadata" />
            <PayloadSection title="Error" value={span.error_info} type="metadata" danger />
          </>
        ) : (
          <p className="text-sm text-text-tertiary border border-border rounded-[var(--radius-md)] p-3">
            No payload data recorded for this span.
          </p>
        )}
      </div>
    </details>
  );
}
