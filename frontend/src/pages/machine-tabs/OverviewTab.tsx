import { useEffect, useState } from "react";
import {
  MessageSquare, Zap, TrendingUp, DollarSign,
  Server, Calendar, Activity, HardDrive,
  Brain, Plug, Globe, ArrowUpCircle, ArrowDownCircle,
} from "lucide-react";
import type { Machine, MachineUsageDetail, UsageRecord } from "../../lib/types";
import { getMachineUsage, listMachineCapabilities, upgradeMachineOpenClaw, rollbackMachineOpenClaw, listOpenClawReleases, upgradeMachineRootfs, rollbackMachineRootfs, listRootfsReleases } from "../../lib/api";
import type { MachineCapability, RuntimeRelease } from "../../lib/api";
import { useSizes } from "../../lib/useSizes";
import { SetupCard } from "../../components/SetupCard";
import { StatusBadge } from "../../components/StatusBadge";

interface OverviewTabProps {
  machine: Machine;
  accountId: number;
  onTabChange: (tab: string) => void;
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

function formatUptime(startedAt: string | undefined): string {
  if (!startedAt) return "\u2014";
  const ms = Date.now() - new Date(startedAt).getTime();
  const h = Math.floor(ms / 3600000);
  const m = Math.floor((ms % 3600000) / 60000);
  if (h > 24) return `${Math.floor(h / 24)}d ${h % 24}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function formatMicrocents(mc: number): string {
  const cents = mc / 10000;
  return `$${(cents / 100).toFixed(4)}`;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}

function ProgressBar({ value, max, colorClass }: { value: number; max: number; colorClass: string }) {
  const pct = max > 0 ? Math.min((value / max) * 100, 100) : 0;
  return (
    <div className="w-full h-2 bg-border rounded-full overflow-hidden">
      <div className={`h-full rounded-full transition-all duration-500 ${colorClass}`} style={{ width: `${pct}%` }} />
    </div>
  );
}

function RuntimeVersionSection({ machine, accountId }: { machine: Machine; accountId: number }) {
  type RuntimeTarget = "openclaw" | "rootfs";
  const [expanded, setExpanded] = useState(false);
  const [targetKind, setTargetKind] = useState<RuntimeTarget>("openclaw");
  const [targetVersion, setTargetVersion] = useState("");
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; message: string } | null>(null);
  const [openClawReleases, setOpenClawReleases] = useState<RuntimeRelease[]>([]);
  const [rootfsReleases, setRootfsReleases] = useState<RuntimeRelease[]>([]);
  const [openClawReleasesLoaded, setOpenClawReleasesLoaded] = useState(false);
  const [rootfsReleasesLoaded, setRootfsReleasesLoaded] = useState(false);

  const machineKind = machine.kind ?? "openclaw";
  const isHermesMachine = machineKind === "hermes";
  const runtimeLabel = isHermesMachine ? "Hermes" : "OpenClaw";
  const rootfsLabel = isHermesMachine ? "Hermes rootfs" : "Rootfs";
  const effectiveTargetKind: RuntimeTarget = isHermesMachine ? "rootfs" : targetKind;
  const actualOpenClawVersion = machine.actual_openclaw_version || machine.openclaw_version || "";
  const actualRootfsVersion = machine.actual_rootfs_version || machine.rootfs_snapshot || "";
  const currentVersion = effectiveTargetKind === "openclaw"
    ? (actualOpenClawVersion || machine.resolved_openclaw_version || "")
    : (actualRootfsVersion || machine.resolved_rootfs_version || "");
  const resolverActive = true; // upgrade controls available for all machines including legacy
  const openClawUpgradePending = !!(
    machine.desired_openclaw_version &&
    actualOpenClawVersion &&
    machine.desired_openclaw_version !== actualOpenClawVersion
  );
  const rootfsUpgradePending = !!(
    machine.desired_rootfs_version &&
    actualRootfsVersion &&
    machine.desired_rootfs_version !== actualRootfsVersion
  );
  const upgradePending = openClawUpgradePending || rootfsUpgradePending;
  // Only show "Upgrade available" when machine is running and versions differ.
  // When stopped, the upgrade is already queued — show "Upgrade pending" instead.
  const isRunning = machine.status === "running";
  const upgradeAvailable = upgradePending && isRunning;
  const upgradePendingRestart = upgradePending && !isRunning;

  // Determine action direction based on selected version vs current
  const isUpgrade = targetVersion !== "" && targetVersion > currentVersion;
  const actionLabel = targetVersion === "" ? "Select version" : isUpgrade ? "Upgrade" : "Rollback";
  const ActionIcon = isUpgrade ? ArrowUpCircle : ArrowDownCircle;
  const releases = effectiveTargetKind === "openclaw" ? openClawReleases : rootfsReleases;
  const releasesLoaded = effectiveTargetKind === "openclaw" ? openClawReleasesLoaded : rootfsReleasesLoaded;

  useEffect(() => {
    setOpenClawReleases([]);
    setRootfsReleases([]);
    setOpenClawReleasesLoaded(false);
    setRootfsReleasesLoaded(false);
    setTargetVersion("");
    setResult(null);
    if (isHermesMachine) setTargetKind("rootfs");
  }, [accountId, machine.id, isHermesMachine]);

  useEffect(() => {
    if (!expanded) {
      // Reset loaded flags on collapse so re-expanding retries on failure
      if (openClawReleases.length === 0) setOpenClawReleasesLoaded(false);
      if (rootfsReleases.length === 0) setRootfsReleasesLoaded(false);
      return;
    }
    if (!isHermesMachine && !openClawReleasesLoaded) {
      listOpenClawReleases(accountId, "stable", machineKind).then(setOpenClawReleases).catch(() => {}).finally(() => setOpenClawReleasesLoaded(true));
    }
    if (!rootfsReleasesLoaded) {
      listRootfsReleases(accountId, "stable", machineKind).then(setRootfsReleases).catch(() => {}).finally(() => setRootfsReleasesLoaded(true));
    }
  }, [expanded, openClawReleasesLoaded, rootfsReleasesLoaded, accountId, machineKind, isHermesMachine, openClawReleases.length, rootfsReleases.length]);

  useEffect(() => {
    setTargetVersion("");
    setResult(null);
  }, [targetKind]);

  const handleAction = async () => {
    if (!targetVersion.trim()) return;
    setLoading(true);
    setResult(null);
    try {
      const fn = effectiveTargetKind === "rootfs"
        ? (isUpgrade ? upgradeMachineRootfs : rollbackMachineRootfs)
        : (isUpgrade ? upgradeMachineOpenClaw : rollbackMachineOpenClaw);
      const resp = await fn(accountId, machine.id, targetVersion.trim(), false);
      if (resp.error) {
        setResult({ ok: false, message: resp.error });
      } else {
        const label = effectiveTargetKind === "rootfs" ? rootfsLabel : runtimeLabel;
        setResult({ ok: true, message: `${label} ${resp.status}: ${resp.target_version}` });
        setTargetVersion("");
      }
    } catch (err: any) {
      setResult({ ok: false, message: err.message || "Request failed" });
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="border-b border-border-subtle">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center justify-between w-full py-2.5 md:py-3 text-left"
      >
        <div className="flex items-center gap-2">
          <Server className="w-4 h-4 text-text-muted" />
          <span className="text-xs md:text-sm font-medium text-text-secondary">Runtime</span>
        </div>
          <div className="flex items-center gap-2">
            <code className="text-[10px] md:text-[11px] bg-elevated px-2 py-0.5 rounded font-mono text-text-secondary">
            {actualOpenClawVersion || "—"}
            </code>
          {upgradeAvailable && (
            <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-100 text-amber-700">
              Upgrade available
            </span>
          )}
          {upgradePendingRestart && (
            <span className="text-[10px] px-1.5 py-0.5 rounded bg-blue-100 text-blue-700">
              Upgrade pending — start to apply
            </span>
          )}
          <span className="text-[10px] px-1.5 py-0.5 rounded bg-elevated text-text-muted">
            Artifact
          </span>
          <span className="text-text-muted text-xs">{expanded ? "▴" : "▾"}</span>
        </div>
      </button>

      {expanded && (
        <div className="pb-3 space-y-3">
          {/* Version summary — two rows max */}
          <div className="grid grid-cols-2 gap-x-4 gap-y-1 text-[11px]">
            <div>
              <span className="text-text-muted">{runtimeLabel}:</span>{" "}
              <code className="text-text-secondary">{machine.desired_openclaw_version || actualOpenClawVersion || "default"}</code>
              {machine.desired_openclaw_version && actualOpenClawVersion && machine.desired_openclaw_version !== actualOpenClawVersion && (
                <span className="text-text-muted ml-1">(running {actualOpenClawVersion})</span>
              )}
            </div>
            <div>
              <span className="text-text-muted">{rootfsLabel}:</span>{" "}
              <code className="text-text-secondary">{machine.desired_rootfs_version || actualRootfsVersion || "default"}</code>
            </div>
          </div>

          {/* Change version controls */}
          {resolverActive ? (
          <>
          <div className="flex items-center gap-2">
            {!isHermesMachine && (
              <button
                type="button"
                onClick={() => setTargetKind("openclaw")}
                disabled={loading}
                className={`text-[11px] px-2.5 py-1 rounded border ${effectiveTargetKind === "openclaw" ? "bg-accent text-white border-accent" : "bg-surface text-text-secondary border-border"}`}
              >
                {runtimeLabel}
              </button>
            )}
            <button
              type="button"
              onClick={() => setTargetKind("rootfs")}
              disabled={loading}
              className={`text-[11px] px-2.5 py-1 rounded border ${effectiveTargetKind === "rootfs" ? "bg-accent text-white border-accent" : "bg-surface text-text-secondary border-border"}`}
            >
              Rootfs
            </button>
          </div>
          <div className="flex items-center gap-2">
            <select
              value={targetVersion}
              onChange={(e) => setTargetVersion(e.target.value)}
              disabled={loading || !releasesLoaded}
              className="flex-1 text-xs px-2 py-1.5 rounded border border-border bg-surface text-text-primary"
            >
              <option value="">{effectiveTargetKind === "openclaw" ? `Select ${runtimeLabel} version...` : `Select ${rootfsLabel} version...`}</option>
              {releases.map((r) => (
                <option key={r.exact_version} value={r.exact_version}>
                  {r.version}{r.exact_version === currentVersion ? " (current)" : ""}
                </option>
              ))}
            </select>
            <button
              onClick={handleAction}
              disabled={loading || !targetVersion.trim()}
              className={`flex items-center gap-1 text-xs px-3 py-1.5 rounded whitespace-nowrap disabled:opacity-50 ${
                isUpgrade
                  ? "bg-accent text-white hover:bg-accent/90"
                  : "bg-elevated text-text-secondary hover:bg-border"
              }`}
            >
              <ActionIcon className="w-3.5 h-3.5" />
              {actionLabel}
            </button>
          </div>
          </>
          ) : (
          <p className="text-[11px] text-text-muted">
            Runtime version management is not enabled on this platform.
          </p>
          )}
          {result && (
            <p className={`text-[11px] ${result.ok ? "text-green-500" : "text-red-400"}`}>
              {result.message}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

export function OverviewTab({ machine, accountId, onTabChange }: OverviewTabProps) {
  const [usage, setUsage] = useState<MachineUsageDetail | null>(null);
  const [usageLoading, setUsageLoading] = useState(true);
  const [usageError, setUsageError] = useState<string | null>(null);
  const [capabilities, setCapabilities] = useState<MachineCapability[]>([]);
  const [capsLoading, setCapsLoading] = useState(true);
  const [capsError, setCapsError] = useState<string | null>(null);
  const sizes = useSizes();

  const isStopped = machine.status === "stopped";

  useEffect(() => {
    setUsageLoading(true);
    setUsageError(null);
    getMachineUsage(accountId, machine.id)
      .then((data) => { setUsage(data); setUsageError(null); })
      .catch((err) => setUsageError(err instanceof Error ? err.message : "Failed to load usage"))
      .finally(() => setUsageLoading(false));

    setCapsLoading(true);
    setCapsError(null);
    listMachineCapabilities(accountId, machine.id)
      .then((c) => { setCapabilities(c ?? []); setCapsError(null); })
      .catch((err) => setCapsError(err instanceof Error ? err.message : "Failed to load capabilities"))
      .finally(() => setCapsLoading(false));
  }, [accountId, machine.id]);

  const totalInputTokens = usage?.records?.reduce((s: number, r: UsageRecord) => s + r.input_tokens, 0) ?? 0;
  const totalOutputTokens = usage?.records?.reduce((s: number, r: UsageRecord) => s + r.output_tokens, 0) ?? 0;
  const totalMessages = usage?.records?.length ?? 0;
  const totalSpend = usage?.current_month_spend ?? 0;

  // Aggregate usage records by model
  const modelMap = new Map<string, { model: string; provider: string; input_tokens: number; output_tokens: number; cost_microcents: number; count: number }>();
  for (const r of usage?.records ?? []) {
    const key = `${r.provider}/${r.model}`;
    const existing = modelMap.get(key);
    if (existing) {
      existing.input_tokens += r.input_tokens;
      existing.output_tokens += r.output_tokens;
      existing.cost_microcents += r.cost_microcents;
      existing.count += 1;
    } else {
      modelMap.set(key, { model: r.model, provider: r.provider, input_tokens: r.input_tokens, output_tokens: r.output_tokens, cost_microcents: r.cost_microcents, count: 1 });
    }
  }
  const modelBreakdown = [...modelMap.values()].sort((a, b) => b.cost_microcents - a.cost_microcents);
  const budgetMc = usage?.budget_microcents ?? 0;
  const spendPct = budgetMc > 0 ? totalSpend / budgetMc : 0;
  const showCreditWarning = budgetMc > 0 && spendPct >= 0.8;
  const creditWarningLevel = spendPct >= 0.95 ? "red" : "yellow";

  const hasChannels = !capsLoading && !capsError && capabilities.some((c) => c.enabled);
  const hasIntegrations = false;
  const hasBrowser = false;
  const isHermes = machine.kind === "hermes";

  const matchedSize = sizes.find((s) => s.vcpus === machine.vcpus && s.memory_mb === machine.memory_mb);
  const size = matchedSize ? matchedSize.label : `${machine.vcpus} vCPU / ${machine.memory_mb / 1024} GB`;

  const maxMessages = 10000;
  const storageGB = matchedSize?.disk_gb ?? machine.data_volume_gb ?? 5;

  return (
    <div className="space-y-6">
      {/* Credit warning banner */}
      {showCreditWarning && (
        <div
          className={`rounded-[var(--radius)] px-4 py-3 text-sm font-medium flex items-center gap-2 ${
            creditWarningLevel === "red"
              ? "bg-red-500/10 border border-red-500/30 text-red-400"
              : "bg-yellow-500/10 border border-yellow-500/30 text-yellow-400"
          }`}
        >
          <span>{creditWarningLevel === "red" ? "\u26A0" : "!"}</span>
          <span>
            {creditWarningLevel === "red"
              ? "Budget nearly exhausted \u2014 your machine may stop soon."
              : "Approaching budget limit \u2014 usage is over 80%."}
          </span>
        </div>
      )}

      {/* Setup cards */}
      <div>
        <h2 className="text-base md:text-lg font-semibold text-text-primary mb-4">SETUP</h2>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3 md:gap-4">
          <SetupCard
            icon={<Brain className="w-5 h-5 md:w-6 md:h-6" />}
            iconColorClass="bg-gradient-to-br from-[rgba(236,72,153,0.2)] to-[rgba(168,85,247,0.2)] text-purple-400"
            hoverBorderClass="hover:border-purple-400/40"
            title="Choose AI Model"
            description="Select which AI model powers your machine"
            completed={false}
            onClick={() => onTabChange("model")}
          />
          <SetupCard
            icon={<MessageSquare className="w-5 h-5 md:w-6 md:h-6" />}
            iconColorClass="bg-gradient-to-br from-[rgba(52,211,153,0.2)] to-[rgba(20,184,166,0.2)] text-teal-400"
            hoverBorderClass="hover:border-teal-400/40"
            title="Connect Channels"
            description="Add Telegram, WhatsApp, or other messaging channels"
            completed={hasChannels}
            onClick={() => onTabChange("channels")}
          />
          <SetupCard
            icon={<Plug className="w-5 h-5 md:w-6 md:h-6" />}
            iconColorClass="bg-gradient-to-br from-[rgba(96,165,250,0.2)] to-[rgba(99,102,241,0.2)] text-blue-400"
            hoverBorderClass="hover:border-blue-400/40"
            title="Add Integrations"
            description="Connect Gmail, Slack, GitHub, and more"
            completed={hasIntegrations}
            onClick={() => onTabChange("integrations")}
          />
          <SetupCard
            icon={<Globe className="w-5 h-5 md:w-6 md:h-6" />}
            iconColorClass="bg-gradient-to-br from-[rgba(249,115,22,0.2)] to-[rgba(245,158,11,0.2)] text-orange-400"
            hoverBorderClass="hover:border-brand-600/40"
            title={isHermes ? "Open Dashboard" : "Enable Browser"}
            description={isHermes ? "Use the Hermes dashboard for chat, sessions, and files" : "Give your machine the ability to browse the web"}
            completed={isHermes || hasBrowser}
            onClick={() => onTabChange(isHermes ? "dashboard" : "browser")}
          />
        </div>
      </div>

      {/* Usage & Machine Info Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 md:gap-6">
        {/* Usage Metrics Card */}
        <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
          <div className="p-4 md:p-6 pb-0">
            <div className="flex items-center justify-between mb-1">
              <div className="text-lg md:text-xl font-semibold text-text-primary">Usage Metrics</div>
              <span className="text-[10px] md:text-[11px] font-medium text-text-tertiary border border-border rounded-full px-2 py-0.5">This month</span>
            </div>
            <div className="text-xs md:text-sm text-text-tertiary mb-4">Monitor your machine's resource consumption</div>
          </div>
          <div className="px-4 md:px-6 pb-4 md:pb-6 space-y-4">
            {usageLoading ? (
              <div className="space-y-3 py-2">
                <div className="h-4 w-48 bg-elevated rounded animate-pulse" />
                <div className="h-2 w-full bg-elevated rounded animate-pulse" />
                <div className="h-4 w-36 bg-elevated rounded animate-pulse" />
                <div className="h-2 w-full bg-elevated rounded animate-pulse" />
              </div>
            ) : usageError ? (
              <div className="text-sm text-red-400 py-2">Failed to load usage data</div>
            ) : isStopped && !usage?.records?.length ? (
              <div className="text-sm text-text-muted py-4 text-center">Machine is stopped — usage data unavailable</div>
            ) : (<>
            {/* Messages */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-sm">
                <div className="flex items-center gap-2">
                  <MessageSquare className="w-4 h-4 text-blue-400" />
                  <span className="font-medium text-text-secondary">Messages</span>
                </div>
                <span className="font-semibold tabular-nums text-text-primary">{totalMessages.toLocaleString()}</span>
              </div>
              <ProgressBar value={totalMessages} max={maxMessages} colorClass="bg-blue-500" />
              <p className="text-xs text-text-muted">{totalMessages.toLocaleString()} of {maxMessages.toLocaleString()} messages used</p>
            </div>

            <div className="border-t border-border-subtle" />

            {/* Input Tokens */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-sm">
                <div className="flex items-center gap-2">
                  <Zap className="w-4 h-4 text-yellow-400" />
                  <span className="font-medium text-text-secondary">Input Tokens</span>
                </div>
                <span className="font-semibold tabular-nums text-text-primary">{formatTokens(totalInputTokens)}</span>
              </div>
              <ProgressBar value={totalInputTokens} max={1000000} colorClass="bg-yellow-500" />
              <p className="text-xs text-text-muted">{formatTokens(totalInputTokens)} tokens processed</p>
            </div>

            <div className="border-t border-border-subtle" />

            {/* Output Tokens */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-sm">
                <div className="flex items-center gap-2">
                  <TrendingUp className="w-4 h-4 text-green-400" />
                  <span className="font-medium text-text-secondary">Output Tokens</span>
                </div>
                <span className="font-semibold tabular-nums text-text-primary">{formatTokens(totalOutputTokens)}</span>
              </div>
              <ProgressBar value={totalOutputTokens} max={1000000} colorClass="bg-green-500" />
              <p className="text-xs text-text-muted">{formatTokens(totalOutputTokens)} tokens generated</p>
            </div>

            {/* Model breakdown */}
            {modelBreakdown.length > 0 && (
              <>
                <div className="border-t border-border-subtle" />
                <div className="space-y-2">
                  <span className="text-xs font-medium text-text-tertiary">By Model</span>
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="text-text-muted">
                        <th className="text-left py-1 font-medium">Model</th>
                        <th className="text-right py-1 font-medium">Input</th>
                        <th className="text-right py-1 font-medium">Output</th>
                        <th className="text-right py-1 font-medium">Cost</th>
                      </tr>
                    </thead>
                    <tbody>
                      {modelBreakdown.map((m) => (
                        <tr key={`${m.provider}/${m.model}`} className="border-t border-border-subtle">
                          <td className="py-1.5">
                            <div className="font-medium text-text-primary text-xs">{m.model}</div>
                            <div className="text-[10px] text-text-muted">{m.provider}</div>
                          </td>
                          <td className="text-right py-1.5 tabular-nums text-text-secondary">{formatTokens(m.input_tokens)}</td>
                          <td className="text-right py-1.5 tabular-nums text-text-secondary">{formatTokens(m.output_tokens)}</td>
                          <td className="text-right py-1.5 tabular-nums text-text-primary font-medium">{formatMicrocents(m.cost_microcents)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </>
            )}

            <div className="border-t border-border-subtle" />

            {/* Cost summary */}
            <div className="bg-gradient-to-r from-[rgba(52,211,153,0.12)] to-[rgba(20,184,166,0.12)] rounded-[var(--radius)] p-4 md:p-6 border border-[rgba(52,211,153,0.25)]">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2 md:gap-3">
                  <div className="w-8 h-8 md:w-10 md:h-10 rounded-full bg-[rgba(52,211,153,0.15)] flex items-center justify-center flex-shrink-0">
                    <DollarSign className="w-4 h-4 md:w-5 md:h-5 text-green-400" />
                  </div>
                  <div>
                    <p className="text-xs md:text-sm text-text-tertiary">Cost (this month)</p>
                    <p className="text-xl md:text-2xl font-bold text-text-primary tabular-nums">{formatMicrocents(totalSpend)}</p>
                  </div>
                </div>
                {budgetMc > 0 && (
                  <div className="text-right">
                    <span className="text-[11px] font-medium text-text-tertiary">Budget: {formatMicrocents(budgetMc)}</span>
                  </div>
                )}
              </div>
            </div>
            </>)}
          </div>
        </div>

        {/* Machine Info Card */}
        <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
          <div className="p-4 md:p-6 pb-0">
            <div className="flex items-center justify-between mb-1">
              <div className="text-lg md:text-xl font-semibold text-text-primary">Machine Information</div>
            </div>
            <div className="text-xs md:text-sm text-text-tertiary mb-4">Technical details and specifications</div>
          </div>
          <div className="px-4 md:px-6 pb-4 md:pb-6 space-y-0">
            {/* ID */}
            <div className="flex items-start justify-between py-2.5 md:py-3 border-b border-border-subtle">
              <div className="flex items-center gap-2">
                <Server className="w-4 h-4 text-text-muted flex-shrink-0" />
                <span className="text-xs md:text-sm font-medium text-text-secondary">ID</span>
              </div>
              <code className="text-[10px] md:text-[11px] bg-elevated px-2 py-0.5 rounded font-mono text-text-secondary break-all ml-4 max-w-[200px] text-right">
                {machine.id}
              </code>
            </div>

            {/* Size */}
            <div className="flex items-center justify-between py-2.5 md:py-3 border-b border-border-subtle">
              <div className="flex items-center gap-2">
                <Activity className="w-4 h-4 text-text-muted" />
                <span className="text-xs md:text-sm font-medium text-text-secondary">Size</span>
              </div>
              <span className="text-[11px] font-medium px-2 py-0.5 rounded-full bg-blue-500/15 text-blue-400">{size}</span>
            </div>

            {/* Created */}
            <div className="flex items-center justify-between py-2.5 md:py-3 border-b border-border-subtle">
              <div className="flex items-center gap-2">
                <Calendar className="w-4 h-4 text-text-muted" />
                <span className="text-xs md:text-sm font-medium text-text-secondary">Created</span>
              </div>
              <span className="text-xs md:text-sm text-text-primary font-medium">{formatDate(machine.created_at)}</span>
            </div>

            {/* Status */}
            <div className="flex items-center justify-between py-2.5 md:py-3 border-b border-border-subtle">
              <div className="flex items-center gap-2">
                <Activity className="w-4 h-4 text-text-muted" />
                <span className="text-xs md:text-sm font-medium text-text-secondary">Status</span>
              </div>
              <StatusBadge status={machine.status} />
            </div>

            {/* Uptime */}
            <div className="flex items-center justify-between py-2.5 md:py-3 border-b border-border-subtle">
              <div className="flex items-center gap-2">
                <Activity className="w-4 h-4 text-text-muted" />
                <span className="text-xs md:text-sm font-medium text-text-secondary">Uptime</span>
              </div>
              <span className="text-xs md:text-sm tabular-nums text-text-primary font-medium">{formatUptime(machine.started_at)}</span>
            </div>

            {/* Version & Runtime */}
            {(machine.openclaw_version || machine.rootfs_snapshot || machine.version_source === null) && (
              <RuntimeVersionSection machine={machine} accountId={accountId} />
            )}

            {/* Storage */}
            {machine.data_volume_gb !== undefined && (
              <div className="flex items-center justify-between py-2.5 md:py-3">
                <div className="flex items-center gap-2">
                  <HardDrive className="w-4 h-4 text-text-muted" />
                  <span className="text-xs md:text-sm font-medium text-text-secondary">Storage</span>
                </div>
                <span className="text-xs md:text-sm font-semibold tabular-nums text-text-primary">{storageGB} GB</span>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
