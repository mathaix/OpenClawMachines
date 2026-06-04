import { useEffect, useState } from "react";
import { listAdminOpenClawReleases, upgradeMachineOpenClaw } from "../lib/api";
import type { AdminOpenClawRelease } from "../lib/api";
import { useToast } from "./Toast";
import { ConfirmDialog } from "./ConfirmDialog";

type Props = {
  accountId: number;
  machineId: string;
  machineStatus: string | null | undefined;
  currentVersion: string | null | undefined;
  onPinned: () => void;
};

// Superadmin-only control to pin a machine's OpenClaw runtime to any
// registered version (stable or rc). End users see the regular upgrade
// banner, which only surfaces stable. The caller is responsible for
// gating render on isAdmin — this component does not re-check.
export function AdminOpenClawPin({ accountId, machineId, machineStatus, currentVersion, onPinned }: Props) {
  const [open, setOpen] = useState(false);
  const [releases, setReleases] = useState<AdminOpenClawRelease[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [loadingReleases, setLoadingReleases] = useState(false);
  const [pinning, setPinning] = useState(false);
  const [showConfirm, setShowConfirm] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const { toast } = useToast();

  useEffect(() => {
    if (!open || releases.length > 0) return;
    setLoadingReleases(true);
    setLoadError(null);
    listAdminOpenClawReleases()
      .then((resp) => {
        setReleases(resp.releases);
      })
      .catch((err) => {
        setLoadError(err instanceof Error ? err.message : "Failed to load releases");
      })
      .finally(() => setLoadingReleases(false));
  }, [open, releases.length]);

  const handlePin = async () => {
    if (!selected) return;
    setShowConfirm(false);
    setPinning(true);
    // Queue-only: pin without apply_now so the backend's compat checks
    // (rootfs floor, version validation) run before any state change.
    // If the request is rejected, the machine is untouched. Operator
    // stops/starts the VM themselves when ready to actually boot the
    // new version.
    try {
      const resp = await upgradeMachineOpenClaw(accountId, machineId, selected, false, "artifact");
      if (resp.error) {
        toast({ title: "Pin failed", description: resp.error, variant: "error" });
      } else {
        const isRunning = machineStatus === "running";
        toast({
          title: `OpenClaw pinned to ${selected}`,
          description: isRunning
            ? "Stop and start the machine to apply the new version."
            : "Start the machine to use the new version.",
          variant: "success",
        });
        onPinned();
      }
    } catch (err) {
      toast({
        title: "Pin failed",
        description: err instanceof Error ? err.message : "Request failed",
        variant: "error",
      });
    } finally {
      setPinning(false);
    }
  };

  const selectedRelease = releases.find((r) => r.version === selected);
  const channelBadgeClass = (channel: string) =>
    channel === "stable"
      ? "bg-[rgba(34,197,94,0.1)] text-green-500 border-[rgba(34,197,94,0.2)]"
      : channel === "rc"
        ? "bg-[rgba(249,115,22,0.1)] text-orange-500 border-[rgba(249,115,22,0.2)]"
        : "bg-[rgba(148,163,184,0.1)] text-text-secondary border-border";

  return (
    <details
      className="border border-border rounded-[var(--radius-md)] mb-5 bg-[rgba(148,163,184,0.03)]"
      onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary className="cursor-pointer px-4 py-2.5 text-[13px] font-medium text-text-secondary hover:text-text-primary select-none">
        Admin: Pin OpenClaw version
      </summary>
      <div className="px-4 pb-4 pt-1 border-t border-border">
        <p className="text-[12px] text-text-muted mb-3">
          Pin this machine to a specific OpenClaw release across any channel (stable, rc).
          Only visible to superadmins. Currently pinned: {currentVersion || "—"}.
        </p>
        {loadingReleases && (
          <p className="text-[12px] text-text-muted">Loading releases...</p>
        )}
        {loadError && (
          <p className="text-[12px] text-red-400">Error: {loadError}</p>
        )}
        {!loadingReleases && !loadError && releases.length === 0 && open && (
          <p className="text-[12px] text-text-muted">No releases registered yet.</p>
        )}
        {releases.length > 0 && (
          <div className="flex flex-wrap items-center gap-3">
            <select
              value={selected}
              onChange={(e) => setSelected(e.target.value)}
              className="bg-card border border-border rounded-[var(--radius-sm)] px-3 py-1.5 text-[13px] text-text-primary min-w-[280px]"
              disabled={pinning}
            >
              <option value="">Select a version...</option>
              {releases.map((r) => (
                <option key={r.version} value={r.version}>
                  {r.version} ({r.channel}) — {r.created_at.slice(0, 10)}
                </option>
              ))}
            </select>
            {selectedRelease && (
              <span
                className={`inline-flex items-center px-2 py-0.5 rounded-[var(--radius-sm)] border text-[11px] font-medium ${channelBadgeClass(selectedRelease.channel)}`}
              >
                {selectedRelease.channel}
              </span>
            )}
            <button
              onClick={() => setShowConfirm(true)}
              disabled={!selected || pinning || selected === currentVersion}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 text-[13px] font-medium bg-brand-600 text-white rounded-[var(--radius-sm)] hover:bg-brand-700 disabled:opacity-50 transition-colors"
            >
              {pinning ? "Pinning..." : "Pin version"}
            </button>
          </div>
        )}
      </div>
      <ConfirmDialog
        open={showConfirm}
        onOpenChange={setShowConfirm}
        title={`Pin OpenClaw to ${selected}?`}
        description={
          machineStatus === "running"
            ? "The new version will be queued. The machine keeps running on its current version until you stop and start it."
            : "The version will be pinned. Start the machine to apply it."
        }
        confirmLabel="Pin version"
        confirmVariant="primary"
        onConfirm={handlePin}
      />
    </details>
  );
}
