import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, ExternalLink } from "lucide-react";

import { getBrowserVM } from "../lib/api";
import { useAuth } from "../lib/auth";
import { ResourcesTab } from "./machine-tabs/ResourcesTab";
import type { BrowserVM } from "../lib/types";

// Browser VM detail page. Phase-2 sibling of /machines/:id, but stripped
// down: no Channels / Files / Backups / etc. — just resource metrics.
// The live-iframe view stays at /browser-vms/:id/live (separate route).
export function BrowserVMDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { account } = useAuth();
  const [vm, setVM] = useState<BrowserVM | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!account || !id) return;
    let cancelled = false;
    setLoading(true);
    getBrowserVM(account.id, id)
      .then((data) => {
        if (!cancelled) setVM(data);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load browser VM");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [account, id]);

  if (loading) {
    return (
      <div className="px-6 py-6 max-w-7xl mx-auto">
        <div className="h-8 w-48 bg-card rounded animate-pulse mb-4" />
        <div className="h-32 bg-card rounded animate-pulse" />
      </div>
    );
  }

  if (error || !vm || !account) {
    return (
      <div className="px-6 py-6 max-w-7xl mx-auto">
        <Link to="/browser-vms" className="text-sm text-text-tertiary hover:text-text-primary inline-flex items-center gap-1 mb-4">
          <ArrowLeft className="w-4 h-4" /> Back to browser VMs
        </Link>
        <p className="text-sm text-red-400">Error: {error ?? "browser VM not found"}</p>
      </div>
    );
  }

  return (
    <div className="px-6 py-6 max-w-7xl mx-auto">
      <Link
        to="/browser-vms"
        className="text-sm text-text-tertiary hover:text-text-primary inline-flex items-center gap-1 mb-4"
      >
        <ArrowLeft className="w-4 h-4" /> Back to browser VMs
      </Link>

      <header className="flex flex-wrap items-start justify-between gap-3 mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-text-primary">{vm.name || vm.slug}</h1>
          <p className="text-sm text-text-tertiary font-mono mt-0.5">{vm.slug}</p>
          <p className="text-xs text-text-muted mt-1">
            {vm.vcpus} vCPU · {Math.round(vm.memory_mb / 1024)} GB ·{" "}
            {vm.host_id ? <span className="font-mono">host-{vm.host_id}</span> : <span>not placed</span>} ·{" "}
            <span className="capitalize">{vm.status}</span>
          </p>
          <p className="text-xs text-text-muted mt-1">
            {vm.paired_machine ? (
              <>
                Paired with{" "}
                <Link
                  to={`/machines/${vm.paired_machine.id}`}
                  className="text-text-secondary hover:text-brand-500 transition-colors"
                >
                  {vm.paired_machine.name}
                </Link>
              </>
            ) : (
              <span className="text-text-tertiary">Not paired</span>
            )}
          </p>
        </div>
        {vm.status === "running" && (
          <Link
            to={`/browser-vms/${vm.id}/live`}
            className="inline-flex items-center gap-1 text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] border border-border text-text-secondary hover:bg-[rgba(255,255,255,0.04)] transition-colors"
          >
            Open live view <ExternalLink className="w-3.5 h-3.5" />
          </Link>
        )}
      </header>

      <ResourcesTab kind="browser" vm={vm} accountId={account.id} />
    </div>
  );
}
