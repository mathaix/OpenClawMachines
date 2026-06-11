import { useEffect, useState } from "react";
import {
  MessageSquare,
  Server, Calendar, Activity, HardDrive,
  Brain, Plug, Globe, ArrowUpCircle, ArrowDownCircle,
} from "lucide-react";
import type { Machine } from "../../lib/types";
import { listMachineCapabilities, upgradeMachineOpenClaw, rollbackMachineOpenClaw, listOpenClawReleases, upgradeMachineRootfs, rollbackMachineRootfs, listRootfsReleases, listAdminOpenClawReleases, listAdminRootfsReleases } from "../../lib/api";
import type { MachineCapability, RuntimeRelease } from "../../lib/api";
import { useAuth } from "../../lib/auth";
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

// Superadmin sees every release across stable + rc (channel-labelled) so an
// rc pair can be applied to an existing machine; end users only see stable.
// Hermes machines keep the account-scoped lists — the admin endpoints only
// cover openclaw-kind artifacts.
const adminReleaseToRuntimeRelease = (r: { version: string; channel: string; created_at: string }): RuntimeRelease => ({
  version: r.channel === "stable" ? r.version : `${r.version} (${r.channel})`,
  exact_version: r.version,
  channel: r.channel,
  created_at: r.created_at,
});

function RuntimeVersionSection({ machine, accountId }: { machine: Machine; accountId: number }) {
  type RuntimeTarget = "openclaw" | "rootfs";
  const { isAdmin } = useAuth();
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
    const useAdminLists = isAdmin && !isHermesMachine;
    if (!isHermesMachine && !openClawReleasesLoaded) {
      const fetchOpenClaw = useAdminLists
        ? listAdminOpenClawReleases().then((resp) => resp.releases.map(adminReleaseToRuntimeRelease))
        : listOpenClawReleases(accountId, "stable", machineKind);
      fetchOpenClaw.then(setOpenClawReleases).catch(() => {}).finally(() => setOpenClawReleasesLoaded(true));
    }
    if (!rootfsReleasesLoaded) {
      const fetchRootfs = useAdminLists
        ? listAdminRootfsReleases().then((resp) => resp.releases.map(adminReleaseToRuntimeRelease))
        : listRootfsReleases(accountId, "stable", machineKind);
      fetchRootfs.then(setRootfsReleases).catch(() => {}).finally(() => setRootfsReleasesLoaded(true));
    }
  }, [expanded, openClawReleasesLoaded, rootfsReleasesLoaded, accountId, machineKind, isHermesMachine, isAdmin, openClawReleases.length, rootfsReleases.length]);

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
  const [capabilities, setCapabilities] = useState<MachineCapability[]>([]);
  const [capsLoading, setCapsLoading] = useState(true);
  const [capsError, setCapsError] = useState<string | null>(null);
  const sizes = useSizes();

  useEffect(() => {
    setCapsLoading(true);
    setCapsError(null);
    listMachineCapabilities(accountId, machine.id)
      .then((c) => { setCapabilities(c ?? []); setCapsError(null); })
      .catch((err) => setCapsError(err instanceof Error ? err.message : "Failed to load capabilities"))
      .finally(() => setCapsLoading(false));
  }, [accountId, machine.id]);

  const hasChannels = !capsLoading && !capsError && capabilities.some((c) => c.enabled);
  const hasIntegrations = false;
  const hasBrowser = false;
  const isHermes = machine.kind === "hermes";

  const matchedSize = sizes.find((s) => s.vcpus === machine.vcpus && s.memory_mb === machine.memory_mb);
  const size = matchedSize ? matchedSize.label : `${machine.vcpus} vCPU / ${machine.memory_mb / 1024} GB`;

  const storageGB = matchedSize?.disk_gb ?? machine.data_volume_gb ?? 5;

  return (
    <div className="space-y-6">
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

      <div className="grid grid-cols-1 gap-4 md:gap-6">
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
