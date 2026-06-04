import type { Dispatch } from "react";
import type { OnboardingState, OnboardingAction } from "../../pages/OnboardingWizard";
import { CREDENTIAL_PROVIDERS } from "../../lib/types";

const CHANNEL_PROVIDERS = CREDENTIAL_PROVIDERS.filter((p) => p.category === "automation");

interface StepChannelsProps {
  state: OnboardingState;
  dispatch: Dispatch<OnboardingAction>;
  onContinue: () => void;
  onSkip: () => void;
  submitting: boolean;
}

export function StepChannels({ state, dispatch, onContinue, onSkip, submitting }: StepChannelsProps) {
  const selected = CHANNEL_PROVIDERS.find((p) => p.id === state.selectedChannel);

  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-xl font-semibold text-gray-100">Connect a channel</h2>
        <p className="text-sm text-gray-400 mt-1">Link a messaging platform to interact with your machine.</p>
      </div>

      <div className="grid grid-cols-3 gap-3">
        {CHANNEL_PROVIDERS.map((channel) => {
          const isSelected = state.selectedChannel === channel.id;
          return (
            <button
              key={channel.id}
              type="button"
              onClick={() =>
                dispatch({
                  type: "SET_FIELD",
                  field: "selectedChannel",
                  value: isSelected ? null : channel.id,
                })
              }
              className={`flex flex-col items-center gap-2 rounded-lg border p-4 transition-colors ${
                isSelected
                  ? "bg-surface-card border-brand-500 ring-1 ring-brand-500/30"
                  : "bg-surface-card border-border hover:border-gray-600"
              }`}
            >
              <div className={`h-9 w-9 rounded-full ${channel.iconBg} flex items-center justify-center text-white text-sm font-bold`}>
                {channel.iconLetter}
              </div>
              <span className="text-xs font-medium text-gray-300">{channel.label}</span>
            </button>
          );
        })}
      </div>

      {selected && (
        <div>
          <label htmlFor="channelToken" className="block text-sm font-medium text-gray-300 mb-1.5">
            {selected.label} Token
          </label>
          <input
            id="channelToken"
            type="password"
            value={state.channelToken}
            onChange={(e) => dispatch({ type: "SET_FIELD", field: "channelToken", value: e.target.value })}
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
          disabled={submitting || !state.selectedChannel || !state.channelToken.trim()}
          className="flex-1 bg-brand-500 hover:bg-brand-600 text-white rounded-lg px-4 py-2.5 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {submitting ? "Saving..." : "Continue"}
        </button>
      </div>
    </div>
  );
}
