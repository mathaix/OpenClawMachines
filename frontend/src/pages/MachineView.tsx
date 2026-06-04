import { useEffect, useState, useCallback, useRef } from "react";
import { useParams, useNavigate, Link } from "react-router-dom";
import { MessageSquare, Activity, ArrowUpCircle } from "lucide-react";
import { getMachine, startMachine, stopMachine, deleteMachine, listOpenClawReleases, listRootfsReleases, upgradeMachineRuntime } from "../lib/api";
import { useAuth } from "../lib/auth";
import { StatusBadge } from "../components/StatusBadge";
import { KindBadge } from "../components/KindBadge";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { AdminOpenClawPin } from "../components/AdminOpenClawPin";
import { useToast } from "../components/Toast";
import type { Machine } from "../lib/types";
import { machineInterfaceUrl } from "../lib/machineInterface";
import { OverviewTab } from "./machine-tabs/OverviewTab";
import { ResourcesTab } from "./machine-tabs/ResourcesTab";
import { ModelTab } from "./machine-tabs/ModelTab";
import { ChannelsTab } from "./machine-tabs/ChannelsTab";
import IntegrationsTab from "./machine-tabs/IntegrationsTab";
import { BrowserTab } from "./machine-tabs/BrowserTab";
import { BackupsTab } from "./machine-tabs/BackupsTab";
import { FilesTab } from "./machine-tabs/FilesTab";
import { LogConsole } from "../components/LogConsole";
import { UsageTab } from "./machine-tabs/UsageTab";
import { WebSearchTab } from "./machine-tabs/WebSearchTab";
import { TracesTab } from "./machine-tabs/TracesTab";

type TabId = "overview" | "dashboard" | "resources" | "usage" | "traces" | "model" | "search" | "channels" | "integrations" | "browser" | "files" | "logs" | "backups";

const TABS: { id: TabId; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "dashboard", label: "Dashboard" },
  { id: "resources", label: "Resources" },
  { id: "usage", label: "Usage" },
  { id: "traces", label: "Traces" },
  { id: "model", label: "Model" },
  { id: "search", label: "Web Search" },
  { id: "channels", label: "Channels" },
  { id: "integrations", label: "Integrations" },
  { id: "browser", label: "Browser" },
  { id: "files", label: "Files" },
  { id: "logs", label: "Logs" },
  { id: "backups", label: "Backups" },
];

const VALID_TABS = new Set<string>(TABS.map((t) => t.id));

function isValidTab(tab: string | undefined): tab is TabId {
  return !!tab && VALID_TABS.has(tab);
}

function HermesDashboardFrame({ machine, accountId, accountSlug }: { machine: Machine; accountId: number; accountSlug?: string }) {
  const [loading, setLoading] = useState(true);
  if (machine.status !== "running") {
    return (
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-5">
          <p className="text-[13px] md:text-[14px] text-text-secondary">Start the machine to open the Hermes dashboard.</p>
        </div>
      </div>
    );
  }
  const dashboardUrl = machineInterfaceUrl(accountSlug, accountId, machine);
  return (
    <div className="bg-card border border-border rounded-[var(--radius)] overflow-hidden shadow-card relative" style={{ height: "calc(100vh - 260px)", minHeight: "560px" }}>
      {loading && (
        <div className="absolute inset-0 flex items-center justify-center bg-card z-10">
          <div className="h-6 w-6 rounded-full border-2 border-brand-500 border-t-transparent animate-spin" />
        </div>
      )}
      <iframe
        src={dashboardUrl}
        onLoad={() => setLoading(false)}
        className="w-full h-full border-0"
        title="Hermes Dashboard"
      />
    </div>
  );
}

export function MachineView() {
  const { id, tab } = useParams<{ id: string; tab?: string }>();
  const navigate = useNavigate();
  const { account, isAdmin } = useAuth();
  const [machine, setMachine] = useState<Machine | null>(null);
  const [loading, setLoading] = useState(true);
  const [acting, setActing] = useState<"" | "starting" | "stopping" | "deleting">("");
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const { toast } = useToast();
  const [openclawReleases, setOpenclawReleases] = useState<{ version: string; exact_version: string }[]>([]);
  const [rootfsReleases, setRootfsReleases] = useState<{ version: string; exact_version: string }[]>([]);
  const latestRelease = openclawReleases[0] ?? null;
  const latestRootfsRelease = rootfsReleases[0] ?? null;
  const [upgrading, setUpgrading] = useState(false);
  const [upgradeDismissed, setUpgradeDismissed] = useState(false);

  const accountId = account?.id;
  const requestedTab: TabId = isValidTab(tab) ? tab : "overview";

  const handleTabChange = (tabId: string) => {
    navigate(`/machines/${id}/${tabId}`, { replace: true });
  };

  const fetchMachine = useCallback(async () => {
    if (!id || !accountId) return;
    try {
      const data = await getMachine(accountId, id);
      setMachine(data);
    } catch (err) {
      setFetchError(err instanceof Error ? err.message : "Failed to load machine");
    } finally {
      setLoading(false);
    }
  }, [id, accountId]);

  // Initial fetch
  useEffect(() => {
    fetchMachine();
  }, [fetchMachine]);

  // Fetch stable release history for openclaw + rootfs. The list is kept (not just
  // the first element) so the banner can tell the difference between "running an
  // older stable" and "running a newer non-stable (RC/dev)" — the latter shouldn't
  // offer a downgrade disguised as an upgrade.
  useEffect(() => {
    if (!accountId || !machine) return;
    const machineKind = machine.kind ?? "openclaw";
    if (machineKind !== "openclaw") {
      setOpenclawReleases([]);
      setRootfsReleases([]);
      return;
    }
    Promise.all([
      listOpenClawReleases(accountId, "stable", machineKind).catch(() => [] as { version: string; exact_version: string }[]),
      listRootfsReleases(accountId, "stable", machineKind).catch(() => [] as { version: string; exact_version: string }[]),
    ]).then(([openclaw, rootfs]) => {
      setOpenclawReleases(openclaw);
      setRootfsReleases(rootfs);
    });
  }, [accountId, machine?.id, machine?.kind]);

  // Poll while booting (fallback if SSE disconnects)
  const isBooting = machine?.status === "provisioning" || machine?.status === "starting";
  useEffect(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }

    if (isBooting) {
      pollRef.current = setInterval(fetchMachine, 5000);
    }

    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
      }
    };
  }, [machine?.status, fetchMachine, isBooting]);

  const handleStart = async () => {
    if (!id || !accountId) return;
    setActing("starting");
    try {
      await startMachine(accountId, id);
      await fetchMachine();
      toast({ title: "Machine starting", variant: "success" });
    } catch (err) {
      toast({ title: "Failed to start", description: err instanceof Error ? err.message : "Failed to start", variant: "error" });
    } finally {
      setActing("");
    }
  };

  const handleStop = async () => {
    if (!id || !accountId) return;
    setActing("stopping");
    try {
      await stopMachine(accountId, id);
      await fetchMachine();
      toast({ title: "Machine stopped", variant: "success" });
    } catch (err) {
      toast({ title: "Failed to stop", description: err instanceof Error ? err.message : "Failed to stop", variant: "error" });
    } finally {
      setActing("");
    }
  };

  const handleDelete = async () => {
    if (!id || !accountId) return;
    setActing("deleting");
    try {
      await deleteMachine(accountId, id);
      toast({ title: "Machine deleted", variant: "success" });
      navigate("/dashboard");
    } catch (err) {
      toast({ title: "Failed to delete", description: err instanceof Error ? err.message : "Failed to delete", variant: "error" });
      setShowDeleteConfirm(false);
    } finally {
      setActing("");
    }
  };

  const handleUpgrade = async () => {
    if (!id || !accountId || !latestRelease || !latestRootfsRelease) return;
    setUpgrading(true);
    try {
      // Stop the machine first if it's running — the new versions take effect on next start.
      if (machine?.status === "running") {
        toast({ title: "Stopping machine for upgrade...", variant: "success" });
        await stopMachine(accountId, id);
        await new Promise((r) => setTimeout(r, 2000));
      }
      const resp = await upgradeMachineRuntime(
        accountId,
        id,
        latestRootfsRelease.exact_version,
        latestRelease.exact_version,
      );
      if (resp.error) {
        toast({ title: "Upgrade failed", description: resp.error, variant: "error" });
      } else {
        setUpgradeDismissed(true);
        toast({
          title: "Upgrade queued",
          description: "Start the machine to use the new versions",
          variant: "success",
        });
        await fetchMachine();
      }
    } catch (err) {
      toast({ title: "Upgrade failed", description: err instanceof Error ? err.message : "Request failed", variant: "error" });
    } finally {
      setUpgrading(false);
    }
  };

  // "Behind latest stable" is only true when the current version appears in the
  // stable list at index > 0 (it IS stable, just an older one). If current isn't
  // in the list at all, it's an RC/dev/one-off build — offering to "upgrade" to
  // the latest stable would be a downgrade, so don't show the banner in that case.
  const isBehindLatestStable = (current: string, list: { exact_version: string }[]): boolean => {
    if (!current || list.length === 0) return false;
    const idx = list.findIndex((r) => r.exact_version === current);
    return idx > 0;
  };

  const currentOpenclaw = machine?.actual_openclaw_version || machine?.resolved_openclaw_version || "";
  const desiredOpenclaw = machine?.desired_openclaw_version || "";
  const currentRootfs = machine?.actual_rootfs_version || machine?.resolved_rootfs_version || "";
  const desiredRootfs = machine?.desired_rootfs_version || "";
  const openclawAlreadyTargetingLatest = !!(latestRelease && desiredOpenclaw && latestRelease.exact_version === desiredOpenclaw);
  const rootfsAlreadyTargetingLatest = !!(latestRootfsRelease && desiredRootfs && latestRootfsRelease.exact_version === desiredRootfs);
  const openclawBehind = !openclawAlreadyTargetingLatest && isBehindLatestStable(currentOpenclaw, openclawReleases);
  const rootfsBehind = !rootfsAlreadyTargetingLatest && isBehindLatestStable(currentRootfs, rootfsReleases);
  const isOpenClawMachine = (machine?.kind ?? "openclaw") === "openclaw";
  const isHermesMachine = machine?.kind === "hermes";
  const activeTab: TabId = requestedTab === "dashboard" && !isHermesMachine ? "overview" : requestedTab;
  const upgradeAvailable = isOpenClawMachine && !upgradeDismissed && (openclawBehind || rootfsBehind);

  // Loading state
  if (!account || loading) {
    return (
      <div className="max-w-5xl mx-auto px-4 py-8">
        <div className="h-4 w-24 bg-[rgba(255,255,255,0.06)] rounded animate-pulse mb-6" />
        <div className="h-7 w-48 bg-[rgba(255,255,255,0.06)] rounded animate-pulse mb-2" />
        <div className="h-5 w-32 bg-[rgba(255,255,255,0.04)] rounded animate-pulse mb-8" />
        <div className="h-10 bg-[rgba(255,255,255,0.04)] rounded animate-pulse mb-6" />
        <div className="h-64 bg-card border border-border rounded-[var(--radius-lg)] animate-pulse" />
      </div>
    );
  }

  if (fetchError && !machine) {
    return (
      <div className="max-w-5xl mx-auto px-4 py-8">
        <p className="text-red-400 text-sm">Error: {fetchError}</p>
      </div>
    );
  }

  if (!machine) {
    return (
      <div className="max-w-5xl mx-auto px-4 py-8">
        <p className="text-text-secondary text-sm">Machine not found.</p>
      </div>
    );
  }

  return (
    <div className="max-w-7xl mx-auto px-4 md:px-6 py-4 md:py-8">
      {/* Back link */}
      <Link
        to="/dashboard"
        className="inline-flex items-center gap-2 text-sm text-text-tertiary hover:text-text-secondary mb-4 md:mb-6 transition-colors"
      >
        &larr; <span>Dashboard</span>
      </Link>

      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 mb-6 md:mb-8">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl md:text-3xl font-bold text-text-primary">{machine.name}</h1>
          <KindBadge kind={machine.kind} />
          <StatusBadge status={machine.status} />
        </div>

        {/* Action buttons */}
        <div className="flex items-center gap-2 flex-wrap">
          {machine.status === "stopped" && (
            <>
              <button
                onClick={handleStart}
                disabled={!!acting}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium bg-brand-600 text-white rounded-[var(--radius-sm)] hover:bg-brand-700 disabled:opacity-50 transition-colors"
              >
                {acting === "starting" ? "Starting\u2026" : "Start"}
              </button>
              <button
                onClick={() => setShowDeleteConfirm(true)}
                disabled={!!acting}
                className="px-3 py-1.5 text-sm font-medium text-red-400 border border-[rgba(248,113,113,0.15)] rounded-[var(--radius-sm)] hover:bg-[rgba(248,113,113,0.06)] disabled:opacity-50 transition-colors"
              >
                {acting === "deleting" ? "Deleting\u2026" : "Delete"}
              </button>
            </>
          )}

          {machine.status === "running" && (
            <>
              {isHermesMachine ? (
                <>
                  <Link
                    to={`/chat/${id}`}
                    className="inline-flex items-center gap-1.5 px-3.5 py-2 text-sm font-semibold bg-brand-600 text-white rounded-[var(--radius-sm)] hover:bg-brand-700 shadow-[0_2px_8px_rgba(234,88,12,0.3)] hover:shadow-[0_4px_12px_rgba(234,88,12,0.4)] transition-all"
                  >
                    <MessageSquare className="w-4 h-4" />
                    Webchat
                  </Link>
                  <Link
                    to={`/workspace/${id}`}
                    className="inline-flex items-center gap-1.5 px-3.5 py-2 text-sm font-medium text-text-secondary border border-border rounded-[var(--radius-sm)] hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)] hover:border-border-hover transition-all"
                  >
                    <Activity className="w-4 h-4" />
                    Terminal
                  </Link>
                </>
              ) : (
                <>
                  <Link
                    to={`/chat/${id}`}
                    className="inline-flex items-center gap-1.5 px-3.5 py-2 text-sm font-medium text-text-secondary border border-border rounded-[var(--radius-sm)] hover:text-text-primary hover:bg-[rgba(255,255,255,0.04)] hover:border-border-hover transition-all"
                  >
                    <MessageSquare className="w-4 h-4" />
                    Chat
                  </Link>
                  <Link
                    to={`/workspace/${id}`}
                    className="inline-flex items-center gap-1.5 px-3.5 py-2 text-sm font-semibold bg-brand-600 text-white rounded-[var(--radius-sm)] hover:bg-brand-700 shadow-[0_2px_8px_rgba(234,88,12,0.3)] hover:shadow-[0_4px_12px_rgba(234,88,12,0.4)] transition-all"
                  >
                    <Activity className="w-4 h-4" />
                    Workspace
                  </Link>
                </>
              )}
              <button
                onClick={handleStop}
                disabled={!!acting}
                className="px-3 py-1.5 text-sm font-medium text-red-400 border border-[rgba(248,113,113,0.15)] rounded-[var(--radius-sm)] hover:bg-[rgba(248,113,113,0.06)] disabled:opacity-50 transition-colors"
              >
                {acting === "stopping" ? "Stopping\u2026" : "Stop"}
              </button>
            </>
          )}

          {isBooting && (
            <button
              onClick={handleStop}
              disabled={!!acting}
              className="px-3 py-1.5 text-sm font-medium text-red-400 border border-[rgba(248,113,113,0.15)] rounded-[var(--radius-sm)] hover:bg-[rgba(248,113,113,0.06)] disabled:opacity-50 transition-colors"
            >
              {acting === "stopping" ? "Stopping\u2026" : "Stop"}
            </button>
          )}

          {machine.status === "error" && (
            <>
              <button
                onClick={handleStart}
                disabled={!!acting}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium bg-brand-600 text-white rounded-[var(--radius-sm)] hover:bg-brand-700 disabled:opacity-50 transition-colors"
              >
                {acting === "starting" ? "Starting\u2026" : "Retry"}
              </button>
              <button
                onClick={() => setShowDeleteConfirm(true)}
                disabled={!!acting}
                className="px-3 py-1.5 text-sm font-medium text-red-400 border border-[rgba(248,113,113,0.15)] rounded-[var(--radius-sm)] hover:bg-[rgba(248,113,113,0.06)] disabled:opacity-50 transition-colors"
              >
                {acting === "deleting" ? "Deleting\u2026" : "Delete"}
              </button>
            </>
          )}
        </div>
      </div>

      {/* Error banner */}
      {machine.status === "error" && machine.status_message && (
        <div className="bg-[rgba(248,113,113,0.06)] border border-[rgba(248,113,113,0.15)] text-red-400 text-[13px] rounded-[var(--radius-md)] px-4 py-3 mb-5">
          {machine.status_message}
        </div>
      )}

      {/* Upgrade banner — fires when openclaw and/or rootfs is behind latest stable. */}
      {upgradeAvailable && machine && (
        <div className="bg-[rgba(249,115,22,0.06)] border border-[rgba(249,115,22,0.2)] rounded-[var(--radius-md)] px-4 py-3 mb-5 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 rounded-full bg-brand-600/10 flex items-center justify-center flex-shrink-0">
              <ArrowUpCircle className="w-4 h-4 text-brand-500" />
            </div>
            <div>
              <p className="text-[13px] font-medium text-text-primary">
                {openclawBehind && rootfsBehind
                  ? `Update available — OpenClaw ${latestRelease!.version} and runtime ${latestRootfsRelease!.version}`
                  : openclawBehind
                    ? `Update available — OpenClaw ${latestRelease!.version}`
                    : `Update available — runtime ${latestRootfsRelease!.version}`}
              </p>
              <p className="text-[11px] text-text-secondary">
                {openclawBehind && `Currently running OpenClaw ${currentOpenclaw || "—"}`}
                {openclawBehind && rootfsBehind && ", "}
                {rootfsBehind && `runtime ${currentRootfs || "—"}`}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => setUpgradeDismissed(true)}
              className="text-[12px] text-text-muted hover:text-text-secondary px-2 py-1 transition-colors"
            >
              Dismiss
            </button>
            <button
              onClick={handleUpgrade}
              disabled={upgrading || !latestRelease || !latestRootfsRelease}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 text-[13px] font-medium bg-brand-600 text-white rounded-[var(--radius-sm)] hover:bg-brand-700 disabled:opacity-50 transition-colors"
            >
              {upgrading ? "Upgrading..." : "Upgrade"}
            </button>
          </div>
        </div>
      )}

      {/* Superadmin-only OpenClaw version pin (any channel: stable, rc) */}
      {isAdmin && isOpenClawMachine && accountId && id && (
        <AdminOpenClawPin
          accountId={accountId}
          machineId={id}
          machineStatus={machine?.status}
          currentVersion={machine?.actual_openclaw_version || machine?.resolved_openclaw_version}
          onPinned={fetchMachine}
        />
      )}

      {/* Provisioning spinner modal */}
      {isBooting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="absolute inset-0 bg-black/50 backdrop-blur-sm" />
          <div className="relative bg-card border border-border rounded-[var(--radius-lg)] p-8 shadow-modal animate-modal-in text-center">
            <div className="h-10 w-10 mx-auto mb-4 border-[3px] border-border border-t-brand-500 rounded-full animate-spin" />
            <p className="text-[15px] font-medium text-text-primary">Starting machine...</p>
            <p className="text-[13px] text-text-secondary mt-1">{machine.name}</p>
          </div>
        </div>
      )}

      {/* Tab bar */}
      <div className="border-b border-border mb-6 md:mb-8">
        <div className="overflow-x-auto -mx-4 px-4 md:mx-0 md:px-0">
          <nav className="flex gap-0 min-w-max md:min-w-0">
            {TABS.filter((tabItem) => machine.kind === "hermes" || tabItem.id !== "dashboard").map((tabItem) => (
              <button
                key={tabItem.id}
                onClick={() => handleTabChange(tabItem.id)}
                className={`px-4 py-2.5 text-sm font-medium transition-colors -mb-px whitespace-nowrap ${
                  activeTab === tabItem.id
                    ? "border-b-2 border-brand-500 text-text-primary"
                    : "text-text-secondary hover:text-text-primary"
                }`}
              >
                {tabItem.label}
              </button>
            ))}
          </nav>
        </div>
      </div>

      {/* Tab content */}
      {activeTab === "overview" && accountId && (
        <OverviewTab machine={machine} accountId={accountId} onTabChange={handleTabChange} />
      )}
      {activeTab === "dashboard" && accountId && isHermesMachine && (
        <HermesDashboardFrame machine={machine} accountId={accountId} accountSlug={account?.slug} />
      )}
      {activeTab === "resources" && accountId && (
        <ResourcesTab kind="machine" vm={machine} accountId={accountId} />
      )}
      {activeTab === "usage" && accountId && (
        <UsageTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "traces" && accountId && (
        <TracesTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "model" && accountId && (
        <ModelTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "search" && accountId && (
        <WebSearchTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "channels" && accountId && (
        <ChannelsTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "integrations" && (
        <IntegrationsTab machine={machine} />
      )}
      {activeTab === "browser" && accountId && (
        <BrowserTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "files" && accountId && (
        <FilesTab machine={machine} accountId={accountId} />
      )}
      {activeTab === "logs" && accountId && id && (
        <div className="bg-card border border-border rounded-[var(--radius)] overflow-hidden" style={{ height: "calc(100vh - 280px)", minHeight: "400px" }}>
          {machine.status === "running" ? (
            <LogConsole accountId={accountId} machineId={id} accountSlug={account?.slug} machineSlug={machine?.slug} className="h-full" />
          ) : (
            <div className="p-6">
              <p className="text-[13px] text-text-secondary">Start the machine to view logs.</p>
            </div>
          )}
        </div>
      )}
      {activeTab === "backups" && accountId && (
        <BackupsTab machine={machine} accountId={accountId} onRefresh={fetchMachine} />
      )}

      {/* Delete confirmation dialog */}
      <ConfirmDialog
        open={showDeleteConfirm}
        onOpenChange={setShowDeleteConfirm}
        title="Delete Machine"
        description={`Are you sure you want to delete "${machine.name}"? All workspace data and secrets will be permanently deleted. This action cannot be undone.`}
        confirmLabel="Delete"
        confirmVariant="danger"
        onConfirm={handleDelete}
        confirmText={machine.name}
      />
    </div>
  );
}
