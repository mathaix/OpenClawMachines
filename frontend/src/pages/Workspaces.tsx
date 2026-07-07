import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, Navigate, useLocation, useNavigate, useParams } from "react-router-dom";
import { ArrowRight, CheckCircle2, Loader2, Plus, Server, Settings2, Wrench } from "lucide-react";
import { useAuth } from "../lib/auth";
import {
  createWorkspace,
  getWorkspace,
  listWorkspaceMachines,
  listWorkspaces,
} from "../lib/api";
import type { WorkspaceSummary } from "../lib/api";
import type { Machine } from "../lib/types";

function WorkspaceMetric({
  label,
  value,
  Icon,
}: {
  label: string;
  value: number | string;
  Icon: typeof Server;
}) {
  return (
    <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-[11px] uppercase tracking-wider text-text-tertiary">{label}</p>
        <Icon className="h-4 w-4 text-text-tertiary" />
      </div>
      <p className="mt-3 text-2xl font-semibold text-text-primary">{value}</p>
    </div>
  );
}

export function Workspaces() {
  const { account } = useAuth();
  const [workspaces, setWorkspaces] = useState<WorkspaceSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  const load = useCallback(async () => {
    if (!account) return;
    setLoading(true);
    setError(null);
    try {
      setWorkspaces(await listWorkspaces(account.id));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to load workspaces.");
    } finally {
      setLoading(false);
    }
  }, [account]);

  useEffect(() => {
    load();
  }, [load]);

  const totals = useMemo(() => ({
    machines: workspaces.reduce((sum, workspace) => sum + workspace.machine_count, 0),
    integrations: workspaces.reduce((sum, workspace) => sum + workspace.integration_count, 0),
  }), [workspaces]);

  const handleCreate = useCallback(async () => {
    if (!account || !name.trim()) return;
    setCreating(true);
    setError(null);
    try {
      const workspace = await createWorkspace(account.id, { name: name.trim() });
      setName("");
      navigate(`/workspaces/${workspace.id}`);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create workspace.");
    } finally {
      setCreating(false);
    }
  }, [account, name, navigate]);

  if (!account) {
    return <p className="text-[13px] text-text-secondary">Loading account...</p>;
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div>
          <p className="text-[11px] uppercase tracking-wider text-text-tertiary">{account.name}</p>
          <h1 className="mt-1 text-2xl font-bold text-text-primary md:text-3xl">Workspaces</h1>
        </div>
        <div className="flex w-full gap-2 md:w-auto">
          <input
            type="text"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Workspace name"
            className="min-w-0 flex-1 rounded-[var(--radius-sm)] border border-border bg-input px-3 py-2 text-sm text-text-primary outline-none focus:border-brand-500 md:w-56"
          />
          <button
            type="button"
            onClick={handleCreate}
            disabled={creating || !name.trim()}
            className="inline-flex items-center gap-2 rounded-[var(--radius-sm)] bg-brand-600 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
            Create
          </button>
        </div>
      </div>

      {error && (
        <div className="rounded-[var(--radius-lg)] border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-500">
          {error}
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-3">
        <WorkspaceMetric label="Workspaces" value={workspaces.length} Icon={Settings2} />
        <WorkspaceMetric label="Machines" value={totals.machines} Icon={Server} />
        <WorkspaceMetric label="Integrations" value={totals.integrations} Icon={Wrench} />
      </div>

      {loading ? (
        <div className="flex items-center gap-2 text-sm text-text-secondary">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading...
        </div>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {workspaces.map((workspace) => (
            <Link
              key={workspace.id}
              to={`/workspaces/${workspace.id}`}
              className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4 transition-colors hover:border-brand-500/60"
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h2 className="truncate text-[15px] font-semibold text-text-primary">{workspace.name}</h2>
                    {workspace.slug === "default" && (
                      <span className="rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-[10px] uppercase tracking-wider text-emerald-500">
                        Default
                      </span>
                    )}
                  </div>
                  <p className="mt-1 text-xs text-text-tertiary">{workspace.slug}</p>
                </div>
                <ArrowRight className="h-4 w-4 flex-shrink-0 text-text-tertiary" />
              </div>
              <div className="mt-4 grid grid-cols-2 gap-3 text-xs">
                <div>
                  <p className="text-text-tertiary">Machines</p>
                  <p className="mt-1 font-medium text-text-primary">{workspace.machine_count}</p>
                </div>
                <div>
                  <p className="text-text-tertiary">Integrations</p>
                  <p className="mt-1 font-medium text-text-primary">{workspace.integration_count}</p>
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

export function WorkspaceOverview() {
  const { account } = useAuth();
  const { workspaceId } = useParams<{ workspaceId: string }>();
  const [workspace, setWorkspace] = useState<WorkspaceSummary | null>(null);
  const [machines, setMachines] = useState<Machine[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!account || !workspaceId) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    Promise.all([
      getWorkspace(account.id, workspaceId),
      listWorkspaceMachines(account.id, workspaceId),
    ])
      .then(([nextWorkspace, nextMachines]) => {
        if (cancelled) return;
        setWorkspace(nextWorkspace);
        setMachines(nextMachines);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to load workspace.");
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [account, workspaceId]);

  if (!account) {
    return <p className="text-[13px] text-text-secondary">Loading account...</p>;
  }
  if (!workspaceId) {
    return <Navigate to="/workspaces" replace />;
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div>
          <Link to="/workspaces" className="text-[11px] uppercase tracking-wider text-text-tertiary hover:text-text-primary">
            Workspaces
          </Link>
          <h1 className="mt-1 text-2xl font-bold text-text-primary md:text-3xl">{workspace?.name ?? "Workspace"}</h1>
        </div>
        <Link
          to={`/workspaces/${workspaceId}/integrations`}
          className="inline-flex w-fit items-center gap-2 rounded-[var(--radius-sm)] bg-brand-600 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-700"
        >
          <Wrench className="h-4 w-4" />
          Integrations
        </Link>
      </div>

      {error && (
        <div className="rounded-[var(--radius-lg)] border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-500">
          {error}
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-3">
        <WorkspaceMetric label="Machines" value={workspace?.machine_count ?? 0} Icon={Server} />
        <WorkspaceMetric label="Integrations" value={workspace?.integration_count ?? 0} Icon={Wrench} />
        <WorkspaceMetric label="Status" value={loading ? "Loading" : "Ready"} Icon={CheckCircle2} />
      </div>

      <section className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4">
        <div className="mb-3 flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold uppercase tracking-wider text-text-tertiary">Machines</h2>
          <Server className="h-4 w-4 text-text-tertiary" />
        </div>
        {loading ? (
          <div className="flex items-center gap-2 py-4 text-sm text-text-secondary">
            <Loader2 className="h-4 w-4 animate-spin" />
            Loading...
          </div>
        ) : machines.length === 0 ? (
          <p className="py-4 text-sm text-text-tertiary">No machines in this workspace.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[520px] text-left">
              <thead>
                <tr className="text-[11px] uppercase tracking-wider text-text-tertiary">
                  <th className="pb-2 pr-4 font-medium">Machine</th>
                  <th className="pb-2 pr-4 font-medium">Kind</th>
                  <th className="pb-2 pr-4 font-medium">Status</th>
                  <th className="pb-2 font-medium text-right">Open</th>
                </tr>
              </thead>
              <tbody>
                {machines.map((machine) => (
                  <tr key={machine.id} className="border-t border-border">
                    <td className="py-3 pr-4 text-sm font-medium text-text-primary">{machine.name}</td>
                    <td className="py-3 pr-4 text-sm text-text-secondary">{machine.kind || "openclaw"}</td>
                    <td className="py-3 pr-4 text-sm capitalize text-text-secondary">{machine.status}</td>
                    <td className="py-3 text-right">
                      <Link to={`/machines/${machine.id}`} className="text-xs font-medium text-brand-500 hover:text-brand-400">
                        Details
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

export function DefaultWorkspaceIntegrationsRedirect() {
  const { account } = useAuth();
  const location = useLocation();
  const [target, setTarget] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!account) return;
    listWorkspaces(account.id)
      .then((workspaces) => {
        const workspace = workspaces.find((item) => item.slug === "default") ?? workspaces[0];
        setTarget(workspace ? `/workspaces/${workspace.id}/integrations${location.search}` : "/workspaces");
      })
      .catch(() => setFailed(true));
  }, [account, location.search]);

  if (target) {
    return <Navigate to={target} replace />;
  }
  if (failed) {
    return <Navigate to="/workspaces" replace />;
  }
  return <p className="text-[13px] text-text-secondary">Loading workspace...</p>;
}
