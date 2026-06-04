import { useEffect, useState, useCallback } from "react";
import { listPluginCatalog, listMachinePlugins, enableMachinePlugin, disableMachinePlugin, pushMachineConfig } from "../lib/api";
import type { PluginCatalogEntry, MachinePlugin } from "../lib/api";
import { useToast } from "./Toast";

interface MachinePluginsProps {
  accountId: number;
  machineId: string;
  machineRunning?: boolean;
}

export function MachinePlugins({ accountId, machineId, machineRunning }: MachinePluginsProps) {
  const [catalog, setCatalog] = useState<PluginCatalogEntry[]>([]);
  const [plugins, setPlugins] = useState<MachinePlugin[]>([]);
  const [loading, setLoading] = useState(true);
  const [acting, setActing] = useState<string | null>(null);
  const { toast } = useToast();

  const fetchData = useCallback(async () => {
    try {
      const [catalogData, pluginsData] = await Promise.all([
        listPluginCatalog(),
        listMachinePlugins(accountId, machineId),
      ]);
      setCatalog(catalogData);
      setPlugins(pluginsData);
    } catch {
      toast({ title: "Failed to load plugins", variant: "error" });
    } finally {
      setLoading(false);
    }
  }, [accountId, machineId, toast]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const handleEnable = async (pluginId: string) => {
    setActing(pluginId);
    try {
      const result = await enableMachinePlugin(accountId, machineId, pluginId);
      let pushFailed = false;
      if (machineRunning) {
        const pushRes = await pushMachineConfig(accountId, machineId);
        pushFailed = pushRes.live_update === "failed";
      }
      await fetchData();
      if (pushFailed) {
        toast({ title: "Plugin enabled but failed to push config to VM", variant: "error" });
      } else if (result.reconcile_warning) {
        toast({
          title: "Plugin enabled with warning",
          description: result.reconcile_warning,
          variant: "error",
        });
      } else {
        toast({
          title: "Plugin enabled",
          description: machineRunning ? "Installed on running machine." : "Takes effect on next start.",
          variant: "success",
        });
      }
    } catch (err) {
      toast({ title: "Failed to enable plugin", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setActing(null);
    }
  };

  const handleDisable = async (pluginId: string) => {
    setActing(pluginId);
    try {
      await disableMachinePlugin(accountId, machineId, pluginId);
      let pushFailed = false;
      if (machineRunning) {
        const pushRes = await pushMachineConfig(accountId, machineId);
        pushFailed = pushRes.live_update === "failed";
      }
      await fetchData();
      if (pushFailed) {
        toast({ title: "Plugin disabled but failed to push config to VM", variant: "error" });
      } else {
        toast({
          title: "Plugin disabled",
          description: machineRunning ? "Uninstalling from running machine..." : "Takes effect on next start.",
          variant: "success",
        });
      }
    } catch (err) {
      toast({ title: "Failed to disable plugin", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setActing(null);
    }
  };

  if (loading) {
    return <p className="text-sm text-gray-500 dark:text-gray-400">Loading plugins...</p>;
  }

  // Build a map of enabled plugins by plugin_id
  const enabledMap = new Map(plugins.filter(p => p.enabled).map(p => [p.plugin_id, p]));

  // Only show active catalog entries
  const activeCatalog = catalog.filter(e => e.status === "active");

  // Group by slot
  const slots = new Map<string, PluginCatalogEntry[]>();
  for (const entry of activeCatalog) {
    const list = slots.get(entry.slot) || [];
    list.push(entry);
    slots.set(entry.slot, list);
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium text-gray-900 dark:text-gray-100">Plugins</h3>
          <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
            Manage active plugins for this machine. Only one plugin per slot can be active.
          </p>
        </div>
      </div>

      {activeCatalog.length === 0 ? (
        <p className="text-sm text-gray-500 dark:text-gray-400">No plugins available in the catalog.</p>
      ) : (
        <div className="space-y-4">
          {Array.from(slots.entries()).map(([slot, entries]) => (
            <div key={slot}>
              <p className="text-xs text-gray-500 dark:text-gray-400 uppercase tracking-wide mb-2">
                {slot} slot
              </p>
              <div className="space-y-2">
                {entries.map(entry => {
                  const enabled = enabledMap.has(entry.id);
                  const mp = enabledMap.get(entry.id);
                  const isActing = acting === entry.id;

                  return (
                    <div
                      key={entry.id}
                      className={`flex items-center justify-between p-3 rounded-lg border ${
                        enabled
                          ? "border-brand-200 dark:border-brand-800 bg-brand-50/50 dark:bg-brand-900/10"
                          : "border-gray-200 dark:border-border bg-white dark:bg-surface"
                      }`}
                    >
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                            {entry.name}
                          </span>
                          <span className="text-xs text-gray-400 dark:text-gray-500">
                            v{entry.version}
                          </span>
                          {entry.install_kind !== "bundled" && (
                            <span className="text-xs px-1.5 py-0.5 rounded bg-yellow-100 dark:bg-yellow-900/30 text-yellow-700 dark:text-yellow-400">
                              {entry.install_kind}
                            </span>
                          )}
                          {enabled && mp && (
                            <span className={`text-xs px-1.5 py-0.5 rounded ${
                              mp.install_status === "installed"
                                ? "bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400"
                                : mp.install_status === "failed"
                                ? "bg-red-100 dark:bg-red-900/30 text-red-700 dark:text-red-400"
                                : "bg-gray-100 dark:bg-gray-800 text-gray-600 dark:text-gray-400"
                            }`}>
                              {mp.install_status}
                            </span>
                          )}
                        </div>
                        {entry.description && (
                          <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5 truncate">
                            {entry.description}
                          </p>
                        )}
                      </div>

                      <button
                        onClick={() => enabled ? handleDisable(entry.id) : handleEnable(entry.id)}
                        disabled={isActing}
                        className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-brand-500 focus:ring-offset-2 dark:focus:ring-offset-surface-deep disabled:opacity-50 ${
                          enabled ? "bg-brand-600" : "bg-gray-200 dark:bg-gray-700"
                        }`}
                      >
                        <span
                          className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                            enabled ? "translate-x-6" : "translate-x-1"
                          }`}
                        />
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
