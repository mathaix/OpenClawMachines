import { useNavigate, Link } from "react-router-dom";
import { useToast } from "./Toast";
import { useOperation } from "../lib/useOperation";
import { transitionMachine } from "../operations/machine";
import { StatusBadge } from "./StatusBadge";
import { KindBadge } from "./KindBadge";
import type { MachineOperationResult } from "../operations/machine";
import type { Machine } from "../lib/types";

function timeAgo(dateStr: string): string {
  const seconds = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

interface MachineCardProps {
  machine: Machine;
  accountId?: number;
  accountSlug?: string;
  onStatusChange?: (id: string, patch: Partial<Machine>) => void;
}

export function MachineCard({ machine, accountId, onStatusChange }: MachineCardProps) {
  const navigate = useNavigate();
  const { toast } = useToast();
  const lifecycle = useOperation<MachineOperationResult>();

  const handleStart = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (!accountId) return;
    const result = await lifecycle.execute(() => transitionMachine(accountId, machine.id, "start"));
    if (result?.success) onStatusChange?.(machine.id, { status: "starting", status_message: "", provision_step: "" });
    else if (result && !result.success) toast({ title: "Failed to start machine", description: result.error?.message || "Unknown error", variant: "error" });
  };

  const handleStop = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (!accountId) return;
    const result = await lifecycle.execute(() => transitionMachine(accountId, machine.id, "stop"));
    if (result?.success) onStatusChange?.(machine.id, { status: "stopped", status_message: "", provision_step: "" });
    else if (result && !result.success) toast({ title: "Failed to stop machine", description: result.error?.message || "Unknown error", variant: "error" });
  };

  const handleCardClick = () => {
    navigate(`/machines/${machine.id}`);
  };

  const isRunning = machine.status === "running";
  const isHermesMachine = machine.kind === "hermes";
  const canStart = machine.status === "stopped" || machine.status === "error";
  const isBooting = machine.status === "provisioning" || machine.status === "starting";
  const canStop = machine.status === "running" || isBooting;

  return (
    <div
      className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card cursor-pointer transition-all duration-200 hover:bg-card-hover hover:border-border-hover hover:shadow-elevated hover:-translate-y-0.5"
      onClick={handleCardClick}
      role="article"
    >
      {/* Header */}
      <div className="px-4 md:px-6 pt-4 md:pt-6 pb-3">
        <div className="flex items-start justify-between">
          <div className="flex-1 min-w-0">
            <span className="text-lg md:text-xl font-semibold text-text-primary block truncate">
              {machine.name}
            </span>
            <div className="flex flex-wrap items-center gap-2 mt-2 text-xs md:text-sm text-text-tertiary">
              <span>Created {timeAgo(machine.created_at)}</span>
              {isBooting && machine.provision_step && (
                <span className="text-yellow-400">{machine.provision_step}</span>
              )}
              {isRunning && machine.started_at && (
                <>
                  <span className="text-text-muted">&bull;</span>
                  <span className="text-green-400">up {timeAgo(machine.started_at)}</span>
                </>
              )}
              {machine.status === "stopped" && machine.last_started_at && (
                <>
                  <span className="text-text-muted">&bull;</span>
                  <span>last active {timeAgo(machine.last_started_at)}</span>
                </>
              )}
              {machine.status === "error" && machine.status_message && (
                <span className="text-red-400 truncate max-w-[200px]" title={machine.status_message}>
                  {machine.status_message}
                </span>
              )}
            </div>
          </div>
          <div className="ml-3 flex-shrink-0 flex items-center gap-2">
            <KindBadge kind={machine.kind} />
            <StatusBadge status={machine.status} />
          </div>
        </div>
      </div>

      {/* Actions */}
      <div className="px-4 md:px-6 pb-4 md:pb-6 pt-0">
        <div className="flex flex-wrap items-center gap-2">
          {isRunning && (
            <>
              <Link
                to={isHermesMachine ? `/chat/${machine.id}` : `/workspace/${machine.id}`}
                onClick={(e) => e.stopPropagation()}
                className="text-xs md:text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] text-brand-400 hover:bg-brand-600/10 transition-colors"
              >
                {isHermesMachine ? "Webchat" : "Workspace"}
              </Link>
              <Link
                to={isHermesMachine ? `/workspace/${machine.id}` : `/chat/${machine.id}`}
                onClick={(e) => e.stopPropagation()}
                className="text-xs md:text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] text-brand-400 hover:bg-brand-600/10 transition-colors"
              >
                {isHermesMachine ? "Terminal" : "Chat"}
              </Link>
            </>
          )}

          {accountId && (
            <div className="flex gap-2 ml-auto">
              {canStart && (
                <button
                  onClick={handleStart}
                  disabled={lifecycle.loading}
                  className="text-xs md:text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] text-green-400 hover:bg-green-500/10 disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                >
                  {lifecycle.loading ? "..." : "Start"}
                </button>
              )}
              {canStop && (
                <button
                  onClick={handleStop}
                  disabled={lifecycle.loading}
                  className="text-xs md:text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] text-red-400 hover:bg-red-500/10 disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                >
                  {lifecycle.loading ? "..." : "Stop"}
                </button>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
