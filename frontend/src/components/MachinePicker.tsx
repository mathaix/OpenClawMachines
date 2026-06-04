import { Link } from "react-router-dom";
import type { Machine } from "../lib/types";

interface MachinePickerProps {
  machines: Machine[];
  onSelect: (id: string) => void;
  loading: boolean;
}

function SkeletonRow() {
  return (
    <div className="flex items-center gap-3 px-4 py-3 animate-pulse">
      <div className="h-2 w-2 rounded-full bg-border flex-shrink-0" />
      <div className="flex flex-col gap-1.5 flex-1">
        <div className="h-3.5 w-32 rounded bg-border" />
        <div className="h-3 w-20 rounded bg-border" />
      </div>
    </div>
  );
}

export function MachinePicker({ machines, onSelect, loading }: MachinePickerProps) {
  if (loading) {
    return (
      <div className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden divide-y divide-border">
        <SkeletonRow />
        <SkeletonRow />
        <SkeletonRow />
      </div>
    );
  }

  if (machines.length === 0) {
    return (
      <div className="bg-card border border-border rounded-[var(--radius-lg)] px-6 py-10 text-center">
        <p className="text-text-secondary mb-3">No running machines</p>
        <Link
          to="/dashboard"
          className="text-sm text-brand-500 hover:underline"
        >
          Go to Dashboard
        </Link>
      </div>
    );
  }

  return (
    <div className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden divide-y divide-border">
      {machines.map((machine) => (
        <button
          key={machine.id}
          onClick={() => onSelect(machine.id)}
          className="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-card-hover transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500 focus-visible:ring-inset"
        >
          <span
            className="h-2 w-2 rounded-full flex-shrink-0 bg-green-500"
            aria-hidden="true"
          />
          <span className="flex flex-col min-w-0">
            <span className="text-[14px] font-medium text-text-primary truncate">
              {machine.name}
            </span>
            <span className="text-[11px] text-text-secondary truncate">
              {machine.slug}
            </span>
          </span>
        </button>
      ))}
    </div>
  );
}
