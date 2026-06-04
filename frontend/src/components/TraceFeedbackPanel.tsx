import { useMemo, useState } from "react";
import { MessageSquarePlus, Star } from "lucide-react";
import { createTraceFeedback } from "../lib/api";
import type { OpikFeedbackScore, OpikTraceSpan } from "../lib/types";

const SCORE_NAMES = [
  { id: "task_success", label: "Task success" },
  { id: "correctness", label: "Correctness" },
  { id: "helpfulness", label: "Helpfulness" },
  { id: "tool_quality", label: "Tool quality" },
];

const SCORE_VALUES = [
  { label: "Good", value: 1 },
  { label: "Mixed", value: 0.5 },
  { label: "Bad", value: 0 },
];

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatScore(value: number): string {
  return `${Math.round(value * 100)}%`;
}

function shortId(id: string): string {
  return id.length > 12 ? id.slice(0, 8) : id;
}

function spanTitle(span: OpikTraceSpan): string {
  return span.name || span.type || shortId(span.id);
}

function scoreTone(value: number): string {
  if (value >= 0.8) return "text-green-300";
  if (value >= 0.5) return "text-yellow-200";
  return "text-red-100";
}

export function TraceFeedbackPanel({
  accountId,
  traceId,
  selectedSpan,
  feedback,
  onCreated,
}: {
  accountId: number;
  traceId: string;
  selectedSpan: OpikTraceSpan | null;
  feedback: OpikFeedbackScore[];
  onCreated: () => Promise<void> | void;
}) {
  const [target, setTarget] = useState<"trace" | "span">("trace");
  const [name, setName] = useState("task_success");
  const [value, setValue] = useState(1);
  const [reason, setReason] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const average = useMemo(() => {
    if (feedback.length === 0) return null;
    return feedback.reduce((sum, item) => sum + item.value, 0) / feedback.length;
  }, [feedback]);

  const submit = async () => {
    setSaving(true);
    setError(null);
    try {
      await createTraceFeedback(accountId, traceId, {
        name,
        value,
        reason: reason.trim() || undefined,
        span_id: target === "span" && selectedSpan ? selectedSpan.id : undefined,
      });
      setReason("");
      await onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save feedback");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section>
      <div className="flex items-center justify-between gap-3 mb-2">
        <div className="flex items-center gap-2">
          <Star className="w-4 h-4 text-text-tertiary" />
          <h3 className="text-sm font-semibold text-text-primary">Feedback</h3>
        </div>
        <span className="text-xs text-text-tertiary">
          {average === null ? "Unreviewed" : `${formatScore(average)} avg`}
        </span>
      </div>

      <div className="border border-border rounded-[var(--radius-md)] p-3 space-y-3">
        <div className="grid md:grid-cols-[150px_160px_minmax(0,1fr)] gap-3">
          <label className="block">
            <span className="text-xs text-text-muted">Score</span>
            <select
              value={name}
              onChange={(event) => setName(event.target.value)}
              className="mt-1 h-9 w-full border border-border rounded-[var(--radius-sm)] bg-card px-2 text-sm text-text-primary"
            >
              {SCORE_NAMES.map((option) => (
                <option key={option.id} value={option.id}>{option.label}</option>
              ))}
            </select>
          </label>

          <div>
            <span className="text-xs text-text-muted">Value</span>
            <div className="mt-1 inline-flex w-full border border-border rounded-[var(--radius-sm)] overflow-hidden">
              {SCORE_VALUES.map((option) => (
                <button
                  key={option.label}
                  type="button"
                  onClick={() => setValue(option.value)}
                  className={`flex-1 h-9 text-xs font-medium transition-colors ${
                    value === option.value
                      ? "bg-brand-600 text-white"
                      : "text-text-secondary hover:bg-[rgba(255,255,255,0.04)]"
                  }`}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </div>

          <div>
            <span className="text-xs text-text-muted">Target</span>
            <div className="mt-1 inline-flex w-full border border-border rounded-[var(--radius-sm)] overflow-hidden">
              <button
                type="button"
                onClick={() => setTarget("trace")}
                className={`flex-1 h-9 text-xs font-medium transition-colors ${
                  target === "trace" ? "bg-brand-600 text-white" : "text-text-secondary hover:bg-[rgba(255,255,255,0.04)]"
                }`}
              >
                Trace
              </button>
              <button
                type="button"
                disabled={!selectedSpan}
                onClick={() => setTarget("span")}
                className={`flex-1 h-9 text-xs font-medium transition-colors disabled:opacity-40 ${
                  target === "span" ? "bg-brand-600 text-white" : "text-text-secondary hover:bg-[rgba(255,255,255,0.04)]"
                }`}
                title={selectedSpan ? spanTitle(selectedSpan) : "Select a span first"}
              >
                Span
              </button>
            </div>
          </div>
        </div>

        <textarea
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          placeholder="Reason"
          rows={3}
          className="w-full border border-border rounded-[var(--radius-sm)] bg-[rgba(0,0,0,0.12)] px-3 py-2 text-sm text-text-primary placeholder:text-text-muted outline-none resize-y"
        />

        {error && (
          <p className="text-sm border border-red-400/50 bg-red-950/60 text-red-50 rounded-[var(--radius-md)] p-3">
            {error}
          </p>
        )}

        <button
          type="button"
          onClick={submit}
          disabled={saving}
          className="inline-flex items-center gap-2 h-9 px-3 rounded-[var(--radius-sm)] bg-brand-600 text-white text-sm font-medium hover:bg-brand-700 disabled:opacity-50 transition-colors"
        >
          <MessageSquarePlus className="w-4 h-4" />
          {saving ? "Saving..." : "Add feedback"}
        </button>

        {feedback.length > 0 && (
          <div className="pt-2 border-t border-border-subtle space-y-2">
            {feedback.slice(0, 6).map((item) => (
              <div key={item.id} className="flex flex-col md:flex-row md:items-start justify-between gap-2 text-sm">
                <div className="min-w-0">
                  <p className="text-text-primary">
                    <span className={`font-semibold ${scoreTone(item.value)}`}>{formatScore(item.value)}</span>
                    <span className="text-text-tertiary"> · {item.name}</span>
                    {item.span_id && <span className="text-text-muted font-mono"> · span {shortId(item.span_id)}</span>}
                  </p>
                  {item.reason && <p className="text-text-tertiary mt-0.5 break-words">{item.reason}</p>}
                </div>
                <span className="text-xs text-text-muted shrink-0">{formatDate(item.created_at)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}
