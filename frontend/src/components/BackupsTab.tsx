import { useState, useEffect, useCallback } from "react";
import { listMachineBackups, createMachineBackup, restoreMachineBackup, deleteMachineBackup, downloadMachineBackup, updateMachine } from "../lib/api";
import { useToast } from "./Toast";
import type { Machine, Backup } from "../lib/types";

interface BackupsTabProps {
  accountId: number;
  machine: Machine;
  onRefresh: () => void;
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`;
}

export function BackupsTab({ accountId, machine, onRefresh }: BackupsTabProps) {
  const [backups, setBackups] = useState<Backup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [actionId, setActionId] = useState<number | null>(null);
  const [togglingBackups, setTogglingBackups] = useState(false);
  const [confirmRestore, setConfirmRestore] = useState<number | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<number | null>(null);
  const [deletedIds, setDeletedIds] = useState<Set<number>>(new Set());
  const { toast } = useToast();

  const isStopped = machine.status === "stopped";

  const fetchBackups = useCallback(async () => {
    try {
      const data = await listMachineBackups(accountId, machine.id);
      setBackups(data || []);
      setError(null);
      // Only clear tombstones for IDs no longer in server response
      setDeletedIds((prev) => {
        const serverIds = new Set((data || []).map((b: Backup) => b.id));
        const next = new Set<number>();
        for (const id of prev) {
          if (serverIds.has(id)) next.add(id); // still in server, keep suppressing
        }
        return next.size === 0 ? new Set() : next;
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load backups");
    } finally {
      setLoading(false);
    }
  }, [accountId, machine.id]);

  useEffect(() => {
    fetchBackups();
  }, [fetchBackups]);

  const handleToggleBackups = async () => {
    setTogglingBackups(true);
    try {
      await updateMachine(accountId, machine.id, { backups_enabled: !machine.backups_enabled });
      onRefresh();
      toast({
        title: machine.backups_enabled ? "Backups disabled" : "Backups enabled",
        variant: "default",
      });
    } catch (err) {
      toast({
        title: "Failed to update backups setting",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setTogglingBackups(false);
    }
  };

  const handleCreate = async () => {
    setCreating(true);
    try {
      await createMachineBackup(accountId, machine.id);
      toast({ title: "Backup created", variant: "default" });
      await fetchBackups();
    } catch (err) {
      toast({
        title: "Failed to create backup",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setCreating(false);
    }
  };

  const handleRestore = async (backupId: number) => {
    setConfirmRestore(null);
    setActionId(backupId);
    try {
      await restoreMachineBackup(accountId, machine.id, backupId);
      toast({ title: "Backup restored", variant: "default" });
      onRefresh();
    } catch (err) {
      toast({
        title: "Failed to restore backup",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionId(null);
    }
  };

  const handleDelete = async (backupId: number) => {
    setConfirmDelete(null);
    setActionId(backupId);
    try {
      await deleteMachineBackup(accountId, machine.id, backupId);
      toast({ title: "Backup deleted", variant: "default" });
      // Optimistic: add to tombstone and let filter hide it immediately
      setDeletedIds((prev) => new Set(prev).add(backupId));
      // Background fetch for server truth
      fetchBackups();
    } catch (err) {
      toast({
        title: "Failed to delete backup",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionId(null);
    }
  };

  const handleDownload = async (backupId: number) => {
    setActionId(backupId);
    try {
      await downloadMachineBackup(accountId, machine.id, backupId);
      toast({ title: "Download started", variant: "default" });
    } catch (err) {
      toast({
        title: "Failed to download backup",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActionId(null);
    }
  };

  if (loading) {
    return (
      <div className="bg-card border border-border rounded-[var(--radius)] p-6 shadow-card">
        <div className="h-4 w-32 bg-elevated rounded animate-pulse" />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {error && (
        <div className="bg-red-500/10 border border-red-500/30 text-red-400 text-[13px] rounded-[var(--radius)] p-3">
          {error}
        </div>
      )}

      {/* Enable/Disable toggle */}
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-lg md:text-xl font-semibold text-text-primary mb-1">Automatic Backups</p>
              <p className="text-xs md:text-sm text-text-tertiary">
                Automatically back up your machine data volume.
              </p>
            </div>
            <button
              onClick={handleToggleBackups}
              disabled={togglingBackups}
              className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-brand-500 focus:ring-offset-2 focus:ring-offset-deep disabled:opacity-50 flex-shrink-0 ${
                machine.backups_enabled ? "bg-brand-600" : "bg-[var(--border-hover)]"
              }`}
            >
              <span
                className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                  machine.backups_enabled ? "translate-x-6" : "translate-x-1"
                }`}
              />
            </button>
          </div>
        </div>
      </div>

      {/* Backup list */}
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="flex items-center justify-between px-4 md:px-5 py-3 md:py-4 border-b border-border-subtle">
          <p className="text-lg md:text-xl font-semibold text-text-primary">Backups</p>
          <div className="relative group">
            <button
              onClick={handleCreate}
              disabled={creating || !isStopped}
              className="text-xs md:text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 text-white hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {creating ? "Creating..." : "Create Backup"}
            </button>
            {!isStopped && (
              <div className="absolute right-0 top-full mt-1 z-10 w-48 bg-elevated text-text-secondary text-[11px] rounded-[var(--radius-sm)] px-2.5 py-1.5 opacity-0 group-hover:opacity-100 transition-opacity pointer-events-none shadow-elevated border border-border">
                Machine must be stopped to create a backup.
              </div>
            )}
          </div>
        </div>

        {backups.filter((b) => !deletedIds.has(b.id)).length === 0 ? (
          <div className="p-6 md:p-8">
            <p className="text-[13px] text-text-tertiary">No backups yet.</p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead>
                <tr className="text-[11px] text-text-muted uppercase tracking-wider border-b border-border-subtle">
                  <th className="text-left px-4 md:px-5 py-2.5 font-medium">ID</th>
                  <th className="text-left px-4 md:px-5 py-2.5 font-medium">Timestamp</th>
                  <th className="text-left px-4 md:px-5 py-2.5 font-medium">Size</th>
                  <th className="text-left px-4 md:px-5 py-2.5 font-medium">Trigger</th>
                  <th className="text-right px-4 md:px-5 py-2.5 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-subtle">
                {backups.filter((b) => !deletedIds.has(b.id)).map((backup) => {
                  const isActing = actionId === backup.id;
                  return (
                    <tr key={backup.id} className="hover:bg-card-hover transition-colors">
                      <td className="px-4 md:px-5 py-3 font-mono text-[11px] text-text-tertiary">
                        #{backup.id}
                      </td>
                      <td className="px-4 md:px-5 py-3 text-text-secondary">
                        {new Date(backup.created_at).toLocaleString()}
                      </td>
                      <td className="px-4 md:px-5 py-3 text-text-secondary tabular-nums">
                        {formatBytes(backup.compressed_bytes || backup.size_bytes)}
                      </td>
                      <td className="px-4 md:px-5 py-3">
                        <span className="inline-flex items-center px-2 py-0.5 rounded-full text-[11px] font-medium bg-elevated text-text-secondary border border-border">
                          {backup.trigger}
                        </span>
                      </td>
                      <td className="px-4 md:px-5 py-3">
                        <div className="flex items-center justify-end gap-3">
                          <button
                            onClick={() => handleDownload(backup.id)}
                            disabled={isActing}
                            className="text-xs md:text-sm font-medium text-brand-400 hover:text-brand-500 disabled:opacity-50 transition-colors"
                          >
                            Download
                          </button>
                          <div className="relative group">
                            <button
                              onClick={() => setConfirmRestore(backup.id)}
                              disabled={isActing || !isStopped}
                              className="text-xs md:text-sm font-medium text-yellow-400 hover:text-yellow-300 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                            >
                              {isActing && confirmRestore === null ? "Restoring..." : "Restore"}
                            </button>
                            {!isStopped && (
                              <div className="absolute right-0 top-full mt-1 z-10 w-48 bg-elevated text-text-secondary text-[11px] rounded-[var(--radius-sm)] px-2.5 py-1.5 opacity-0 group-hover:opacity-100 transition-opacity pointer-events-none shadow-elevated border border-border">
                                Machine must be stopped to restore.
                              </div>
                            )}
                          </div>
                          <button
                            onClick={() => setConfirmDelete(backup.id)}
                            disabled={isActing}
                            className="text-xs md:text-sm font-medium text-red-400 hover:text-red-300 disabled:opacity-50 transition-colors"
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
        )}
      </div>

      {/* Restore confirmation dialog */}
      {confirmRestore !== null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="fixed inset-0 bg-black/60" onClick={() => setConfirmRestore(null)} />
          <div className="relative bg-card border border-border rounded-[var(--radius-lg)] shadow-modal p-5 md:p-6 max-w-sm w-full mx-4 animate-modal-in">
            <h3 className="text-[16px] md:text-[17px] font-semibold text-text-primary mb-2">Restore Backup</h3>
            <p className="text-[12px] md:text-[13px] text-text-secondary mb-4">
              Restore backup <strong>#{confirmRestore}</strong>? This will overwrite the current data volume. This action cannot be undone.
            </p>
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setConfirmRestore(null)}
                className="px-3 py-1.5 text-[12px] md:text-[13px] font-medium text-text-secondary border border-border rounded-[var(--radius-sm)] hover:bg-card-hover transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => handleRestore(confirmRestore)}
                className="px-3 py-1.5 text-[12px] md:text-[13px] font-medium text-white bg-yellow-600 rounded-[var(--radius-sm)] hover:bg-yellow-700 transition-colors"
              >
                Restore
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Delete confirmation dialog */}
      {confirmDelete !== null && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="fixed inset-0 bg-black/60" onClick={() => setConfirmDelete(null)} />
          <div className="relative bg-card border border-border rounded-[var(--radius-lg)] shadow-modal p-5 md:p-6 max-w-sm w-full mx-4 animate-modal-in">
            <h3 className="text-[16px] md:text-[17px] font-semibold text-text-primary mb-2">Delete Backup</h3>
            <p className="text-[12px] md:text-[13px] text-text-secondary mb-4">
              Delete backup <strong>#{confirmDelete}</strong>? This action cannot be undone.
            </p>
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setConfirmDelete(null)}
                className="px-3 py-1.5 text-[12px] md:text-[13px] font-medium text-text-secondary border border-border rounded-[var(--radius-sm)] hover:bg-card-hover transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => handleDelete(confirmDelete)}
                className="px-3 py-1.5 text-[12px] md:text-[13px] font-medium text-white bg-red-600 rounded-[var(--radius-sm)] hover:bg-red-700 transition-colors"
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
