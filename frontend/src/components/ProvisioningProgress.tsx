import { useEffect, useState, useRef } from "react";

interface ProgressStep {
  id: string;
  label: string;
  message: string;
  order: number;
}

const STEPS: ProgressStep[] = [
  { id: "allocating", label: "Allocating isolated KVM hardware...", message: "", order: 1 },
  { id: "rootfs", label: "Preparing filesystem...", message: "", order: 2 },
  { id: "network", label: "Configuring secure network...", message: "", order: 3 },
  { id: "booting", label: "Booting MicroVM...", message: "", order: 4 },
  { id: "openclaw_ready", label: "Injecting secure identity vault...", message: "", order: 5 },
  { id: "machine_ready", label: "Waking the agent brain...", message: "", order: 6 },
];

function formatElapsed(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  return `${minutes}m ${remainingSeconds}s`;
}

interface ProvisioningProgressProps {
  accountId: number;
  machineId: string;
  onComplete?: () => void;
  onRetry?: () => void;
}

export function ProvisioningProgress({ accountId, machineId, onComplete, onRetry }: ProvisioningProgressProps) {
  const [currentStep, setCurrentStep] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [completed, setCompleted] = useState(false);
  const [connectAttempt, setConnectAttempt] = useState(0);
  const eventSourceRef = useRef<EventSource | null>(null);
  const receivedAnyStep = useRef(false);
  const startTime = useRef(Date.now());
  const stepStartTimes = useRef<Record<string, number>>({});
  const [now, setNow] = useState(Date.now());
  const onCompleteRef = useRef(onComplete);
  onCompleteRef.current = onComplete;

  // Stop timer when provisioning finishes or errors
  useEffect(() => {
    if (completed || errorMessage) return;
    const interval = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(interval);
  }, [completed, errorMessage]);

  useEffect(() => {
    const base = import.meta.env.VITE_API_URL || "/api";
    const url = `${base}/accounts/${accountId}/machines/${machineId}/progress`;
    const maxRetries = 40; // ~5 minutes of retrying (40 * 8s)
    let retryCount = 0;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let cancelled = false;

    function connect() {
      if (cancelled) return;
      const es = new EventSource(url, { withCredentials: true });
      eventSourceRef.current = es;

      es.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data);
          receivedAnyStep.current = true;
          retryCount = 0; // reset retries on success
          if (data.step === "error") {
            setErrorMessage(data.message || "Provisioning failed");
            es.close();
            return;
          }
          if (!stepStartTimes.current[data.step]) {
            stepStartTimes.current[data.step] = Date.now();
          }
          setCurrentStep(data.step);
          if (data.step === "machine_ready") {
            setCompleted(true);
            es.close();
            onCompleteRef.current?.();
          }
        } catch (e) {
          console.error("[ProvisioningProgress] Failed to parse SSE message:", event.data, e);
        }
      };

      es.onerror = () => {
        es.close();
        if (cancelled) return;
        // If we've received steps before, this is a mid-stream disconnect — always retry
        // If no steps yet, the host VM is likely still booting — retry until max
        if (retryCount < maxRetries) {
          retryCount++;
          setConnectAttempt(retryCount);
          retryTimer = setTimeout(connect, 8000);
        } else {
          setErrorMessage("Failed to connect to provisioning service");
        }
      };
    }

    connect();

    return () => {
      cancelled = true;
      if (retryTimer) clearTimeout(retryTimer);
      eventSourceRef.current?.close();
    };
  }, [accountId, machineId]);

  const currentOrder = currentStep
    ? (STEPS.find((s) => s.id === currentStep)?.order ?? 0)
    : 0;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between mb-1">
        <p className="text-sm font-medium text-gray-700">Provisioning</p>
        <span className="text-xs text-gray-400">{formatElapsed(now - startTime.current)}</span>
      </div>
      {!currentStep && !completed && !errorMessage && connectAttempt > 0 && (
        <p className="text-xs text-gray-400 mb-3">Waiting for host VM to boot...</p>
      )}
      {STEPS.map((step) => {
        const stepOrder = step.order;
        const isCompleted = currentOrder > stepOrder || completed;
        const isCurrent = currentStep === step.id && !completed;
        // First step pulses while waiting for SSE connection (no steps received yet)
        const isWaiting = !currentStep && !completed && !errorMessage && step.order === 1;
        const isPending = currentOrder < stepOrder && !completed && !isWaiting;

        // Calculate per-step elapsed time
        const stepStart = stepStartTimes.current[step.id];
        let stepElapsed: number | null = null;
        if (stepStart) {
          if (isCompleted) {
            // For completed steps, use next step's start time
            const nextStepIndex = step.order; // order is 1-based, so step.order is the next index (0-based)
            const nextStep = STEPS[nextStepIndex];
            const nextStepStart = nextStep ? stepStartTimes.current[nextStep.id] : null;
            stepElapsed = (nextStepStart || now) - stepStart;
          } else if (isCurrent) {
            stepElapsed = now - stepStart;
          }
        }

        return (
          <div
            key={step.id}
            className={`flex items-start gap-3 transition-all duration-300 ${
              isCompleted ? "opacity-60" : ""
            }`}
          >
            <div className="flex-shrink-0 mt-0.5">
              {isCompleted && (
                <div className="h-5 w-5 rounded-full bg-green-500 flex items-center justify-center">
                  <svg className="h-3 w-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
                  </svg>
                </div>
              )}
              {(isCurrent || isWaiting) && (
                <div className="h-5 w-5 rounded-full border-2 border-brand-500 flex items-center justify-center">
                  <div className="h-2 w-2 rounded-full bg-brand-500 animate-pulse" />
                </div>
              )}
              {isPending && (
                <div className="h-5 w-5 rounded-full border-2 border-gray-200" />
              )}
            </div>
            <div className="flex-1 flex items-center justify-between">
              <span className={`text-sm ${
                isCompleted ? "text-green-700" : (isCurrent || isWaiting) ? "text-gray-900 dark:text-gray-100 font-medium" : "text-gray-400"
              }`}>
                {step.label}
              </span>
              {(isCompleted || isCurrent) && stepElapsed !== null && (
                <span className="text-xs text-gray-400 ml-2">
                  {formatElapsed(stepElapsed)}
                </span>
              )}
            </div>
          </div>
        );
      })}

      {errorMessage && (
        <div className="mt-3 bg-red-50 border border-red-200 rounded-lg p-4">
          <div className="flex items-start gap-3">
            <div className="flex-shrink-0 mt-0.5">
              <div className="h-5 w-5 rounded-full bg-red-500 flex items-center justify-center">
                <svg className="h-3 w-3 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                </svg>
              </div>
            </div>
            <div className="flex-1">
              <p className="text-sm font-medium text-red-800">
                {currentStep
                  ? `Failed at: ${STEPS.find(s => s.id === currentStep)?.label || currentStep}`
                  : "Provisioning failed"}
              </p>
              <p className="text-sm text-red-600 mt-1">{errorMessage}</p>
              <p className="text-xs text-red-400 mt-1">
                After {formatElapsed(now - startTime.current)}
              </p>
            </div>
          </div>
          {onRetry && (
            <button
              onClick={onRetry}
              className="mt-3 bg-red-600 text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-red-700"
            >
              Retry
            </button>
          )}
        </div>
      )}
    </div>
  );
}
