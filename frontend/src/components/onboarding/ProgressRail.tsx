const STEP_LABELS = ["Account", "Machine", "Provider", "Channels", "Identity", "Launch"];

interface ProgressRailProps {
  currentStep: number;
  completedSteps: Set<number>;
}

export function ProgressRail({ currentStep, completedSteps }: ProgressRailProps) {
  return (
    <nav className="w-full py-4">
      <ol className="flex items-center justify-between gap-1">
        {STEP_LABELS.map((label, i) => {
          const isCompleted = completedSteps.has(i);
          const isActive = currentStep === i;
          const isUpcoming = !isCompleted && !isActive;

          return (
            <li key={label} className="flex flex-1 items-center">
              <div className="flex flex-col items-center gap-1.5 w-full">
                <div className="flex items-center w-full">
                  {i > 0 && (
                    <div
                      className={`flex-1 h-px ${
                        completedSteps.has(i - 1) ? "bg-green-500" : "bg-border"
                      }`}
                    />
                  )}
                  <div
                    className={`flex-shrink-0 h-7 w-7 rounded-full flex items-center justify-center text-xs font-medium transition-colors ${
                      isCompleted
                        ? "bg-green-500 text-white"
                        : isActive
                          ? "bg-brand-500 text-white ring-2 ring-brand-500/30"
                          : "bg-surface-elevated text-gray-500 border border-border"
                    }`}
                  >
                    {isCompleted ? (
                      <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                        <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
                      </svg>
                    ) : (
                      i + 1
                    )}
                  </div>
                  {i < STEP_LABELS.length - 1 && (
                    <div
                      className={`flex-1 h-px ${
                        isCompleted ? "bg-green-500" : "bg-border"
                      }`}
                    />
                  )}
                </div>
                <span
                  className={`text-[11px] leading-tight ${
                    isCompleted
                      ? "text-green-400"
                      : isActive
                        ? "text-brand-400 font-medium"
                        : "text-gray-500"
                  } ${isUpcoming ? "hidden sm:block" : ""}`}
                >
                  {label}
                </span>
              </div>
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
