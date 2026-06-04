import { useEffect, useState, useCallback, useRef } from "react";
import { Link } from "react-router-dom";
import { listBrowserVMs, createBrowserVM, startBrowserVM, stopBrowserVM, deleteBrowserVM } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { ConfirmDialog } from "../components/ConfirmDialog";
import type { BrowserVM } from "../lib/types";
import {
  BROWSER_VM_SIZES,
  DEFAULT_BROWSER_VM_SIZE,
  type BrowserVMSize,
} from "../lib/browser-vm-sizes";
import {
  BROWSER_IMAGE_OPTIONS,
  browserImageLabelForVM,
  type BrowserImage,
} from "../lib/browser-vm-images";

function statusBadge(status: BrowserVM["status"]) {
  switch (status) {
    case "running":
      return (
        <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-green-500/10 text-green-400 border border-green-500/20">
          Running
        </span>
      );
    case "provisioning":
      return (
        <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-blue-500/10 text-blue-400 border border-blue-500/20">
          Provisioning
        </span>
      );
    case "stopped":
      return (
        <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-yellow-500/10 text-yellow-400 border border-yellow-500/20">
          Stopped
        </span>
      );
    case "error":
      return (
        <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-red-500/10 text-red-400 border border-red-500/20">
          Error
        </span>
      );
    default: {
      // Exhaustiveness check: if BrowserVM["status"] gains a new
      // variant the TS compiler fails this line, forcing a badge
      // update instead of a silent "nothing renders" fall-through.
      const _exhaustive: never = status;
      return _exhaustive;
    }
  }
}

export function BrowserVMsPage() {
  const { account } = useAuth();
  const { toast } = useToast();

  const [vms, setVMs] = useState<BrowserVM[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [actingOn, setActingOn] = useState<string | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [createSize, setCreateSize] = useState<BrowserVMSize>(DEFAULT_BROWSER_VM_SIZE);
  const [browserImage, setBrowserImage] = useState<BrowserImage>("kernel-stable");

  const intervalRef = useRef<ReturnType<typeof setInterval> | undefined>(undefined);

  const fetchVMs = useCallback(async () => {
    if (!account) return;
    try {
      const data = await listBrowserVMs(account.id);
      setVMs(data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load browser VMs");
    } finally {
      setLoading(false);
    }
  }, [account]);

  useEffect(() => {
    fetchVMs();
  }, [fetchVMs]);

  const hasTransitional = vms.some((v) => v.status === "provisioning");

  useEffect(() => {
    clearInterval(intervalRef.current);
    const rate = hasTransitional ? 5000 : 30000;
    intervalRef.current = setInterval(fetchVMs, rate);
    return () => clearInterval(intervalRef.current);
  }, [fetchVMs, hasTransitional]);

  const handleCreate = async () => {
    if (!account) return;
    setCreating(true);
    try {
      const vm = await createBrowserVM(account.id, {
        vcpus: createSize.vcpus,
        memory_mb: createSize.memory_mb,
        browser_image: browserImage,
      });
      setVMs((prev) => [vm, ...prev]);
      toast({
        title: `Browser VM created (${createSize.vcpus} vCPU · ${Math.round(createSize.memory_mb / 1024)} GB)`,
        variant: "success",
      });
    } catch (err) {
      toast({
        title: "Failed to create Browser VM",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setCreating(false);
    }
  };

  const handleStart = async (id: string) => {
    if (!account) return;
    setActingOn(id);
    try {
      const updated = await startBrowserVM(account.id, id);
      setVMs((prev) => prev.map((v) => (v.id === id ? updated : v)));
      toast({ title: "Browser VM starting", variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to start Browser VM",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActingOn(null);
    }
  };

  const handleStop = async (id: string) => {
    if (!account) return;
    setActingOn(id);
    try {
      const updated = await stopBrowserVM(account.id, id);
      setVMs((prev) => prev.map((v) => (v.id === id ? updated : v)));
      toast({ title: "Browser VM stopped", variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to stop Browser VM",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActingOn(null);
    }
  };

  const handleDelete = async (id: string) => {
    if (!account) return;
    setConfirmDeleteId(null);
    setActingOn(id);
    try {
      await deleteBrowserVM(account.id, id);
      setVMs((prev) => prev.filter((v) => v.id !== id));
      toast({ title: "Browser VM deleted", variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to delete Browser VM",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActingOn(null);
    }
  };

  const vmToDelete = vms.find((v) => v.id === confirmDeleteId);

  if (!account) {
    return (
      <div className="max-w-6xl mx-auto">
        <div className="flex items-center justify-between mb-6 md:mb-8">
          <h1 className="text-2xl md:text-3xl font-bold tracking-[-0.02em] text-text-primary">Browser VMs</h1>
        </div>
        <div className="animate-pulse space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-14 bg-card border border-border rounded-[var(--radius-lg)]" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="max-w-6xl mx-auto">
      <div className="flex items-center justify-between mb-6 md:mb-8">
        <div className="flex items-center gap-2">
          <h1 className="text-2xl md:text-3xl font-bold tracking-[-0.02em] text-text-primary">Browser VMs</h1>
          {vms.length > 0 && (
            <span className="text-text-muted text-lg md:text-xl">{vms.length}</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <select
            value={browserImage}
            onChange={(e) => setBrowserImage(e.target.value as BrowserImage)}
            disabled={creating}
            className="text-sm px-3 py-2 rounded-[var(--radius-sm)] border border-border bg-card text-text-secondary disabled:opacity-50 focus:outline-none focus:ring-1 focus:ring-brand-600"
            aria-label="Browser image"
          >
            {BROWSER_IMAGE_OPTIONS.map((option) => (
              <option key={option.id} value={option.id}>{option.label}</option>
            ))}
          </select>
          <select
            value={createSize.id}
            onChange={(e) => {
              const next = BROWSER_VM_SIZES.find((s) => s.id === e.target.value);
              if (next) setCreateSize(next);
            }}
            disabled={creating}
            className="text-sm px-3 py-2 rounded-[var(--radius-sm)] border border-border bg-card text-text-secondary disabled:opacity-50 focus:outline-none focus:ring-1 focus:ring-brand-600"
            aria-label="Browser VM size"
          >
            {BROWSER_VM_SIZES.map((s) => (
              <option key={s.id} value={s.id}>{s.label}</option>
            ))}
          </select>
          <button
            onClick={handleCreate}
            disabled={creating}
            className="bg-brand-600 text-white text-sm md:text-base font-medium px-4 py-2 rounded-[var(--radius-sm)] hover:bg-brand-700 shadow-[0_2px_8px_rgba(234,88,12,0.3)] transition-all hover:shadow-[0_4px_12px_rgba(234,88,12,0.4)] disabled:opacity-50"
          >
            {creating ? "Creating..." : "New Browser VM"}
          </button>
        </div>
      </div>

      {error && <p className="text-red-600 dark:text-red-400 mb-4">Error: {error}</p>}

      {loading ? (
        <div className="animate-pulse space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-14 bg-card border border-border rounded-[var(--radius-lg)]" />
          ))}
        </div>
      ) : vms.length === 0 ? (
        <div className="text-center py-16">
          <p className="text-text-muted mb-4">No browser VMs yet</p>
          <button
            onClick={handleCreate}
            disabled={creating}
            className="bg-brand-600 text-white text-sm md:text-base font-medium px-4 py-2 rounded-[var(--radius-sm)] hover:bg-brand-700 shadow-[0_2px_8px_rgba(234,88,12,0.3)] transition-all hover:shadow-[0_4px_12px_rgba(234,88,12,0.4)] disabled:opacity-50"
          >
            {creating ? "Creating..." : "Create your first Browser VM"}
          </button>
        </div>
      ) : (
        <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-text-tertiary text-xs uppercase tracking-wide">
                  <th className="text-left px-4 py-3 font-medium">Name / Slug</th>
                  <th className="text-left px-4 py-3 font-medium">Status</th>
                  <th className="text-left px-4 py-3 font-medium">Paired With</th>
                  <th className="text-left px-4 py-3 font-medium">Host</th>
                  <th className="text-left px-4 py-3 font-medium">Image</th>
                  <th className="text-left px-4 py-3 font-medium">Resources</th>
                  <th className="text-right px-4 py-3 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {vms.map((vm) => {
                  const isActing = actingOn === vm.id;
                  return (
                    <tr key={vm.id} className="hover:bg-[var(--color-card-hover)] transition-colors">
                      <td className="px-4 py-3">
                        <Link
                          to={`/browser-vms/${vm.id}`}
                          className="font-medium text-text-primary hover:text-brand-500 transition-colors"
                        >
                          {vm.name || vm.slug}
                        </Link>
                        {vm.name && (
                          <p className="text-xs text-text-tertiary font-mono">{vm.slug}</p>
                        )}
                        {vm.error_message && (
                          <p className="text-xs text-red-400 mt-0.5 max-w-xs truncate" title={vm.error_message}>
                            {vm.error_message}
                          </p>
                        )}
                      </td>
                      <td className="px-4 py-3">{statusBadge(vm.status)}</td>
                      <td className="px-4 py-3 text-text-secondary text-xs">
                        {vm.paired_machine ? (
                          <Link
                            to={`/machines/${vm.paired_machine.id}`}
                            className="text-text-primary hover:text-brand-500 transition-colors"
                          >
                            {vm.paired_machine.name}
                          </Link>
                        ) : (
                          <span className="text-text-tertiary">—</span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-text-secondary">
                        {vm.host_id ? (
                          <span className="font-mono text-xs">host-{vm.host_id}</span>
                        ) : (
                          <span className="text-text-tertiary">—</span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-text-secondary text-xs">
                        <span className={vm.desired_rootfs_manifest ? undefined : "text-text-tertiary"}>
                          {browserImageLabelForVM(vm)}
                        </span>
                        {(vm.rootfs_version || vm.desired_rootfs_version) && (
                          <p className="font-mono text-[11px] text-text-tertiary truncate max-w-[160px]" title={vm.rootfs_version || vm.desired_rootfs_version || undefined}>
                            {vm.rootfs_version || vm.desired_rootfs_version}
                          </p>
                        )}
                      </td>
                      <td className="px-4 py-3 text-text-secondary text-xs">
                        {vm.vcpus} vCPU / {Math.round(vm.memory_mb / 1024)} GB
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex items-center justify-end gap-2">
                          {vm.status === "stopped" && (
                            <button
                              onClick={() => handleStart(vm.id)}
                              disabled={isActing}
                              className="text-xs px-3 py-1 rounded bg-brand-600 hover:bg-brand-700 text-white transition-colors disabled:opacity-50"
                            >
                              {isActing ? "..." : "Start"}
                            </button>
                          )}
                          {vm.status === "running" && (
                            <button
                              onClick={() => handleStop(vm.id)}
                              disabled={isActing}
                              className="text-xs px-3 py-1 rounded bg-[var(--color-surface)] hover:bg-[var(--color-surface-hover)] border border-border text-text-secondary transition-colors disabled:opacity-50"
                            >
                              {isActing ? "..." : "Stop"}
                            </button>
                          )}
                          {vm.status === "provisioning" && (
                            <span className="text-xs text-text-tertiary px-3 py-1">Starting...</span>
                          )}
                          <Link
                            to={`/browser-vms/${vm.id}`}
                            className="text-xs px-3 py-1 rounded border border-border text-text-secondary hover:bg-[var(--color-surface-hover)] transition-colors"
                          >
                            Resources
                          </Link>
                          <button
                            onClick={() => setConfirmDeleteId(vm.id)}
                            disabled={isActing || vm.status === "provisioning"}
                            className="text-xs px-3 py-1 rounded border border-red-500/30 text-red-400 hover:bg-red-500/10 transition-colors disabled:opacity-50"
                          >
                            Delete
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={!!confirmDeleteId}
        onOpenChange={(open) => { if (!open) setConfirmDeleteId(null); }}
        title="Delete Browser VM"
        description={`Are you sure you want to delete "${vmToDelete?.name || vmToDelete?.slug}"? This action cannot be undone.`}
        confirmLabel="Delete"
        onConfirm={() => confirmDeleteId && handleDelete(confirmDeleteId)}
        confirmVariant="danger"
      />
    </div>
  );
}
