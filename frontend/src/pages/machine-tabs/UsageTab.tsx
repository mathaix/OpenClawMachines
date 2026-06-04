import { useEffect, useState } from "react";
import { DollarSign, Zap, TrendingUp, BarChart3 } from "lucide-react";
import type { Machine, UsageBreakdown, UsageBucketEntry } from "../../lib/types";
import { getMachineUsageBreakdown } from "../../lib/api";

interface UsageTabProps {
  machine: Machine;
  accountId: number;
}

function formatMicrocents(mc: number): string {
  const dollars = mc / 1_000_000;
  return `$${dollars.toFixed(4)}`;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}

function formatHour(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function formatDay(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

type Period = "hour" | "day";

export function UsageTab({ machine, accountId }: UsageTabProps) {
  const [period, setPeriod] = useState<Period>("hour");
  const [breakdown, setBreakdown] = useState<UsageBreakdown | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    getMachineUsageBreakdown(accountId, machine.id, period)
      .then((b) => setBreakdown(b ?? null))
      .catch(() => setBreakdown(null))
      .finally(() => setLoading(false));
  }, [accountId, machine.id, period]);

  const totals = breakdown?.totals ?? { input_tokens: 0, output_tokens: 0, cost_microcents: 0, request_count: 0 };

  // Aggregate entries across all buckets by model for the summary table
  const modelTotals = new Map<string, UsageBucketEntry & { key: string }>();
  for (const bucket of breakdown?.buckets ?? []) {
    for (const e of bucket.entries) {
      const key = `${e.provider}/${e.model}`;
      const existing = modelTotals.get(key);
      if (existing) {
        existing.input_tokens += e.input_tokens;
        existing.output_tokens += e.output_tokens;
        existing.cost_microcents += e.cost_microcents;
        existing.request_count += e.request_count;
      } else {
        modelTotals.set(key, { ...e, key });
      }
    }
  }
  const sortedModels = [...modelTotals.values()].sort((a, b) => b.cost_microcents - a.cost_microcents);

  // Find max cost per bucket for the bar chart scaling
  const bucketCosts = (breakdown?.buckets ?? []).map((b) =>
    b.entries.reduce((sum, e) => sum + e.cost_microcents, 0)
  );
  const maxBucketCost = Math.max(...bucketCosts, 1);

  return (
    <div className="space-y-6">
      {/* Period toggle */}
      <div className="flex items-center gap-2">
        <button
          onClick={() => setPeriod("hour")}
          className={`px-3 py-1.5 text-sm font-medium rounded-[var(--radius-sm)] transition-colors ${
            period === "hour"
              ? "bg-brand-600 text-white"
              : "text-text-secondary border border-border hover:bg-[rgba(255,255,255,0.04)]"
          }`}
        >
          Today (Hourly)
        </button>
        <button
          onClick={() => setPeriod("day")}
          className={`px-3 py-1.5 text-sm font-medium rounded-[var(--radius-sm)] transition-colors ${
            period === "day"
              ? "bg-brand-600 text-white"
              : "text-text-secondary border border-border hover:bg-[rgba(255,255,255,0.04)]"
          }`}
        >
          This Month (Daily)
        </button>
      </div>

      {/* Summary cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <SummaryCard icon={<DollarSign className="w-4 h-4 text-green-400" />} label="Total Cost" value={formatMicrocents(totals.cost_microcents)} />
        <SummaryCard icon={<Zap className="w-4 h-4 text-yellow-400" />} label="Input Tokens" value={formatTokens(totals.input_tokens)} />
        <SummaryCard icon={<TrendingUp className="w-4 h-4 text-blue-400" />} label="Output Tokens" value={formatTokens(totals.output_tokens)} />
        <SummaryCard icon={<BarChart3 className="w-4 h-4 text-purple-400" />} label="Requests" value={totals.request_count.toLocaleString()} />
      </div>

      {loading ? (
        <div className="h-48 bg-card border border-border rounded-[var(--radius-lg)] animate-pulse" />
      ) : (
        <>
          {/* Bar chart */}
          <div className="bg-card border border-border rounded-[var(--radius-lg)] p-4 md:p-6">
            <h3 className="text-sm font-semibold text-text-primary mb-4">
              {period === "hour" ? "Hourly" : "Daily"} Cost
            </h3>
            {(breakdown?.buckets ?? []).length === 0 ? (
              <p className="text-sm text-text-tertiary">No usage data for this period.</p>
            ) : (
              <div className="flex items-end gap-1 h-32">
                {(breakdown?.buckets ?? []).map((bucket, i) => {
                  const cost = bucket.entries.reduce((s, e) => s + e.cost_microcents, 0);
                  const pct = (cost / maxBucketCost) * 100;
                  return (
                    <div key={i} className="flex-1 flex flex-col items-center gap-1 min-w-0">
                      <div
                        className="w-full bg-brand-600 rounded-t-sm transition-all"
                        style={{ height: `${Math.max(pct, 2)}%` }}
                        title={`${period === "hour" ? formatHour(bucket.timestamp) : formatDay(bucket.timestamp)}: ${formatMicrocents(cost)}`}
                      />
                      {(breakdown?.buckets ?? []).length <= 24 && (
                        <span className="text-[9px] text-text-muted truncate w-full text-center">
                          {period === "hour" ? formatHour(bucket.timestamp) : formatDay(bucket.timestamp)}
                        </span>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {/* Model breakdown table */}
          <div className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden">
            <div className="p-4 md:p-6 pb-0">
              <h3 className="text-sm font-semibold text-text-primary mb-3">By Model</h3>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-text-tertiary">
                    <th className="text-left px-4 md:px-6 py-2 font-medium">Model</th>
                    <th className="text-right px-4 py-2 font-medium">Input</th>
                    <th className="text-right px-4 py-2 font-medium">Output</th>
                    <th className="text-right px-4 py-2 font-medium">Cost</th>
                    <th className="text-right px-4 md:px-6 py-2 font-medium">Requests</th>
                  </tr>
                </thead>
                <tbody>
                  {sortedModels.length === 0 ? (
                    <tr>
                      <td colSpan={5} className="px-4 md:px-6 py-4 text-text-tertiary text-center">
                        No usage data
                      </td>
                    </tr>
                  ) : (
                    sortedModels.map((m) => (
                      <tr key={m.key} className="border-b border-border-subtle hover:bg-[rgba(255,255,255,0.02)]">
                        <td className="px-4 md:px-6 py-2.5">
                          <div className="font-medium text-text-primary">{m.model}</div>
                          <div className="text-xs text-text-muted">{m.provider}</div>
                        </td>
                        <td className="text-right px-4 py-2.5 tabular-nums text-text-secondary">{formatTokens(m.input_tokens)}</td>
                        <td className="text-right px-4 py-2.5 tabular-nums text-text-secondary">{formatTokens(m.output_tokens)}</td>
                        <td className="text-right px-4 py-2.5 tabular-nums text-text-primary font-medium">{formatMicrocents(m.cost_microcents)}</td>
                        <td className="text-right px-4 md:px-6 py-2.5 tabular-nums text-text-secondary">{m.request_count}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function SummaryCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <div className="bg-card border border-border rounded-[var(--radius-lg)] p-3 md:p-4">
      <div className="flex items-center gap-2 mb-1">
        {icon}
        <span className="text-xs text-text-tertiary">{label}</span>
      </div>
      <div className="text-lg md:text-xl font-bold tabular-nums text-text-primary">{value}</div>
    </div>
  );
}
