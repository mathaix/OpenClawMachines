import type { MachineKind } from "../lib/types";

interface KindBadgeProps {
  kind?: MachineKind;
  className?: string;
}

export function KindBadge({ kind = "openclaw", className = "" }: KindBadgeProps) {
  const label = kind === "hermes" ? "Hermes" : "OpenClaw";
  const tone =
    kind === "hermes"
      ? "border-sky-500/25 bg-sky-500/10 text-sky-300"
      : "border-brand-500/25 bg-brand-500/10 text-brand-300";

  return (
    <span
      className={`inline-flex items-center h-6 px-2 rounded-[var(--radius-sm)] border text-[11px] font-medium ${tone} ${className}`}
    >
      {label}
    </span>
  );
}
