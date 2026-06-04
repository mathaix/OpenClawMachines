import { useEffect, useState, useCallback } from "react";
import { listRegistrySkills, listMachineCapabilities, enableMachineCapability, disableMachineCapability, pushMachineConfig } from "../lib/api";
import type { RegistryEntry, MachineCapability } from "../lib/api";
import { useToast } from "./Toast";

interface MachineSkillsProps {
  accountId: number;
  machineId: string;
  machineRunning?: boolean;
}

export function MachineSkills({ accountId, machineId, machineRunning }: MachineSkillsProps) {
  const [skills, setSkills] = useState<RegistryEntry[]>([]);
  const [capabilities, setCapabilities] = useState<MachineCapability[]>([]);
  const [loading, setLoading] = useState(true);
  const [acting, setActing] = useState<string | null>(null);
  const { toast } = useToast();

  const fetchData = useCallback(async () => {
    try {
      const [skillsData, capsData] = await Promise.all([
        listRegistrySkills(),
        listMachineCapabilities(accountId, machineId),
      ]);
      setSkills(skillsData);
      setCapabilities(capsData);
    } catch {
      toast({ title: "Failed to load skills", variant: "error" });
    } finally {
      setLoading(false);
    }
  }, [accountId, machineId, toast]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  // Skills enabled as capabilities for this machine
  const enabledSkillCaps = capabilities.filter(c => c.enabled && c.entry_id);
  const enabledSet = new Set(enabledSkillCaps.map(c => c.entry_id));
  const isManaged = enabledSet.size > 0;

  const handleToggle = async (skillId: string, currentlyEnabled: boolean) => {
    setActing(skillId);
    try {
      if (currentlyEnabled) {
        await disableMachineCapability(accountId, machineId, skillId);
      } else {
        await enableMachineCapability(accountId, machineId, skillId);
      }
      let pushFailed = false;
      if (machineRunning) {
        const pushRes = await pushMachineConfig(accountId, machineId);
        pushFailed = pushRes.live_update === "failed";
      }
      await fetchData();
      if (pushFailed) {
        toast({ title: (currentlyEnabled ? "Skill removed" : "Skill added") + " but failed to push config to VM", variant: "error" });
      } else {
        toast({
          title: currentlyEnabled ? "Skill removed" : "Skill added",
          description: machineRunning ? "Config pushed to machine." : "Takes effect on next start.",
          variant: "success",
        });
      }
    } catch (err) {
      toast({
        title: `Failed to ${currentlyEnabled ? "remove" : "add"} skill`,
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setActing(null);
    }
  };

  if (loading) {
    return <p className="text-sm text-gray-500 dark:text-gray-400">Loading skills...</p>;
  }

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-sm font-medium text-gray-900 dark:text-gray-100">Skills</h3>
        <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
          {isManaged
            ? `${enabledSet.size} skill${enabledSet.size === 1 ? "" : "s"} enabled. Only enabled skills are available on this machine.`
            : "No skills explicitly enabled — all bundled skills are available by default. Enable specific skills to restrict the allowlist."
          }
        </p>
      </div>

      {skills.length === 0 ? (
        <p className="text-sm text-gray-500 dark:text-gray-400">No skills available in the registry.</p>
      ) : (
        <div className="space-y-2">
          {skills.map(skill => {
            const enabled = enabledSet.has(skill.id);
            const isActing = acting === skill.id;

            return (
              <div
                key={skill.id}
                className={`flex items-center justify-between p-3 rounded-lg border ${
                  enabled
                    ? "border-brand-200 dark:border-brand-800 bg-brand-50/50 dark:bg-brand-900/10"
                    : "border-gray-200 dark:border-border bg-white dark:bg-surface"
                }`}
              >
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                      {skill.name}
                    </span>
                    {!isManaged && !enabled && (
                      <span className="text-xs px-1.5 py-0.5 rounded bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400">
                        available
                      </span>
                    )}
                  </div>
                  {skill.description && (
                    <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5 truncate">
                      {skill.description}
                    </p>
                  )}
                </div>

                <button
                  onClick={() => handleToggle(skill.id, enabled)}
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
      )}
    </div>
  );
}
