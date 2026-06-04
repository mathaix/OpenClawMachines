import type { Dispatch } from "react";
import type { OnboardingState, OnboardingAction } from "../../pages/OnboardingWizard";
import { useSizes } from "../../lib/useSizes";

interface StepMachineProps {
  state: OnboardingState;
  dispatch: Dispatch<OnboardingAction>;
  onContinue: () => void;
  submitting: boolean;
}

export function StepMachine({ state, dispatch, onContinue, submitting }: StepMachineProps) {
  const sizes = useSizes();

  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-gray-100">Configure your machine</h2>
        <p className="text-sm text-gray-400 mt-1">Choose a name and size for your first machine.</p>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-300 mb-2">Machine Kind</label>
        <div className="grid grid-cols-2 gap-3">
          {([
            ["openclaw", "OpenClaw", "General-purpose OpenClaw agent"],
            ["hermes", "Hermes", "Hermes agent with in-VM browser tools"],
          ] as const).map(([kind, label, description]) => {
            const isSelected = state.machineKind === kind;
            return (
              <button
                key={kind}
                type="button"
                onClick={() => dispatch({ type: "SET_FIELD", field: "machineKind", value: kind })}
                className={`w-full text-left rounded-lg border p-3.5 transition-colors ${
                  isSelected
                    ? "bg-surface-card border-brand-500 ring-1 ring-brand-500/30"
                    : "bg-surface-card border-border hover:border-gray-600"
                }`}
              >
                <span className="text-sm font-medium text-gray-100">{label}</span>
                <p className="text-xs text-gray-400 mt-0.5">{description}</p>
              </button>
            );
          })}
        </div>
      </div>

      <div>
        <label htmlFor="machineName" className="block text-sm font-medium text-gray-300 mb-1.5">
          Machine Name
        </label>
        <input
          id="machineName"
          type="text"
          value={state.machineName}
          onChange={(e) => dispatch({ type: "SET_FIELD", field: "machineName", value: e.target.value })}
          placeholder="my-machine"
          className="w-full rounded-lg bg-surface-input border border-border text-gray-100 px-3 py-2.5 text-sm placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-300 mb-2">Machine Size</label>
        <div className="grid gap-3">
          {sizes.map((size) => {
            const isSelected = state.machineSizeId === size.id;
            return (
              <button
                key={size.id}
                type="button"
                onClick={() => dispatch({ type: "SET_FIELD", field: "machineSizeId", value: size.id })}
                className={`w-full text-left rounded-lg border p-3.5 transition-colors ${
                  isSelected
                    ? "bg-surface-card border-brand-500 ring-1 ring-brand-500/30"
                    : "bg-surface-card border-border hover:border-gray-600"
                }`}
              >
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium text-gray-100">{size.label}</span>
                  {isSelected && (
                    <svg className="h-4 w-4 text-brand-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
                    </svg>
                  )}
                </div>
                <p className="text-xs text-gray-400 mt-0.5">{size.description}</p>
              </button>
            );
          })}
        </div>
      </div>

      {state.error && (
        <p className="text-sm text-red-400">{state.error}</p>
      )}

      <button
        onClick={onContinue}
        disabled={submitting || !state.machineName.trim()}
        className="w-full bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {submitting ? "Creating machine..." : "Continue"}
      </button>
    </div>
  );
}
