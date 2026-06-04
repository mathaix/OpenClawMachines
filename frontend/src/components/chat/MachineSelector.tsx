import type { Machine } from "../../lib/types";

interface MachineSelectorProps {
  machines: Machine[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  loading: boolean;
}

export function MachineSelector({
  machines,
  selectedId,
  onSelect,
  loading,
}: MachineSelectorProps) {
  if (loading) {
    return (
      <div className="flex items-center gap-2 px-4 py-2 text-sm text-zinc-400">
        <div className="h-4 w-4 animate-spin rounded-full border-2 border-zinc-600 border-t-zinc-300" />
        Loading machines...
      </div>
    );
  }

  if (machines.length === 0) {
    return (
      <div className="px-4 py-2 text-sm text-zinc-400">
        No running machines.{" "}
        <a href="/dashboard" className="text-blue-400 hover:underline">
          Start one
        </a>
      </div>
    );
  }

  return (
    <select
      role="listbox"
      value={selectedId ?? ""}
      onChange={(e) => onSelect(e.target.value)}
      className="w-full max-w-xs rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm text-zinc-200 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500"
    >
      {machines.map((m) => (
        <option key={m.id} value={m.id}>
          {m.name}
        </option>
      ))}
    </select>
  );
}
