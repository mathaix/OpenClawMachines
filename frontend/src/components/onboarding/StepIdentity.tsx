import type { Dispatch } from "react";
import type { OnboardingState, OnboardingAction } from "../../pages/OnboardingWizard";

interface StepIdentityProps {
  state: OnboardingState;
  dispatch: Dispatch<OnboardingAction>;
  onContinue: () => void;
  onSkip: () => void;
  submitting: boolean;
}

export function StepIdentity({ state, dispatch, onContinue, onSkip, submitting }: StepIdentityProps) {
  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-gray-100">Set up your assistant</h2>
        <p className="text-sm text-gray-400 mt-1">Give your AI assistant a name and personality.</p>
      </div>

      <div className="space-y-4">
        <div>
          <label htmlFor="assistantName" className="block text-sm font-medium text-gray-300 mb-1.5">
            Assistant Name
          </label>
          <input
            id="assistantName"
            type="text"
            value={state.assistantName}
            onChange={(e) => dispatch({ type: "SET_FIELD", field: "assistantName", value: e.target.value })}
            placeholder="e.g. Claw, Atlas, Helper..."
            className="w-full rounded-lg bg-surface-input border border-border text-gray-100 px-3 py-2.5 text-sm placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
          />
        </div>

      </div>

      {state.error && (
        <p className="text-sm text-red-400">{state.error}</p>
      )}

      <div className="flex gap-3">
        <button
          onClick={onSkip}
          disabled={submitting}
          className="flex-1 rounded-lg border border-border px-4 py-2.5 text-sm font-medium text-gray-400 hover:bg-surface-elevated transition-colors disabled:opacity-50"
        >
          Skip
        </button>
        <button
          onClick={onContinue}
          disabled={submitting}
          className="flex-1 bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {submitting ? "Saving..." : "Continue"}
        </button>
      </div>
    </div>
  );
}
