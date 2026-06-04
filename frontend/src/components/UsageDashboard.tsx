import { useEffect, useState, useCallback } from "react";
import { getAccountUsage, setMachineBudget, deleteMachineBudget } from "../lib/api";
import type { AccountUsage, MachineUsageSummary } from "../lib/types";

interface UsageDashboardProps {
  accountId: number;
}

/** Convert microcents to a formatted dollar string */
function formatDollars(microcents: number): string {
  return "$" + (microcents / 1_000_000).toFixed(2);
}

/** Get the percentage of budget used, capped at 100 for display */
function budgetPercent(spend: number, budget: number): number {
  if (budget <= 0) return 0;
  return Math.min(100, (spend / budget) * 100);
}

/** Get the color class for a progress bar based on percentage */
function progressColor(percent: number): string {
  if (percent > 90) return "bg-red-500";
  if (percent > 75) return "bg-yellow-500";
  return "bg-green-500";
}

/** Get the background track color to match the bar */
function progressTrackColor(percent: number): string {
  if (percent > 90) return "bg-red-100 dark:bg-red-900/20";
  if (percent > 75) return "bg-yellow-100 dark:bg-yellow-900/20";
  return "bg-green-100 dark:bg-green-900/20";
}

export function UsageDashboard({ accountId }: UsageDashboardProps) {
  const [usage, setUsage] = useState<AccountUsage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Budget editing state
  const [editingMachineId, setEditingMachineId] = useState<string | null>(null);
  const [budgetInput, setBudgetInput] = useState("");
  const [saving, setSaving] = useState(false);

  // Delete budget confirmation state
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const fetchUsage = useCallback(async () => {
    try {
      const data = await getAccountUsage(accountId);
      setUsage(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load usage data");
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    fetchUsage();
  }, [fetchUsage]);

  const handleSetBudget = async (machineId: string) => {
    const cents = parseFloat(budgetInput);
    if (isNaN(cents) || cents <= 0) {
      setError("Please enter a valid budget amount in dollars");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await setMachineBudget(accountId, machineId, Math.round(cents * 100));
      setEditingMachineId(null);
      setBudgetInput("");
      await fetchUsage();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to set budget");
    } finally {
      setSaving(false);
    }
  };

  const handleDeleteBudget = async (machineId: string) => {
    setDeleting(true);
    setError(null);
    try {
      await deleteMachineBudget(accountId, machineId);
      setConfirmDeleteId(null);
      await fetchUsage();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to remove budget");
    } finally {
      setDeleting(false);
    }
  };

  const handleCancelEdit = () => {
    setEditingMachineId(null);
    setBudgetInput("");
  };

  if (loading) return <p className="text-sm text-gray-500 dark:text-gray-400">Loading usage data...</p>;

  return (
    <div className="space-y-6">
      <p className="text-sm text-gray-500 dark:text-gray-400">
        Track AI spending across your machines and set monthly budgets to control costs.
      </p>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-red-700 dark:text-red-400 text-sm rounded-lg p-3">
          {error}
        </div>
      )}

      {/* Total account spend */}
      <div className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border p-6">
        <p className="text-xs text-gray-500 dark:text-gray-400 uppercase tracking-wide">Current Month Spend</p>
        <p className="text-3xl font-semibold text-gray-900 dark:text-gray-100 mt-1">
          {usage ? formatDollars(usage.current_month_spend) : "$0.00"}
        </p>
      </div>

      {/* Per-machine breakdown */}
      {usage && usage.per_machine && usage.per_machine.length > 0 ? (
        <div className="space-y-3">
          <h2 className="text-sm font-medium text-gray-900 dark:text-gray-100">Per-Machine Breakdown</h2>
          <div className="space-y-3">
            {usage.per_machine.map((machine: MachineUsageSummary) => {
              const hasBudget = machine.budget_microcents > 0;
              const percent = hasBudget
                ? budgetPercent(machine.spend_microcents, machine.budget_microcents)
                : 0;
              const isEditing = editingMachineId === machine.machine_id;
              const isConfirmingDelete = confirmDeleteId === machine.machine_id;

              return (
                <div
                  key={machine.machine_id}
                  className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border p-4"
                >
                  {/* Machine header row */}
                  <div className="flex items-start justify-between">
                    <div className="flex-1">
                      <p className="text-sm font-medium text-gray-900 dark:text-gray-100">
                        {machine.machine_name}
                      </p>
                      <div className="mt-1 flex items-center gap-4 text-sm text-gray-500 dark:text-gray-400">
                        <span>Spend: {formatDollars(machine.spend_microcents)}</span>
                        {hasBudget && (
                          <>
                            <span>Budget: {formatDollars(machine.budget_microcents)}</span>
                            <span>{percent.toFixed(1)}% used</span>
                          </>
                        )}
                        {!hasBudget && (
                          <span className="text-gray-400 dark:text-gray-500">No budget set</span>
                        )}
                      </div>
                    </div>

                    {/* Action buttons */}
                    {!isEditing && (
                      <div className="flex gap-2 ml-4">
                        <button
                          onClick={() => {
                            setEditingMachineId(machine.machine_id);
                            setBudgetInput(
                              hasBudget
                                ? (machine.budget_microcents / 1_000_000).toFixed(2)
                                : ""
                            );
                          }}
                          className="text-xs font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400 dark:hover:text-brand-300"
                        >
                          {hasBudget ? "Edit Budget" : "Set Budget"}
                        </button>
                        {hasBudget && (
                          <>
                            {isConfirmingDelete ? (
                              <span className="flex gap-1">
                                <button
                                  onClick={() => handleDeleteBudget(machine.machine_id)}
                                  disabled={deleting}
                                  className="text-xs font-medium text-red-600 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300 disabled:opacity-50"
                                >
                                  {deleting ? "..." : "Confirm"}
                                </button>
                                <button
                                  onClick={() => setConfirmDeleteId(null)}
                                  className="text-xs font-medium text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-300"
                                >
                                  Cancel
                                </button>
                              </span>
                            ) : (
                              <button
                                onClick={() => setConfirmDeleteId(machine.machine_id)}
                                className="text-xs font-medium text-red-600 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
                              >
                                Remove
                              </button>
                            )}
                          </>
                        )}
                      </div>
                    )}
                  </div>

                  {/* Budget progress bar */}
                  {hasBudget && (
                    <div className="mt-3">
                      <div className={`w-full h-2 rounded-full ${progressTrackColor(percent)}`}>
                        <div
                          className={`h-2 rounded-full transition-all ${progressColor(percent)}`}
                          style={{ width: `${percent}%` }}
                        />
                      </div>
                    </div>
                  )}

                  {/* Inline budget form */}
                  {isEditing && (
                    <div className="mt-3 border-t border-gray-100 dark:border-border pt-3 space-y-3">
                      <div>
                        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                          Monthly Budget (USD)
                        </label>
                        <div className="relative">
                          <span className="absolute left-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 dark:text-gray-500">
                            $
                          </span>
                          <input
                            type="number"
                            min="0"
                            step="0.01"
                            value={budgetInput}
                            onChange={(e) => setBudgetInput(e.target.value)}
                            className="w-full border border-gray-300 dark:border-border rounded-lg pl-7 pr-3 py-2 text-sm bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100"
                            placeholder="e.g. 10.00"
                            autoFocus
                          />
                        </div>
                        <p className="text-xs text-gray-400 dark:text-gray-500 mt-1">
                          Set a monthly spending limit for this machine. Usage will be paused when the budget is reached.
                        </p>
                      </div>
                      <div className="flex gap-2">
                        <button
                          onClick={() => handleSetBudget(machine.machine_id)}
                          disabled={saving || !budgetInput.trim()}
                          className="px-4 py-2 text-sm font-medium text-white bg-brand-600 rounded-lg hover:bg-brand-700 disabled:opacity-50"
                        >
                          {saving ? "Saving..." : "Save Budget"}
                        </button>
                        <button
                          onClick={handleCancelEdit}
                          className="px-4 py-2 text-sm font-medium text-gray-700 dark:text-gray-300 border border-gray-300 dark:border-border rounded-lg hover:bg-gray-50 dark:hover:bg-surface-elevated"
                        >
                          Cancel
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      ) : (
        !loading && (
          <div className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border p-6 text-center">
            <p className="text-sm text-gray-500 dark:text-gray-400">
              No machines found. Create a machine to start tracking usage.
            </p>
          </div>
        )
      )}
    </div>
  );
}
