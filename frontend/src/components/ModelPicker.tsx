import { useState, useRef, useEffect } from "react";
import type { ModelEntry } from "../lib/types";

const TIER_STYLES: Record<string, { icon: string; color: string; bg: string }> = {
  smart: { icon: "brain", color: "text-purple-600 dark:text-purple-400", bg: "bg-purple-50 dark:bg-purple-900/20 border-purple-200 dark:border-purple-800" },
  balanced: { icon: "scale", color: "text-blue-600 dark:text-blue-400", bg: "bg-blue-50 dark:bg-blue-900/20 border-blue-200 dark:border-blue-800" },
  fast: { icon: "zap", color: "text-amber-600 dark:text-amber-400", bg: "bg-amber-50 dark:bg-amber-900/20 border-amber-200 dark:border-amber-800" },
};

const PROVIDER_LABELS: Record<string, string> = {
  nebius: "Nebius",
  anthropic: "Anthropic",
  openai: "OpenAI",
  google: "Google AI",
  openrouter: "OpenRouter",
  "openai-codex": "OpenAI Codex",
};

function providerLabel(id: string): string {
  return PROVIDER_LABELS[id] || id;
}

interface ModelPickerProps {
  value: string;
  onChange: (modelId: string) => void;
  disabled?: boolean;
  models: ModelEntry[];
}

export function ModelPicker({ value, onChange, disabled, models }: ModelPickerProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const platformTiers = models.filter(m => m.source === "platform");
  const byokModels = models.filter(m => m.source === "byok");
  const subscriptionModels = models.filter(m => m.source === "subscription");

  const selected = models.find((m) => m.id === value);
  const selectedTier = selected?.tier;

  return (
    <div ref={ref} className="relative">
      {/* Trigger */}
      <button
        type="button"
        onClick={() => !disabled && setOpen(!open)}
        disabled={disabled}
        className="flex items-center gap-2 text-sm border border-gray-200 dark:border-border bg-white dark:bg-surface-input text-gray-700 dark:text-gray-300 rounded px-3 py-1.5 hover:border-gray-300 dark:hover:border-gray-600 disabled:opacity-50 min-w-[200px] text-left"
      >
        <span className="flex-1 truncate">
          {selected
            ? selectedTier
              ? `${selected.label} — ${selected.description}`
              : selected.label
            : "Balanced"}
        </span>
        <svg className="w-4 h-4 text-gray-400 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
        </svg>
      </button>

      {/* Dropdown */}
      {open && (
        <div className="absolute z-50 mt-1 w-[400px] rounded-lg border border-gray-200 dark:border-border bg-white dark:bg-surface shadow-lg overflow-hidden">

          {/* Platform tiers */}
          <div className="px-3 pt-3 pb-1.5">
            <p className="text-[10px] uppercase tracking-wider font-semibold text-gray-400 dark:text-gray-500">
              Choose a tier
            </p>
          </div>

          <div className="px-2 pb-2 space-y-1.5">
            {platformTiers.map((tier) => {
              const style = TIER_STYLES[tier.tier!];
              const isSelected = value === tier.id || (!value && tier.tier === "balanced");
              return (
                <button
                  key={tier.id}
                  type="button"
                  onClick={() => { onChange(tier.id); setOpen(false); }}
                  className={`w-full text-left rounded-md border px-3 py-2.5 transition-colors ${
                    isSelected ? style.bg : "border-transparent hover:bg-gray-50 dark:hover:bg-surface-hover"
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <div>
                      <span className={`text-sm font-semibold ${style.color}`}>
                        {tier.label}
                      </span>
                      <span className="text-sm text-gray-500 dark:text-gray-400 ml-2">
                        {tier.description}
                      </span>
                    </div>
                  </div>
                  <div className="text-[10px] text-gray-400 dark:text-gray-500 mt-0.5">
                    {providerLabel(tier.provider)}
                  </div>
                </button>
              );
            })}
          </div>

          {/* BYOK section */}
          {byokModels.length > 0 && (
            <div className="border-t border-gray-100 dark:border-border">
              <div className="px-3 pt-2.5 pb-1">
                <p className="text-[10px] uppercase tracking-wider font-semibold text-gray-400 dark:text-gray-500">
                  Bring Your Own Key
                </p>
              </div>
              {byokModels.map((m) => (
                <button
                  key={m.id}
                  type="button"
                  onClick={() => { onChange(m.id); setOpen(false); }}
                  className={`w-full text-left px-3 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-surface-hover ${
                    value === m.id ? "bg-blue-50 dark:bg-blue-900/20" : ""
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <div>
                      <span className="font-medium text-gray-900 dark:text-gray-100">{m.label}</span>
                      <span className="text-gray-400 dark:text-gray-500 ml-1.5 text-xs">{m.description}</span>
                    </div>
                  </div>
                </button>
              ))}
            </div>
          )}

          {/* Subscription section */}
          {subscriptionModels.length > 0 && (
            <div className="border-t border-gray-100 dark:border-border">
              <div className="px-3 pt-2.5 pb-1">
                <p className="text-[10px] uppercase tracking-wider font-semibold text-gray-400 dark:text-gray-500">
                  Subscription
                </p>
              </div>
              {subscriptionModels.map((m) => (
                <button
                  key={m.id}
                  type="button"
                  onClick={() => { onChange(m.id); setOpen(false); }}
                  className={`w-full text-left px-3 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-surface-hover ${
                    value === m.id ? "bg-blue-50 dark:bg-blue-900/20" : ""
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <div>
                      <span className="font-medium text-gray-900 dark:text-gray-100">{m.label}</span>
                      <span className="text-gray-400 dark:text-gray-500 ml-1.5 text-xs">{m.description}</span>
                    </div>
                    <span className="text-[11px] text-emerald-500 dark:text-emerald-400">included</span>
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
