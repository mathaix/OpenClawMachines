import type { Dispatch } from "react";
import type { OnboardingState, OnboardingAction } from "../../pages/OnboardingWizard";
import { CREDENTIAL_PROVIDERS } from "../../lib/types";
import { useSizes } from "../../lib/useSizes";
import { ProvisioningProgress } from "../ProvisioningProgress";

interface StepLaunchProps {
  state: OnboardingState;
  dispatch: Dispatch<OnboardingAction>;
  onLaunch: () => void;
  accountId: number;
  machineId: string;
}

export function StepLaunch({ state, dispatch, onLaunch, accountId, machineId }: StepLaunchProps) {
  const sizes = useSizes();
  const size = sizes.find((s) => s.id === state.machineSizeId);
  const provider = CREDENTIAL_PROVIDERS.find((p) => p.id === state.selectedProvider);
  const channel = CREDENTIAL_PROVIDERS.find((p) => p.id === state.selectedChannel);

  if (state.launchPhase === "provisioning" || state.launchPhase === "complete") {
    return (
      <div className="space-y-5">
        <div>
          <h2 className="text-xl font-semibold text-gray-100">
            {state.launchPhase === "complete" ? "Your machine is ready!" : `Launching your ${state.machineKind === "hermes" ? "Hermes" : "OpenClaw"} machine...`}
          </h2>
        </div>

        <div className="rounded-lg bg-surface-card border border-border p-4">
          <ProvisioningProgress accountId={accountId} machineId={machineId} onComplete={() => dispatch({ type: "SET_LAUNCH_PHASE", phase: "complete" })} />
        </div>

        {state.launchPhase === "complete" && (
          <a
            href={state.machineKind === "hermes" ? `/chat/${machineId}` : `/workspace/${machineId}`}
            className="block w-full bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium text-center transition-colors"
          >
            {state.machineKind === "hermes" ? "Open Webchat" : "Open Workspace"}
          </a>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-gray-100">Ready to launch</h2>
        <p className="text-sm text-gray-400 mt-1">Review your configuration and start your machine.</p>
      </div>

      <div className="rounded-lg bg-surface-card border border-border divide-y divide-border">
        <SummaryRow label="Account" value={state.accountName} />
        <SummaryRow label="Machine" value={state.machineName} />
        <SummaryRow label="Kind" value={state.machineKind === "hermes" ? "Hermes" : "OpenClaw"} />
        <SummaryRow label="Size" value={size ? `${size.label} (${size.description})` : state.machineSizeId} />
        {provider && <SummaryRow label="AI Provider" value={provider.label} />}
        {channel && <SummaryRow label="Channel" value={channel.label} />}
        {state.assistantName && <SummaryRow label="Assistant" value={state.assistantName} />}
      </div>

      {state.error && (
        <p className="text-sm text-red-400">{state.error}</p>
      )}

      <button
        onClick={onLaunch}
        disabled={state.submitting}
        className="w-full bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {state.submitting ? "Starting..." : "Launch Machine"}
      </button>
    </div>
  );
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between px-4 py-3">
      <span className="text-sm text-gray-400">{label}</span>
      <span className="text-sm font-medium text-gray-200">{value}</span>
    </div>
  );
}
