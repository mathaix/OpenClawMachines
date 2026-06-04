import { clsx } from "clsx";

type MachineStatus = "running" | "stopped" | "provisioning" | "starting" | "error" | string;

interface StatusBadgeProps {
  status: MachineStatus;
  className?: string;
}

const statusConfig: Record<string, { bg: string; text: string; dotColor: string; glow?: boolean }> = {
  running: {
    bg: "bg-[rgba(74,222,128,0.08)]",
    text: "text-green-400",
    dotColor: "bg-green-400",
    glow: true,
  },
  stopped: {
    bg: "bg-[rgba(107,114,128,0.1)]",
    text: "text-text-tertiary",
    dotColor: "bg-text-muted",
  },
  provisioning: {
    bg: "bg-[rgba(250,204,21,0.08)]",
    text: "text-yellow-400",
    dotColor: "bg-yellow-400",
  },
  starting: {
    bg: "bg-[rgba(250,204,21,0.08)]",
    text: "text-yellow-400",
    dotColor: "bg-yellow-400",
  },
  error: {
    bg: "bg-[rgba(248,113,113,0.08)]",
    text: "text-red-400",
    dotColor: "bg-red-400",
  },
};

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const config = statusConfig[status] ?? statusConfig.stopped;
  return (
    <span
      className={clsx(
        "inline-flex items-center gap-1.5 text-[11px] md:text-[12px] font-medium px-2.5 py-[3px] rounded-full capitalize",
        config.bg,
        config.text,
        className
      )}
    >
      <span
        className={clsx(
          "w-[6px] h-[6px] rounded-full",
          config.dotColor,
          config.glow && "shadow-[0_0_6px_var(--green-glow)] animate-pulse-dot"
        )}
      />
      {status}
    </span>
  );
}
