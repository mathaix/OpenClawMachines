import type { Dispatch } from "react";
import type { OnboardingState, OnboardingAction } from "../../pages/OnboardingWizard";
import { toSlug } from "../../lib/utils";

const DATA_PLANE_DOMAIN = (import.meta.env.VITE_DATA_PLANE_DOMAIN || "localhost").trim().replace(/^\./, "");

interface StepAccountProps {
  state: OnboardingState;
  dispatch: Dispatch<OnboardingAction>;
  onContinue: () => void;
  submitting: boolean;
}

export function StepAccount({ state, dispatch, onContinue, submitting }: StepAccountProps) {
  const effectiveSlug = state.accountSlugTouched ? state.accountSlug : toSlug(state.accountName);

  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-gray-100">Create your account</h2>
        <p className="text-sm text-gray-400 mt-1">This is your workspace on OpenClaw Machines.</p>
      </div>

      <div className="space-y-4">
        <div>
          <label htmlFor="accountName" className="block text-sm font-medium text-gray-300 mb-1.5">
            Display Name
          </label>
          <input
            id="accountName"
            type="text"
            value={state.accountName}
            onChange={(e) => dispatch({ type: "SET_FIELD", field: "accountName", value: e.target.value })}
            placeholder="Your name or team name"
            className="w-full rounded-lg bg-surface-input border border-border text-gray-100 px-3 py-2.5 text-sm placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
          />
        </div>

        <div>
          <label htmlFor="accountSlug" className="block text-sm font-medium text-gray-300 mb-1.5">
            URL Slug
            <span className="text-gray-500 font-normal ml-1">(optional)</span>
          </label>
          <div className="text-xs text-gray-500 mb-1.5 font-mono">
            {effectiveSlug || "your-slug"}.{DATA_PLANE_DOMAIN}
          </div>
          <input
            id="accountSlug"
            type="text"
            value={state.accountSlugTouched ? state.accountSlug : effectiveSlug}
            onChange={(e) => {
              dispatch({ type: "SET_FIELD", field: "accountSlugTouched", value: true });
              dispatch({ type: "SET_FIELD", field: "accountSlug", value: e.target.value });
            }}
            placeholder="auto-generated from name"
            className="w-full rounded-lg bg-surface-input border border-border text-gray-100 px-3 py-2.5 text-sm placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
          />
        </div>
      </div>

      {state.error && (
        <p className="text-sm text-red-400">{state.error}</p>
      )}

      <button
        onClick={onContinue}
        disabled={submitting || !state.accountName.trim()}
        className="w-full bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
      >
        {submitting ? "Creating account..." : "Continue"}
      </button>
    </div>
  );
}
