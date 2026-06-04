import type { Dispatch } from "react";
import type { OnboardingState, OnboardingAction } from "../../pages/OnboardingWizard";
import { CREDENTIAL_PROVIDERS } from "../../lib/types";

const AI_PROVIDERS = CREDENTIAL_PROVIDERS.filter((p) => p.category === "ai");

interface StepProviderProps {
  state: OnboardingState;
  dispatch: Dispatch<OnboardingAction>;
  onContinue: () => void;
  onSkip: () => void;
  submitting: boolean;
}

export function StepProvider({ state, dispatch, onContinue, onSkip, submitting }: StepProviderProps) {
  const selected = AI_PROVIDERS.find((p) => p.id === state.selectedProvider);

  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-gray-100">Add an AI provider</h2>
        <p className="text-sm text-gray-400 mt-1">Connect an API key so your machine can use AI models.</p>
      </div>

      <div className="grid grid-cols-3 gap-3">
        {AI_PROVIDERS.map((provider) => {
          const isSelected = state.selectedProvider === provider.id;
          return (
            <button
              key={provider.id}
              type="button"
              onClick={() =>
                dispatch({
                  type: "SET_FIELD",
                  field: "selectedProvider",
                  value: isSelected ? null : provider.id,
                })
              }
              className={`flex flex-col items-center gap-2 rounded-lg border p-4 transition-colors ${
                isSelected
                  ? "bg-surface-card border-brand-500 ring-1 ring-brand-500/30"
                  : "bg-surface-card border-border hover:border-gray-600"
              }`}
            >
              <div className={`h-9 w-9 rounded-full ${provider.iconBg} flex items-center justify-center text-white text-sm font-bold`}>
                {provider.iconLetter}
              </div>
              <span className="text-xs font-medium text-gray-300">{provider.label}</span>
            </button>
          );
        })}
      </div>

      {selected && (
        <div>
          <label htmlFor="providerKey" className="block text-sm font-medium text-gray-300 mb-1.5">
            {selected.label} API Key
          </label>
          <input
            id="providerKey"
            type="password"
            value={state.credentialValue}
            onChange={(e) => dispatch({ type: "SET_FIELD", field: "credentialValue", value: e.target.value })}
            placeholder={selected.placeholder}
            className="w-full rounded-lg bg-surface-input border border-border text-gray-100 px-3 py-2.5 text-sm placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent font-mono"
          />
        </div>
      )}

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
          disabled={submitting || !state.selectedProvider || !state.credentialValue.trim()}
          className="flex-1 bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {submitting ? "Saving..." : "Continue"}
        </button>
      </div>
    </div>
  );
}
