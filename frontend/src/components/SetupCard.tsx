import React from "react";
import { ChevronRight, CheckCircle2 } from "lucide-react";

interface SetupCardProps {
  icon: React.ReactNode;
  iconColorClass: string;
  title: string;
  description: string;
  completed?: boolean;
  hoverBorderClass?: string;
  onClick?: () => void;
}

export function SetupCard({ icon, iconColorClass, title, description, completed, hoverBorderClass, onClick }: SetupCardProps) {
  return (
    <div
      onClick={onClick}
      className={`group bg-card border-2 border-border rounded-[var(--radius-lg)] p-4 md:p-5 cursor-pointer transition-all duration-300 flex items-center gap-3 md:gap-4 relative hover:shadow-elevated hover:-translate-y-0.5 ${hoverBorderClass || "hover:border-brand-600/30"} ${completed ? "opacity-65" : ""}`}
    >
      <div className={`w-11 h-11 md:w-14 md:h-14 rounded-xl flex items-center justify-center flex-shrink-0 ${iconColorClass}`}>
        {icon}
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <div className="text-[14px] md:text-[16px] font-semibold tracking-[-0.01em] text-text-primary">{title}</div>
          {completed && (
            <CheckCircle2 className="w-4 h-4 md:w-5 md:h-5 text-green-500 flex-shrink-0" />
          )}
        </div>
        <div className="text-[11px] md:text-[13px] text-text-tertiary leading-[1.45] mt-0.5">{description}</div>
      </div>
      <ChevronRight className="w-5 h-5 text-text-muted transition-all duration-200 group-hover:text-brand-500 group-hover:translate-x-0.5 flex-shrink-0 ml-2" />
    </div>
  );
}
