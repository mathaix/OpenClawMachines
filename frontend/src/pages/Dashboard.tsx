import { useEffect, useState, useCallback, useMemo, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { listMachines } from "../lib/api";
import { useAuth } from "../lib/auth";
import { MachineCard } from "../components/MachineCard";
import { CreateMachineModal } from "../components/CreateMachineModal";
import { SkeletonCard } from "../components/SkeletonCard";
import { useKeyboardShortcuts } from "../lib/useKeyboardShortcuts";
import type { Machine } from "../lib/types";

export function Dashboard() {
  const navigate = useNavigate();
  const { account, accountError } = useAuth();
  const [machines, setMachines] = useState<Machine[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showCreateModal, setShowCreateModal] = useState(false);

  const shortcuts = useMemo(() => [
    { key: "n", ctrl: true, shift: true, action: () => setShowCreateModal(true), description: "Open create machine modal" },
  ], []);
  useKeyboardShortcuts(shortcuts);

  const fetchMachines = useCallback(async () => {
    if (!account) return;
    try {
      const data = await listMachines(account.id);
      setMachines(data ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load machines");
    } finally {
      setLoading(false);
    }
  }, [account]);

  const FAST_POLL = 5000;
  const SLOW_POLL = 30000;
  const intervalRef = useRef<ReturnType<typeof setInterval> | undefined>(undefined);

  const hasTransitional = machines.some(
    (m) => m.status === "provisioning" || m.status === "starting"
  );

  useEffect(() => {
    fetchMachines();
  }, [fetchMachines]);

  useEffect(() => {
    clearInterval(intervalRef.current);
    const rate = hasTransitional ? FAST_POLL : SLOW_POLL;
    intervalRef.current = setInterval(fetchMachines, rate);
    return () => clearInterval(intervalRef.current);
  }, [fetchMachines, hasTransitional]);

  const handleStatusChange = useCallback((id: string, patch: Partial<Machine>) => {
    setMachines((prev) => prev.map((m) => (m.id === id ? { ...m, ...patch } : m)));
    // Background fetch for server truth
    fetchMachines();
  }, [fetchMachines]);

  if (!account && accountError) return <p className="text-red-600 dark:text-red-400">Failed to load account. Please refresh the page.</p>;
  if (!account) return (
    <div className="max-w-6xl mx-auto">
      <div className="flex items-center justify-between mb-6 md:mb-8">
        <h1 className="text-2xl md:text-3xl font-bold tracking-[-0.02em] text-text-primary">Machines</h1>
      </div>
      <div className="flex flex-col gap-2">
        <SkeletonCard />
        <SkeletonCard />
        <SkeletonCard />
      </div>
    </div>
  );

  return (
    <div className="max-w-6xl mx-auto">
      <div className="flex items-center justify-between mb-6 md:mb-8">
        <div className="flex items-center gap-2">
          <h1 className="text-2xl md:text-3xl font-bold tracking-[-0.02em] text-text-primary">Machines</h1>
          {machines.length > 0 && (
            <span className="text-text-muted text-lg md:text-xl">{machines.length}</span>
          )}
        </div>
        <button
          onClick={() => setShowCreateModal(true)}
          className="bg-brand-600 text-white text-sm md:text-base font-medium px-4 py-2 rounded-[var(--radius-sm)] hover:bg-brand-700 shadow-[0_2px_8px_rgba(234,88,12,0.3)] transition-all hover:shadow-[0_4px_12px_rgba(234,88,12,0.4)]"
        >
          New Machine
        </button>
      </div>

      {error && <p className="text-red-600 dark:text-red-400">Error: {error}</p>}

      {loading ? (
        <div className="flex flex-col gap-2">
          <SkeletonCard />
          <SkeletonCard />
          <SkeletonCard />
        </div>
      ) : machines.length === 0 ? (
        <div className="text-center py-16">
          <p className="text-text-muted mb-4">No machines yet</p>
          <button
            onClick={() => setShowCreateModal(true)}
            className="bg-brand-600 text-white text-sm md:text-base font-medium px-4 py-2 rounded-[var(--radius-sm)] hover:bg-brand-700 shadow-[0_2px_8px_rgba(234,88,12,0.3)] transition-all hover:shadow-[0_4px_12px_rgba(234,88,12,0.4)]"
          >
            Create your first machine
          </button>
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          {machines.map((m) => (
            <MachineCard key={m.id} machine={m} accountId={account.id} accountSlug={account.slug} onStatusChange={handleStatusChange} />
          ))}
        </div>
      )}

      <CreateMachineModal
        open={showCreateModal}
        onOpenChange={setShowCreateModal}
        accountId={account.id}
        onCreated={(machineId) => navigate(`/machines/${machineId}`)}
      />
    </div>
  );
}
