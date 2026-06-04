import { useEffect, useRef, useState, useCallback } from "react";
import {
  listHosts,
  provisionHost,
  destroyHost,
  listHostMachines,
  refreshHostRootfs,
  triggerHostUpdate,
  drainHostUpdate,
  configureHostBrowserStorage,
  setHostMaintenanceMode,
  getLatestVersions,
  getHostLogs,
  getHostVMStats,
  listEnrollmentTokens,
  type Host,
  type HostMachine,
  type VMDiskStats,
  type LatestVersions,
  type EnrollmentToken,
} from "../../lib/api";
import { AdminEnrollHost } from "./AdminEnrollHost";
import { useToast } from "../../components/Toast";

// ── Helpers ────────────────────────────────────────────────

function formatHeartbeat(lastHeartbeat?: string | null): { text: string; dotClass: string } {
  if (!lastHeartbeat) return { text: "never", dotClass: "bg-gray-500" };
  const age = Date.now() - new Date(lastHeartbeat).getTime();
  const seconds = Math.round(age / 1000);
  if (seconds < 60) return { text: `${seconds}s ago`, dotClass: "bg-green-500" };
  if (seconds < 300) return { text: `${Math.round(seconds / 60)}m ago`, dotClass: "bg-yellow-500" };
  return { text: `${Math.round(seconds / 60)}m ago`, dotClass: "bg-gray-500" };
}

function formatSnapshot(source?: string) {
  if (!source) return "\u2014";
  const match = source.match(/(\d{8})-(\d{6})$/);
  if (match) {
    const date = match[1];
    const time = match[2];
    return `${date.slice(4, 6)}/${date.slice(6, 8)} ${time.slice(0, 2)}:${time.slice(2, 4)}`;
  }
  return source;
}

function statusColor(status: string) {
  switch (status) {
    case "ready":
    case "running":
      return "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400";
    case "provisioning":
    case "starting":
      return "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400";
    case "error":
      return "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400";
    case "draining":
      return "bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-400";
    default:
      return "bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-300";
  }
}

function statusDotColor(status: string) {
  switch (status) {
    case "ready":
    case "running":
      return "bg-green-500";
    case "provisioning":
    case "starting":
      return "bg-yellow-500 animate-pulse";
    case "error":
      return "bg-red-500";
    default:
      return "bg-gray-500";
  }
}

// ── Sub-components ─────────────────────────────────────────

function Spinner({ className = "h-4 w-4" }: { className?: string }) {
  return (
    <svg className={`animate-spin ${className}`} viewBox="0 0 24 24" fill="none">
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
      <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
    </svg>
  );
}

function CapacityBar({ label, used, total, unit }: { label: string; used: number; total: number; unit: string }) {
  const pct = total > 0 ? Math.round((used / total) * 100) : 0;
  return (
    <div>
      <div className="flex items-center justify-between text-xs text-gray-500 dark:text-gray-400 mb-1">
        <span>{label}</span>
        <span className="font-mono">
          {used}{unit} / {total}{unit} ({pct}%)
        </span>
      </div>
      <div className="h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
        <div
          className={`h-full rounded-full transition-all ${pct > 80 ? "bg-red-500" : pct > 50 ? "bg-yellow-500" : "bg-green-500"}`}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function StatusBadge({ status, message }: { status: string; message?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`w-2 h-2 rounded-full ${statusDotColor(status)}`} />
      <span
        className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${statusColor(status)}`}
        title={message || undefined}
      >
        {status}
      </span>
    </span>
  );
}

function MetaRow({ label, value, mono = true }: { label: string; value?: string | null; mono?: boolean }) {
  if (!value) return null;
  return (
    <div className="flex items-baseline gap-2">
      <span className="text-xs text-gray-500 dark:text-gray-400 w-28 shrink-0">{label}</span>
      <span className={`text-xs text-gray-700 dark:text-gray-300 ${mono ? "font-mono" : ""} break-all`}>{value}</span>
    </div>
  );
}

/** Check if a specific component version is stale compared to the latest. */
function isComponentStale(hostVersion: string | undefined, latestVersion: string | undefined): "current" | "stale" | "unknown" {
  if (!latestVersion) return "unknown";
  if (!hostVersion) return "unknown";
  return hostVersion === latestVersion ? "current" : "stale";
}

function VersionBadge({ host, latestVersions, isStale }: { host: Host; latestVersions: LatestVersions | null; isStale: boolean }) {
  if (!latestVersions?.agent?.version) {
    return <span className="text-xs text-gray-400">{"\u2014"}</span>;
  }
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
        !host.agent_version
          ? "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400"
          : isStale
            ? "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400"
            : "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400"
      }`}
    >
      {!host.agent_version ? "Unknown" : isStale ? "Update available" : "Current"}
    </span>
  );
}

/** Inline version status indicator for HostDetail info grid rows. */
function VersionStatus({ status }: { status: "current" | "stale" | "unknown" }) {
  if (status === "current") {
    return <span className="inline-flex items-center gap-1 text-green-600 dark:text-green-400 text-xs font-medium"><span>&#10003;</span> current</span>;
  }
  if (status === "stale") {
    return <span className="inline-flex items-center gap-1 text-yellow-600 dark:text-yellow-400 text-xs font-medium"><span>&#9888;</span> update available</span>;
  }
  return <span className="text-xs text-gray-400">&mdash;</span>;
}

// ── Host Card (left panel) ─────────────────────────────────

function HostCard({
  host,
  selected,
  latestVersions,
  isStale,
  onClick,
}: {
  host: Host;
  selected: boolean;
  latestVersions: LatestVersions | null;
  isStale: boolean;
  onClick: () => void;
}) {
  const heartbeat = formatHeartbeat(host.last_heartbeat);
  const vcpuPct = host.capacity_vcpus > 0 ? Math.round((host.used_vcpus / host.capacity_vcpus) * 100) : 0;
  const memPct = host.capacity_memory_mb > 0 ? Math.round((host.used_memory_mb / host.capacity_memory_mb) * 100) : 0;

  return (
    <button
      onClick={onClick}
      className={`w-full text-left p-3 rounded-lg border transition-all ${
        selected
          ? "border-brand-500 ring-2 ring-brand-500/30 bg-brand-50 dark:bg-brand-900/20"
          : "border-gray-200 dark:border-gray-700 hover:border-gray-300 dark:hover:border-gray-600 bg-white dark:bg-gray-800"
      }`}
    >
      {/* Row 1: name + status */}
      <div className="flex items-center justify-between mb-2">
        <span className="font-mono text-sm font-medium text-gray-900 dark:text-gray-100 truncate mr-2">
          {host.vm_name}
        </span>
        <div className="flex items-center gap-1.5">
          {host.maintenance_mode && (
            <span className="text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400">
              Maint
            </span>
          )}
          <StatusBadge status={host.status} message={host.status_message} />
        </div>
      </div>

      {/* Row 2: zone + type */}
      <div className="text-xs text-gray-500 dark:text-gray-400 mb-2">
        {host.zone} &middot; {host.machine_type}
      </div>

      {/* Capacity bars */}
      <div className="space-y-1.5 mb-2">
        <div>
          <div className="flex items-center justify-between text-[10px] text-gray-400 mb-0.5">
            <span>vCPU</span>
            <span className="font-mono">{host.used_vcpus}/{host.capacity_vcpus}</span>
          </div>
          <div className="h-1.5 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
            <div
              className={`h-full rounded-full ${vcpuPct > 80 ? "bg-red-500" : vcpuPct > 50 ? "bg-yellow-500" : "bg-green-500"}`}
              style={{ width: `${vcpuPct}%` }}
            />
          </div>
        </div>
        <div>
          <div className="flex items-center justify-between text-[10px] text-gray-400 mb-0.5">
            <span>Memory</span>
            <span className="font-mono">{Math.round(host.used_memory_mb / 1024)}/{Math.round(host.capacity_memory_mb / 1024)} GB</span>
          </div>
          <div className="h-1.5 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
            <div
              className={`h-full rounded-full ${memPct > 80 ? "bg-red-500" : memPct > 50 ? "bg-yellow-500" : "bg-green-500"}`}
              style={{ width: `${memPct}%` }}
            />
          </div>
        </div>
      </div>

      {/* Row 3: VMs + heartbeat + version */}
      <div className="flex items-center justify-between text-xs">
        <span className="text-gray-500 dark:text-gray-400">
          {host.machine_count} VM{host.machine_count !== 1 ? "s" : ""}
        </span>
        <span className="inline-flex items-center gap-1">
          <span className={`w-1.5 h-1.5 rounded-full ${heartbeat.dotClass}`} />
          <span className="font-mono text-[10px] text-gray-400">{heartbeat.text}</span>
        </span>
        <VersionBadge host={host} latestVersions={latestVersions} isStale={isStale} />
      </div>
    </button>
  );
}

// ── Machine Card (detail panel) ────────────────────────────

function MachineCard({ machine, diskStats }: { machine: HostMachine; diskStats?: VMDiskStats }) {
  return (
    <div className={`rounded-lg border px-3 py-2.5 text-xs ${statusColor(machine.status)} border-current/10`}>
      <div className="flex items-center justify-between mb-1.5">
        <span className="font-mono font-medium">{machine.name || machine.slug}</span>
        <StatusBadge status={machine.status} />
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-0.5 opacity-80">
        {machine.rootfs_snapshot && (
          <span>Image: <span className="font-mono">{formatSnapshot(machine.rootfs_snapshot)}</span></span>
        )}
        {machine.openclaw_version && (
          <span>OCM: <span className="font-mono">{machine.openclaw_version}</span></span>
        )}
        <span>Created: <span className="font-mono">{new Date(machine.created_at).toLocaleDateString()}</span></span>
        {machine.started_at && (
          <span>Started: <span className="font-mono">{new Date(machine.started_at).toLocaleString()}</span></span>
        )}
        {machine.vm_ip && <span>IP: <span className="font-mono">{machine.vm_ip}</span></span>}
      </div>
      {diskStats && diskStats.disk_total_mb > 0 && (() => {
        const pct = Math.round((diskStats.disk_used_mb / diskStats.disk_total_mb) * 100);
        const usedGB = (diskStats.disk_used_mb / 1024).toFixed(1);
        const totalGB = (diskStats.disk_total_mb / 1024).toFixed(1);
        return (
          <div className="mt-1.5">
            <div className="flex items-center justify-between text-[10px] opacity-70 mb-0.5">
              <span>Disk</span>
              <span className="font-mono">{usedGB} / {totalGB} GB ({pct}%)</span>
            </div>
            <div className="h-1.5 bg-black/10 dark:bg-white/10 rounded-full overflow-hidden">
              <div
                className={`h-full rounded-full transition-all ${pct > 90 ? "bg-red-500" : pct > 70 ? "bg-yellow-500" : "bg-green-500"}`}
                style={{ width: `${pct}%` }}
              />
            </div>
          </div>
        );
      })()}
      {!diskStats && machine.data_volume_gb > 0 && (
        <div className="mt-1 opacity-60">{machine.data_volume_gb} GB allocated</div>
      )}
    </div>
  );
}

// ── Detail Panel (right side) ──────────────────────────────

function HostDetail({
  host,
  latestVersions,
  isStale,
  hostMachines,
  vmDiskStats,
  showLogs,
  loadingLogs,
  hostLogs,
  updatingId,
  drainingUpdateId,
  browserStorageConfiguringId,
  refreshingId,
  destroyingId,
  confirmDestroyId,
  onTriggerUpdate,
  onDrainUpdate,
  onConfigureBrowserStorage,
  onToggleMaintenance,
  onRefreshRootfs,
  onDestroy,
  onConfirmDestroy,
  onCancelDestroy,
  onToggleLogs,
}: {
  host: Host;
  latestVersions: LatestVersions | null;
  isStale: boolean;
  hostMachines: HostMachine[] | undefined;
  vmDiskStats: Record<string, VMDiskStats> | undefined;
  showLogs: boolean;
  loadingLogs: boolean;
  hostLogs: string | undefined;
  updatingId: number | null;
  drainingUpdateId: number | null;
  browserStorageConfiguringId: number | null;
  refreshingId: number | null;
  destroyingId: number | null;
  confirmDestroyId: number | null;
  onTriggerUpdate: () => void;
  onDrainUpdate: () => void;
  onConfigureBrowserStorage: (device: string, format: boolean) => void;
  onToggleMaintenance: () => void;
  onRefreshRootfs: () => void;
  onDestroy: () => void;
  onConfirmDestroy: () => void;
  onCancelDestroy: () => void;
  onToggleLogs: () => void;
}) {
  const heartbeat = formatHeartbeat(host.last_heartbeat);
  const [browserStorageDevice, setBrowserStorageDevice] = useState("");
  const [formatBrowserStorage, setFormatBrowserStorage] = useState(false);
  const browserStorage = host.capabilities?.browser_storage;
  const browserStateDir = browserStorage?.browser_state_dir || browserStorage?.state_dir || "\u2014";
  const browserReflink = browserStorage?.reflink_supported;
  const browserStorageConfiguring = browserStorageConfiguringId === host.id;

  return (
    <div className="space-y-6">
      {/* ── Header ── */}
      <div>
        <div className="flex items-center gap-3 mb-1">
          <h2 className="text-xl font-semibold text-gray-900 dark:text-gray-100 font-mono">{host.vm_name}</h2>
          <StatusBadge status={host.status} message={host.status_message} />
          {host.maintenance_mode && (
            <span className="text-xs font-medium px-2 py-0.5 rounded-full bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400">
              Maintenance
            </span>
          )}
        </div>
        <div className="flex items-center gap-4 text-sm text-gray-500 dark:text-gray-400">
          <span className="inline-flex items-center gap-1">
            <span className={`w-1.5 h-1.5 rounded-full ${heartbeat.dotClass}`} />
            {heartbeat.text}
          </span>
          <VersionBadge host={host} latestVersions={latestVersions} isStale={isStale} />
          {isStale && (
            <>
              <button
                onClick={onTriggerUpdate}
                disabled={updatingId === host.id || drainingUpdateId === host.id || host.status !== "ready"}
                className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400 hover:text-amber-800 dark:hover:text-amber-300 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed"
                title={updatingId === host.id ? "Update in progress" : host.status !== "ready" ? "Host unreachable" : "Restart the agent without draining VMs"}
              >
                {updatingId === host.id && <Spinner className="h-3 w-3" />}
                {updatingId === host.id ? "Updating..." : "Update Agent"}
              </button>
              <button
                onClick={onDrainUpdate}
                disabled={updatingId === host.id || drainingUpdateId === host.id || host.status !== "ready"}
                className="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-full bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400 dark:hover:bg-orange-900/50 disabled:opacity-50 disabled:cursor-not-allowed"
                title={drainingUpdateId === host.id ? "Drain update in progress" : host.status !== "ready" ? "Host unreachable" : "Stop running VMs, enter maintenance, and update the agent"}
              >
                {drainingUpdateId === host.id && <Spinner className="h-3 w-3" />}
                {drainingUpdateId === host.id ? "Draining..." : "Drain + Update"}
              </button>
            </>
          )}
          <button
            onClick={onToggleMaintenance}
            className={`text-xs font-medium px-2 py-0.5 rounded-full transition-colors ${
              host.maintenance_mode
                ? "bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400 dark:hover:bg-orange-900/50"
                : "bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-gray-800 dark:text-gray-400 dark:hover:bg-gray-700"
            }`}
            title={host.maintenance_mode ? "Disable maintenance mode — reconciler will resume heartbeat checks" : "Enable maintenance mode — reconciler will skip heartbeat checks"}
          >
            {host.maintenance_mode ? "Exit Maintenance" : "Enter Maintenance"}
          </button>
        </div>
        {host.status === "error" && host.status_message && (
          <p className="text-sm text-red-600 dark:text-red-400 mt-2">{host.status_message}</p>
        )}
      </div>

      {/* ── Info Grid ── */}
      <div className="bg-gray-50 dark:bg-gray-900 rounded-lg p-4 space-y-1.5">
        <MetaRow label="Zone" value={host.zone} />
        <MetaRow label="Machine Type" value={host.machine_type} />
        <MetaRow label="External IP" value={host.external_ip} />
        <MetaRow label="Internal IP" value={host.internal_ip} />
        <MetaRow label="Tunnel" value={host.tunnel_url} />
        <MetaRow label="Source Image" value={host.source_image} />
        <MetaRow label="OCM Version" value={host.openclaw_version} />
        <MetaRow label="Created" value={new Date(host.created_at).toLocaleString()} mono={false} />
        {/* Version comparison */}
        <div className="pt-2 mt-2 border-t border-gray-200 dark:border-gray-700 space-y-1">
          <div className="flex items-baseline gap-2">
            <span className="text-xs text-gray-500 dark:text-gray-400 w-28 shrink-0">Agent</span>
            <span className="text-xs font-mono text-gray-700 dark:text-gray-300">{host.agent_version || "\u2014"}</span>
            <VersionStatus status={isComponentStale(host.agent_version, latestVersions?.agent?.version)} />
          </div>
          <div className="flex items-baseline gap-2">
            <span className="text-xs text-gray-500 dark:text-gray-400 w-28 shrink-0">Rootfs</span>
            <span className="text-xs font-mono text-gray-700 dark:text-gray-300">{host.rootfs_version || "\u2014"}</span>
            <VersionStatus status={isComponentStale(host.rootfs_version, latestVersions?.rootfs?.version)} />
          </div>
          {(host.browser_rootfs_version || latestVersions?.browser_rootfs) && (
            <div className="flex items-baseline gap-2">
              <span className="text-xs text-gray-500 dark:text-gray-400 w-28 shrink-0">Browser Rootfs</span>
              <span className="text-xs font-mono text-gray-700 dark:text-gray-300">{host.browser_rootfs_version || "\u2014"}</span>
              <VersionStatus status={isComponentStale(host.browser_rootfs_version, latestVersions?.browser_rootfs?.version)} />
            </div>
          )}
        </div>
      </div>

      {/* ── Browser Storage ── */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300">Browser Storage</h3>
          <span
            className={`text-xs font-medium px-2 py-0.5 rounded-full ${
              browserReflink
                ? "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400"
                : "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400"
            }`}
          >
            {browserReflink ? "Reflink ready" : "Needs XFS"}
          </span>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 text-xs">
          <div>
            <span className="block text-gray-500 dark:text-gray-400">State dir</span>
            <span className="font-mono text-gray-700 dark:text-gray-300 break-all">{browserStateDir}</span>
          </div>
          <div>
            <span className="block text-gray-500 dark:text-gray-400">Filesystem</span>
            <span className="font-mono text-gray-700 dark:text-gray-300">{browserStorage?.fs_type || "\u2014"}</span>
          </div>
          <div>
            <span className="block text-gray-500 dark:text-gray-400">Mount</span>
            <span className="font-mono text-gray-700 dark:text-gray-300 break-all">{browserStorage?.mount_point || "\u2014"}</span>
          </div>
        </div>
        {browserStorage?.reflink_error && (
          <p className="text-xs text-yellow-700 dark:text-yellow-400 break-all">{browserStorage.reflink_error}</p>
        )}
        <div className="flex flex-col sm:flex-row sm:items-center gap-2">
          <input
            value={browserStorageDevice}
            onChange={(e) => setBrowserStorageDevice(e.target.value)}
            placeholder="/dev/nvme1n1"
            className="flex-1 border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-900 dark:text-gray-200"
          />
          <label className="inline-flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input
              type="checkbox"
              checked={formatBrowserStorage}
              onChange={(e) => setFormatBrowserStorage(e.target.checked)}
              className="rounded border-gray-300 dark:border-gray-600"
            />
            Format
          </label>
          <button
            onClick={() => onConfigureBrowserStorage(browserStorageDevice.trim(), formatBrowserStorage)}
            disabled={browserStorageConfiguring || host.status !== "ready" || !browserStorageDevice.trim()}
            className="inline-flex items-center justify-center gap-1.5 bg-brand-600 text-white px-3 py-2 rounded-lg text-sm font-medium hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed"
            title={host.status !== "ready" ? "Host must be ready" : "Configure this host for experimental browser storage"}
          >
            {browserStorageConfiguring && <Spinner className="h-3 w-3" />}
            {browserStorageConfiguring ? "Configuring..." : "Configure"}
          </button>
        </div>
      </div>

      {/* ── Capacity ── */}
      <div className="space-y-3">
        <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300">Capacity</h3>
        <CapacityBar label="vCPU" used={host.used_vcpus} total={host.capacity_vcpus} unit="" />
        <CapacityBar
          label="Memory"
          used={Math.round(host.used_memory_mb / 1024)}
          total={Math.round(host.capacity_memory_mb / 1024)}
          unit=" GB"
        />
      </div>

      {/* ── Machines ── */}
      <div>
        <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
          Machines ({hostMachines?.length ?? host.machine_count})
        </h3>
        {hostMachines === undefined ? (
          <p className="text-sm text-gray-400">Loading...</p>
        ) : hostMachines.length === 0 ? (
          <p className="text-sm text-gray-400">No machines on this host</p>
        ) : (
          <div className="space-y-2">
            {hostMachines.map((m) => (
              <MachineCard key={m.id} machine={m} diskStats={vmDiskStats?.[m.id]} />
            ))}
          </div>
        )}
      </div>

      {/* ── Logs ── */}
      <div>
        <button
          onClick={onToggleLogs}
          disabled={host.status !== "ready" && host.status !== "running"}
          className="text-sm font-medium text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {showLogs ? "\u25BC Hide Agent Logs" : "\u25B6 Show Agent Logs"}
        </button>
        {showLogs && (
          <div className="mt-2">
            {loadingLogs ? (
              <p className="text-sm text-gray-400">Loading logs...</p>
            ) : (
              <pre className="text-xs bg-gray-900 text-gray-100 p-3 rounded-lg overflow-x-auto max-h-64 overflow-y-auto font-mono">
                {hostLogs || "No logs available"}
              </pre>
            )}
          </div>
        )}
      </div>

      {/* ── Actions ── */}
      <div className="flex items-center gap-3 pt-2 border-t border-gray-200 dark:border-gray-700">
        <button
          onClick={onRefreshRootfs}
          disabled={refreshingId === host.id || host.status !== "ready"}
          className="inline-flex items-center gap-1.5 text-blue-600 dark:text-blue-400 hover:text-blue-800 dark:hover:text-blue-300 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {refreshingId === host.id && <Spinner className="h-3 w-3" />}
          {refreshingId === host.id ? "Refreshing..." : "Refresh Rootfs"}
        </button>

        {confirmDestroyId === host.id ? (
          <span className="inline-flex items-center gap-2">
            <span className="text-sm text-gray-500 dark:text-gray-400">Destroy this host?</span>
            <button
              onClick={onDestroy}
              disabled={destroyingId === host.id}
              className="text-red-700 dark:text-red-400 hover:text-red-900 dark:hover:text-red-300 text-sm font-bold disabled:opacity-50"
            >
              Yes
            </button>
            <button
              onClick={onCancelDestroy}
              className="text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 text-sm font-medium"
            >
              No
            </button>
          </span>
        ) : destroyingId === host.id ? (
          <span className="inline-flex items-center gap-1 text-red-600 dark:text-red-400 text-sm font-medium">
            <Spinner className="h-3 w-3" />
            Destroying...
          </span>
        ) : (
          <button
            onClick={onConfirmDestroy}
            className="text-red-600 dark:text-red-400 hover:text-red-800 dark:hover:text-red-300 text-sm font-medium"
          >
            Destroy
          </button>
        )}
      </div>
    </div>
  );
}

// ── Enrollment Tokens Table ─────────────────────────────────

function EnrollmentTokensTable() {
  const [tokens, setTokens] = useState<EnrollmentToken[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    listEnrollmentTokens()
      .then((data) => setTokens(data || []))
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []);

  if (loading) return <p className="text-sm text-gray-400">Loading tokens...</p>;
  if (tokens.length === 0) return <p className="text-sm text-gray-400">No enrollment tokens.</p>;

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-gray-500 dark:text-gray-400 border-b border-gray-200 dark:border-gray-700">
            <th className="pb-2 pr-4 font-medium">Token</th>
            <th className="pb-2 pr-4 font-medium">Provider</th>
            <th className="pb-2 pr-4 font-medium">Created</th>
            <th className="pb-2 pr-4 font-medium">Expires</th>
            <th className="pb-2 font-medium">Status</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
          {tokens.map((t) => {
            const isExpired = new Date(t.expires_at) < new Date();
            const isUsed = !!t.used_by_host_id;
            return (
              <tr key={t.id} className="text-gray-700 dark:text-gray-300">
                <td className="py-2 pr-4 font-mono text-xs">
                  {t.token.slice(0, 12)}...
                </td>
                <td className="py-2 pr-4 text-xs">{t.provider}</td>
                <td className="py-2 pr-4 text-xs font-mono">
                  {new Date(t.created_at).toLocaleDateString()}
                </td>
                <td className="py-2 pr-4 text-xs font-mono">
                  {new Date(t.expires_at).toLocaleDateString()}
                </td>
                <td className="py-2">
                  {isUsed ? (
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400">
                      Used (host #{t.used_by_host_id})
                    </span>
                  ) : isExpired ? (
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-400">
                      Expired
                    </span>
                  ) : (
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400">
                      Pending
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ── Main Component ─────────────────────────────────────────

export function AdminHosts() {
  const [hosts, setHosts] = useState<Host[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [provisioning, setProvisioning] = useState(false);
  const [machineType, setMachineType] = useState("n2-standard-2");
  const [zone, setZone] = useState("us-central1-b");
  const [destroyingId, setDestroyingId] = useState<number | null>(null);
  const [confirmDestroyId, setConfirmDestroyId] = useState<number | null>(null);
  const [refreshingId, setRefreshingId] = useState<number | null>(null);
  const [selectedHostId, setSelectedHostId] = useState<number | null>(null);
  const [hostMachines, setHostMachines] = useState<Record<number, HostMachine[]>>({});
  const [hostLogs, setHostLogs] = useState<Record<number, string>>({});
  const [vmDiskStats, setVmDiskStats] = useState<Record<number, Record<string, VMDiskStats>>>({});
  const [showLogs, setShowLogs] = useState<number | null>(null);
  const [loadingLogs, setLoadingLogs] = useState(false);
  const [updatingId, setUpdatingId] = useState<number | null>(null);
  const [drainingUpdateId, setDrainingUpdateId] = useState<number | null>(null);
  const [browserStorageConfiguringId, setBrowserStorageConfiguringId] = useState<number | null>(null);
  const [latestVersions, setLatestVersions] = useState<LatestVersions | null>(null);
  const updateStartedAt = useRef<number>(0);
  const [showEnrollModal, setShowEnrollModal] = useState(false);
  const [activeTab, setActiveTab] = useState<"hosts" | "tokens">("hosts");
  const { toast } = useToast();

  const fetchHosts = useCallback(async () => {
    try {
      const data = await listHosts();
      setHosts(data || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load hosts");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchHosts();
    const interval = setInterval(fetchHosts, 5000);
    return () => clearInterval(interval);
  }, [fetchHosts]);

  useEffect(() => {
    getLatestVersions().then(setLatestVersions).catch(() => {});
  }, []);

  // Clear updating spinner when host reports the new version or after 60s timeout
  useEffect(() => {
    if (updatingId === null || !latestVersions?.agent?.version) return;
    const host = hosts.find((h) => h.id === updatingId);
    if (host?.agent_version === latestVersions.agent.version) {
      setUpdatingId(null);
      toast({ title: "Update complete", description: `${host.vm_name} is now current.` });
    } else if (Date.now() - updateStartedAt.current > 60_000) {
      setUpdatingId(null);
    }
  }, [hosts, updatingId, latestVersions, toast]);

  // Fetch machines + disk stats when a host is selected
  const handleSelectHost = useCallback(
    async (hostId: number) => {
      setSelectedHostId(hostId);
      if (!hostMachines[hostId]) {
        try {
          const machines = await listHostMachines(hostId);
          setHostMachines((prev) => ({ ...prev, [hostId]: machines }));
        } catch {
          setHostMachines((prev) => ({ ...prev, [hostId]: [] }));
        }
      }
      const host = hosts.find((h) => h.id === hostId);
      if (host && (host.status === "ready" || host.status === "running")) {
        try {
          const stats = await getHostVMStats(hostId);
          const byId: Record<string, VMDiskStats> = {};
          for (const s of stats) byId[s.machine_id] = s;
          setVmDiskStats((prev) => ({ ...prev, [hostId]: byId }));
        } catch {
          // Agent unreachable — no disk stats
        }
      }
    },
    [hosts, hostMachines],
  );

  const handleProvision = async () => {
    setProvisioning(true);
    setError(null);
    try {
      await provisionHost(machineType, zone);
      toast({ title: "Provisioning started", description: `${machineType} in ${zone} — this takes 3-5 minutes.` });
      await fetchHosts();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to provision host";
      setError(msg);
      toast({ title: "Provisioning failed", description: msg, variant: "error" });
    } finally {
      setProvisioning(false);
    }
  };

  const handleDestroy = async (id: number) => {
    setDestroyingId(id);
    setConfirmDestroyId(null);
    setError(null);
    try {
      await destroyHost(id);
      toast({ title: "Host destruction started" });
      await fetchHosts();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to destroy host";
      setError(msg);
      toast({ title: "Destroy failed", description: msg, variant: "error" });
    } finally {
      setDestroyingId(null);
    }
  };

  const handleRefreshRootfs = async (id: number) => {
    setRefreshingId(id);
    setError(null);
    toast({ title: "Rootfs refresh started", description: "Stopping VMs and refreshing rootfs..." });
    try {
      await refreshHostRootfs(id);
      toast({ title: "Rootfs refreshed", description: "VMs stopped, rootfs updated, machines restarting" });
      await fetchHosts();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to refresh rootfs";
      setError(msg);
      toast({ title: "Refresh failed", description: msg, variant: "error" });
    } finally {
      setRefreshingId(null);
    }
  };

  const handleTriggerUpdate = async (host: Host) => {
    setUpdatingId(host.id);
    updateStartedAt.current = Date.now();
    setError(null);
    toast({ title: "Agent update started", description: `Restarting the agent on ${host.vm_name} without draining VMs` });
    try {
      const result = await triggerHostUpdate(host.id);
      const status = (result as Record<string, unknown>).status;
      if (status === "updated") {
        toast({ title: "Agent update complete", description: `${host.vm_name} updated and back online` });
      } else {
        toast({ title: "Agent update in progress", description: `${host.vm_name} is still updating — check host status` });
      }
      await fetchHosts();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to trigger update";
      setError(msg);
      toast({ title: "Agent update failed", description: msg, variant: "error" });
    } finally {
      setUpdatingId(null);
    }
  };

  const handleDrainUpdate = async (host: Host) => {
    if (!confirm("This will stop all running machines on this host, enter maintenance, and restart the agent. Continue?")) return;

    setDrainingUpdateId(host.id);
    updateStartedAt.current = Date.now();
    setError(null);
    toast({ title: "Drain update started", description: `Stopping VMs and updating ${host.vm_name}... this may take a few minutes` });
    try {
      const result = await drainHostUpdate(host.id);
      const status = (result as Record<string, unknown>).status;
      if (status === "updated") {
        toast({ title: "Drain update complete", description: `${host.vm_name} updated and back online` });
      } else {
        toast({ title: "Drain update in progress", description: `${host.vm_name} is still updating — check host status` });
      }
      await fetchHosts();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to trigger drain update";
      setError(msg);
      toast({ title: "Drain update failed", description: msg, variant: "error" });
    } finally {
      setDrainingUpdateId(null);
    }
  };

  const handleConfigureBrowserStorage = async (host: Host, device: string, format: boolean) => {
    if (!device) return;
    if (format && !confirm(`This will format ${device} as XFS and erase existing data. Continue?`)) return;
    if (!confirm(`This will enter maintenance, configure browser storage on ${host.vm_name}, and restart the agent. Existing machine VMs keep running. Continue?`)) return;

    setBrowserStorageConfiguringId(host.id);
    setError(null);
    toast({ title: "Browser storage configuration started", description: `${host.vm_name} agent will restart; existing machine VMs keep running` });
    try {
      await configureHostBrowserStorage(host.id, { device, format });
      toast({ title: "Browser storage operation queued", description: "Host will report ready after the agent restarts" });
      await fetchHosts();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to configure browser storage";
      setError(msg);
      toast({ title: "Browser storage failed", description: msg, variant: "error" });
    } finally {
      setBrowserStorageConfiguringId(null);
    }
  };

  const handleToggleMaintenance = async (host: Host) => {
    const enabling = !host.maintenance_mode;
    try {
      await setHostMaintenanceMode(host.id, enabling);
      toast({ title: enabling ? "Maintenance enabled" : "Maintenance disabled", description: host.vm_name });
      await fetchHosts();
    } catch (err) {
      toast({ title: "Failed to toggle maintenance", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    }
  };

  const handleShowLogs = async (hostId: number) => {
    if (showLogs === hostId) {
      setShowLogs(null);
      return;
    }
    setShowLogs(hostId);
    setLoadingLogs(true);
    try {
      const resp = await getHostLogs(hostId, 50);
      setHostLogs((prev) => ({ ...prev, [hostId]: resp.logs }));
    } catch (err) {
      setHostLogs((prev) => ({ ...prev, [hostId]: `Error loading logs: ${err}` }));
    } finally {
      setLoadingLogs(false);
    }
  };

  const isStale = (host: Host) => {
    const agentStale = !!(latestVersions?.agent?.version && host.agent_version && host.agent_version !== latestVersions.agent.version);
    const rootfsStale = !!(latestVersions?.rootfs?.version && host.rootfs_version && host.rootfs_version !== latestVersions.rootfs.version);
    const browserStale = !!(latestVersions?.browser_rootfs?.version && host.browser_rootfs_version && host.browser_rootfs_version !== latestVersions.browser_rootfs.version);
    return agentStale || rootfsStale || browserStale;
  };

  const [showInactive, setShowInactive] = useState(false);
  const activeHosts = hosts.filter((h) => h.status === "ready" || h.status === "provisioning");
  const inactiveHosts = hosts.filter((h) => h.status !== "ready" && h.status !== "provisioning");
  const visibleHosts = showInactive ? hosts : activeHosts;

  const selectedHost = visibleHosts.find((h) => h.id === selectedHostId) ?? null;

  if (loading) return <p className="text-gray-500 dark:text-gray-400">Loading hosts...</p>;

  return (
    <div>
      {/* Tab bar + Enroll button */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-1 border-b border-gray-200 dark:border-gray-700">
          <button
            onClick={() => setActiveTab("hosts")}
            className={`px-4 py-2 text-sm font-medium border-b-2 transition ${
              activeTab === "hosts"
                ? "border-brand-500 text-brand-600 dark:text-brand-400"
                : "border-transparent text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300"
            }`}
          >
            Hosts
          </button>
          <button
            onClick={() => setActiveTab("tokens")}
            className={`px-4 py-2 text-sm font-medium border-b-2 transition ${
              activeTab === "tokens"
                ? "border-brand-500 text-brand-600 dark:text-brand-400"
                : "border-transparent text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300"
            }`}
          >
            Enrollment Tokens
          </button>
        </div>
        <button
          onClick={() => setShowEnrollModal(true)}
          className="inline-flex items-center gap-1.5 bg-brand-600 text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-brand-700"
        >
          Enroll Host
        </button>
      </div>

      {/* Enroll Host Modal */}
      {showEnrollModal && (
        <AdminEnrollHost
          onClose={() => setShowEnrollModal(false)}
          onComplete={() => fetchHosts()}
        />
      )}

      {/* Tokens tab */}
      {activeTab === "tokens" && (
        <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-6">
          <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-4">Enrollment Tokens</h3>
          <EnrollmentTokensTable />
        </div>
      )}

      {/* Hosts tab */}
      {activeTab === "hosts" && error && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-red-700 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
          {error}
        </div>
      )}

      {activeTab === "hosts" && (activeHosts.length === 0 && !showInactive ? (
        <div className="text-center py-16">
          <p className="text-gray-500 dark:text-gray-400 mb-4">No hosts provisioned yet.</p>
          <div className="flex items-center justify-center gap-2">
            <select
              value={zone}
              onChange={(e) => setZone(e.target.value)}
              disabled={provisioning}
              className="border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-800 dark:text-gray-200 disabled:opacity-50"
            >
              <option value="us-central1-b">us-central1-b</option>
              <option value="us-east1-b">us-east1-b</option>
              <option value="us-west1-b">us-west1-b</option>
              <option value="us-east4-b">us-east4-b</option>
            </select>
            <select
              value={machineType}
              onChange={(e) => setMachineType(e.target.value)}
              disabled={provisioning}
              className="border border-gray-300 dark:border-gray-600 rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-800 dark:text-gray-200 disabled:opacity-50"
            >
              <option value="n2-standard-2">n2-standard-2 (2 vCPU / 8 GB)</option>
              <option value="n2-standard-4">n2-standard-4 (4 vCPU / 16 GB)</option>
              <option value="n2-standard-8">n2-standard-8 (8 vCPU / 32 GB)</option>
              <option value="c2-standard-4">c2-standard-4 (4 vCPU / 16 GB)</option>
            </select>
            <button
              onClick={handleProvision}
              disabled={provisioning}
              className="inline-flex items-center gap-2 bg-brand-600 text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {provisioning && <Spinner />}
              {provisioning ? "Provisioning..." : "Provision Host"}
            </button>
          </div>
        </div>
      ) : (
        <div className="flex flex-col lg:flex-row gap-4">
          {/* ── Left Panel: Host Cards ── */}
          <div className="w-full lg:w-80 shrink-0 space-y-3">
            {/* Provision controls */}
            <div className="flex items-center gap-2 flex-wrap">
              <select
                value={zone}
                onChange={(e) => setZone(e.target.value)}
                disabled={provisioning}
                className="border border-gray-300 dark:border-gray-600 rounded-lg px-2 py-1.5 text-xs bg-white dark:bg-gray-800 dark:text-gray-200 disabled:opacity-50"
              >
                <option value="us-central1-b">us-central1</option>
                <option value="us-east1-b">us-east1</option>
                <option value="us-west1-b">us-west1</option>
                <option value="us-east4-b">us-east4</option>
              </select>
              <select
                value={machineType}
                onChange={(e) => setMachineType(e.target.value)}
                disabled={provisioning}
                className="flex-1 border border-gray-300 dark:border-gray-600 rounded-lg px-2 py-1.5 text-xs bg-white dark:bg-gray-800 dark:text-gray-200 disabled:opacity-50"
              >
                <option value="n2-standard-2">n2-std-2 (2/8)</option>
                <option value="n2-standard-4">n2-std-4 (4/16)</option>
                <option value="n2-standard-8">n2-std-8 (8/32)</option>
                <option value="c2-standard-4">c2-std-4 (4/16)</option>
              </select>
              <button
                onClick={handleProvision}
                disabled={provisioning}
                className="inline-flex items-center gap-1.5 bg-brand-600 text-white px-3 py-1.5 rounded-lg text-xs font-medium hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed whitespace-nowrap"
              >
                {provisioning && <Spinner className="h-3 w-3" />}
                {provisioning ? "Provisioning..." : "Provision"}
              </button>
            </div>

            {/* Inactive toggle */}
            {inactiveHosts.length > 0 && (
              <button
                onClick={() => setShowInactive(!showInactive)}
                className="text-xs text-gray-400 dark:text-gray-500 hover:text-gray-600 dark:hover:text-gray-400"
              >
                {showInactive ? "Hide" : "Show"} {inactiveHosts.length} inactive host{inactiveHosts.length !== 1 ? "s" : ""}
              </button>
            )}

            {/* Host card list */}
            <div className="space-y-2 lg:max-h-[calc(100vh-12rem)] lg:overflow-y-auto lg:pr-1">
              {visibleHosts.map((host) => (
                <HostCard
                  key={host.id}
                  host={host}
                  selected={selectedHostId === host.id}
                  latestVersions={latestVersions}
                  isStale={isStale(host)}
                  onClick={() => handleSelectHost(host.id)}
                />
              ))}
            </div>
          </div>

          {/* ── Right Panel: Host Detail ── */}
          <div className="flex-1 min-w-0 bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-6">
            {selectedHost ? (
              <HostDetail
                host={selectedHost}
                latestVersions={latestVersions}
                isStale={isStale(selectedHost)}
                hostMachines={hostMachines[selectedHost.id]}
                vmDiskStats={vmDiskStats[selectedHost.id]}
                showLogs={showLogs === selectedHost.id}
                loadingLogs={loadingLogs}
                hostLogs={hostLogs[selectedHost.id]}
                updatingId={updatingId}
                drainingUpdateId={drainingUpdateId}
                browserStorageConfiguringId={browserStorageConfiguringId}
                refreshingId={refreshingId}
                destroyingId={destroyingId}
                confirmDestroyId={confirmDestroyId}
                onTriggerUpdate={() => handleTriggerUpdate(selectedHost)}
                onDrainUpdate={() => handleDrainUpdate(selectedHost)}
                onConfigureBrowserStorage={(device, format) => handleConfigureBrowserStorage(selectedHost, device, format)}
                onToggleMaintenance={() => handleToggleMaintenance(selectedHost)}
                onRefreshRootfs={() => handleRefreshRootfs(selectedHost.id)}
                onDestroy={() => handleDestroy(selectedHost.id)}
                onConfirmDestroy={() => setConfirmDestroyId(selectedHost.id)}
                onCancelDestroy={() => setConfirmDestroyId(null)}
                onToggleLogs={() => handleShowLogs(selectedHost.id)}
              />
            ) : (
              <div className="flex items-center justify-center h-64 text-gray-400 dark:text-gray-500">
                <p className="text-sm">Select a host to view details</p>
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
