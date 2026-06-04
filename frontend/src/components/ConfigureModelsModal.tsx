import { useEffect, useMemo, useRef, useState } from "react";
import type { ModelEntry, OpenAIAgentRuntime } from "../lib/types";

// ---------------------------------------------------------------------------
// ConfigureModelsModal
//
// First-stab UI for the "configure models" flow.
//
// Scope of this cut:
//  - Two-column modal: left = current selection, right = provider-grouped catalog
//  - Checkbox per model row = machine allowlist toggle
//  - Role buttons: set default, add to fallback slot (1..3)
//  - Role badges on rows mirror the left column
//  - Search across catalog
//  - Save & apply = single commit button
//  - Confirm-on-cancel when there are unsaved changes
//
// BACKEND TODO (iteration 2):
//  - machine_config.platform_overrides persists `preferred_model` and
//    `model_fallbacks`; extend it with enabled_models or add a dedicated
//    machine_model_selection table.
//  - configassembly emits agents.defaults.model = { primary, fallbacks }.
//  - For now the enabled-model allowlist is kept in local state; the runtime
//    catalog still includes every available model.
// ---------------------------------------------------------------------------

export const MAX_FALLBACKS = 3;

const PROVIDER_LABELS: Record<string, string> = {
  nebius: "Nebius · platform",
  anthropic: "Anthropic",
  openai: "OpenAI",
  google: "Google AI",
  openrouter: "OpenRouter",
  "openai-codex": "OpenAI Codex (ChatGPT)",
};

const PROVIDER_ORDER = [
  "nebius",
  "anthropic",
  "openai",
  "google",
  "openrouter",
  "openai-codex",
];

const OPENAI_RUNTIME_LABELS: Record<OpenAIAgentRuntime, string> = {
  auto: "Auto",
  codex: "Codex",
  pi: "PI",
};

function providerLabel(id: string): string {
  return PROVIDER_LABELS[id] || id;
}

export interface ConfigureModelsState {
  defaultModel: string;
  fallbacks: string[];
  enabled: string[];
  openaiAgentRuntime: OpenAIAgentRuntime;
}

export interface ConfigureModelsModalProps {
  open: boolean;
  onClose: () => void;
  models: ModelEntry[];
  initial: ConfigureModelsState;
  onSave: (next: ConfigureModelsState) => Promise<void>;
}

export function ConfigureModelsModal({
  open,
  onClose,
  models,
  initial,
  onSave,
}: ConfigureModelsModalProps) {
  const [state, setState] = useState<ConfigureModelsState>(initial);
  const [search, setSearch] = useState("");
  const [saving, setSaving] = useState(false);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  // Reset state only on the false→true transition of `open`.
  // We intentionally do NOT depend on `initial` identity: the parent
  // (ModelTab) passes a fresh object literal, and the parent re-renders on
  // unrelated events (e.g. MachineView polls every 5s while booting). Keying
  // off `initial` here would wipe in-progress edits on every poll tick.
  const wasOpenRef = useRef(false);
  // Keep the latest initial snapshot in a ref so the open-transition effect
  // can read it without re-running when `initial` identity changes.
  const latestInitialRef = useRef(initial);
  latestInitialRef.current = initial;
  // Stable snapshot captured on the false→true transition of `open`.
  // Used for dirty-checking so that parent re-renders (e.g. 5s poll) don't
  // flip the dirty flag while the user is editing.
  const openSnapshotRef = useRef(initial);
  useEffect(() => {
    if (open && !wasOpenRef.current) {
      const snapshot = latestInitialRef.current;
      openSnapshotRef.current = snapshot;
      setState(snapshot);
      setSearch("");
      setCollapsed({});
    }
    wasOpenRef.current = open;
  }, [open]);

  const dirty = useMemo(() => {
    const snapshot = openSnapshotRef.current;
    return (
      state.defaultModel !== snapshot.defaultModel ||
      state.fallbacks.join(",") !== snapshot.fallbacks.join(",") ||
      [...state.enabled].sort().join(",") !== [...snapshot.enabled].sort().join(",") ||
      state.openaiAgentRuntime !== snapshot.openaiAgentRuntime
    );
  }, [state]);

  const byId = useMemo(() => {
    const m = new Map<string, ModelEntry>();
    for (const entry of models) m.set(entry.id, entry);
    return m;
  }, [models]);

  const filtered = useMemo(() => {
    if (!search.trim()) return models;
    const q = search.trim().toLowerCase();
    return models.filter(
      (m) =>
        m.id.toLowerCase().includes(q) ||
        m.label.toLowerCase().includes(q) ||
        m.description.toLowerCase().includes(q) ||
        m.provider.toLowerCase().includes(q),
    );
  }, [models, search]);

  const grouped = useMemo(() => {
    const groups: Record<string, ModelEntry[]> = {};
    for (const m of filtered) {
      (groups[m.provider] ||= []).push(m);
    }
    for (const list of Object.values(groups)) {
      list.sort((a, b) => a.sort_order - b.sort_order || a.label.localeCompare(b.label));
    }
    const keys = Object.keys(groups).sort((a, b) => {
      const ai = PROVIDER_ORDER.indexOf(a);
      const bi = PROVIDER_ORDER.indexOf(b);
      return (ai === -1 ? 999 : ai) - (bi === -1 ? 999 : bi);
    });
    return keys.map((k) => ({ provider: k, models: groups[k] }));
  }, [filtered]);

  const hasOpenAIModels = useMemo(
    () => models.some((m) => m.provider === "openai" || m.provider === "openai-codex"),
    [models],
  );

  if (!open) return null;

  const isEnabled = (id: string) => state.enabled.includes(id);
  const fallbackIndex = (id: string) => state.fallbacks.indexOf(id);

  const toggleEnabled = (id: string) => {
    setState((prev) => {
      if (prev.enabled.includes(id)) {
        // Unchecking: also clear any role it held.
        const nextFallbacks = prev.fallbacks.filter((x) => x !== id);
        let nextDefault = prev.defaultModel;
        if (nextDefault === id) {
          // Auto-promote first fallback, else clear.
          nextDefault = nextFallbacks[0] || "";
          if (nextFallbacks[0]) nextFallbacks.shift();
        }
        return {
          ...prev,
          defaultModel: nextDefault,
          fallbacks: nextFallbacks,
          enabled: prev.enabled.filter((x) => x !== id),
        };
      }
      return { ...prev, enabled: [...prev.enabled, id] };
    });
  };

  const setAsDefault = (id: string) => {
    setState((prev) => {
      const enabled = prev.enabled.includes(id) ? prev.enabled : [...prev.enabled, id];
      const fallbacks = prev.fallbacks.filter((x) => x !== id);
      // If the old default exists, push it to the front of fallbacks (if room).
      if (prev.defaultModel && prev.defaultModel !== id) {
        if (!fallbacks.includes(prev.defaultModel) && fallbacks.length < MAX_FALLBACKS) {
          fallbacks.unshift(prev.defaultModel);
        }
      }
      return { ...prev, defaultModel: id, fallbacks, enabled };
    });
  };

  const addFallback = (id: string) => {
    setState((prev) => {
      if (prev.defaultModel === id) return prev;
      if (prev.fallbacks.includes(id)) return prev;
      if (prev.fallbacks.length >= MAX_FALLBACKS) return prev;
      const enabled = prev.enabled.includes(id) ? prev.enabled : [...prev.enabled, id];
      return { ...prev, fallbacks: [...prev.fallbacks, id], enabled };
    });
  };

  const removeFallback = (id: string) => {
    setState((prev) => ({ ...prev, fallbacks: prev.fallbacks.filter((x) => x !== id) }));
  };

  const moveFallback = (id: string, dir: -1 | 1) => {
    setState((prev) => {
      const idx = prev.fallbacks.indexOf(id);
      if (idx === -1) return prev;
      const next = [...prev.fallbacks];
      const swap = idx + dir;
      if (swap < 0 || swap >= next.length) return prev;
      [next[idx], next[swap]] = [next[swap], next[idx]];
      return { ...prev, fallbacks: next };
    });
  };

  const handleCancel = () => {
    if (dirty) {
      const ok = window.confirm("Discard unsaved changes to your model configuration?");
      if (!ok) return;
    }
    onClose();
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(state);
      onClose();
    } catch {
      // Parent (onSave) is responsible for showing error feedback.
      // We stay open so the user can retry.
    } finally {
      setSaving(false);
    }
  };

  const toggleCollapsed = (p: string) =>
    setCollapsed((prev) => ({ ...prev, [p]: !prev[p] }));

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) handleCancel();
      }}
    >
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-xl w-full max-w-5xl max-h-[90vh] flex flex-col">
        {/* Header */}
        <div className="flex items-center justify-between p-4 md:p-5 border-b border-border">
          <div>
            <div className="text-lg md:text-xl font-semibold text-text-primary">
              Configure models
            </div>
            <div className="text-xs text-text-tertiary mt-0.5">
              Pick which models this machine can use, then set a default and fallbacks.
            </div>
          </div>
          <button
            onClick={handleCancel}
            className="text-text-muted hover:text-text-primary text-xl leading-none px-2"
            aria-label="Close"
          >
            ×
          </button>
        </div>

        {/* Body: two columns */}
        <div className="flex-1 overflow-hidden flex flex-col md:flex-row">
          {/* LEFT: current selection */}
          <div className="md:w-[320px] md:flex-shrink-0 border-b md:border-b-0 md:border-r border-border p-4 md:p-5 overflow-y-auto">
            <SelectionPanel
              state={state}
              byId={byId}
              showOpenAIRuntime={hasOpenAIModels}
              onOpenAIRuntimeChange={(openaiAgentRuntime) =>
                setState((prev) => ({ ...prev, openaiAgentRuntime }))
              }
              onRemoveFallback={removeFallback}
              onMoveFallback={moveFallback}
              onClearDefault={() => setState((p) => ({ ...p, defaultModel: "" }))}
            />
          </div>

          {/* RIGHT: catalog browser */}
          <div className="flex-1 overflow-y-auto p-4 md:p-5">
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search models by name, provider, or capability..."
              className="w-full px-3 py-2 text-sm bg-input border border-border rounded-[var(--radius-sm)] text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500 mb-4"
            />

            {grouped.length === 0 && (
              <div className="text-sm text-text-tertiary py-8 text-center">
                {search
                  ? "No models match your search."
                  : "No models available. Connect a provider to get started."}
              </div>
            )}

            <div className="space-y-4">
              {grouped.map(({ provider, models: pModels }) => {
                const enabledCount = pModels.filter((m) => isEnabled(m.id)).length;
                const isCollapsed = collapsed[provider];
                return (
                  <div key={provider} className="border border-border rounded-[var(--radius-sm)]">
                    <button
                      onClick={() => toggleCollapsed(provider)}
                      className="w-full flex items-center justify-between px-3 py-2 bg-elevated hover:bg-card-hover rounded-t-[var(--radius-sm)] text-left"
                    >
                      <div className="flex items-center gap-2">
                        <span className="text-text-tertiary text-xs">
                          {isCollapsed ? "▸" : "▾"}
                        </span>
                        <span className="text-sm font-semibold text-text-primary">
                          {providerLabel(provider)}
                        </span>
                      </div>
                      <span className="text-[11px] text-text-tertiary">
                        {enabledCount} of {pModels.length} enabled
                      </span>
                    </button>
                    {!isCollapsed && (
                      <div className="divide-y divide-border">
                        {pModels.map((m) => (
                          <ModelRow
                            key={m.id}
                            model={m}
                            enabled={isEnabled(m.id)}
                            isDefault={state.defaultModel === m.id}
                            fallbackSlot={fallbackIndex(m.id)}
                            canAddFallback={state.fallbacks.length < MAX_FALLBACKS}
                            onToggleEnabled={() => toggleEnabled(m.id)}
                            onSetDefault={() => setAsDefault(m.id)}
                            onAddFallback={() => addFallback(m.id)}
                            onRemoveRole={() => {
                              if (state.defaultModel === m.id) {
                                setState((p) => ({ ...p, defaultModel: "" }));
                              } else {
                                removeFallback(m.id);
                              }
                            }}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>

            {/* TODO (iteration 2): custom model ID input for free-form BYOK */}
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between gap-3 p-4 md:p-5 border-t border-border">
          <div className="text-xs text-text-tertiary">
            {state.enabled.length} enabled · {state.fallbacks.length}/{MAX_FALLBACKS} fallbacks
            {dirty && <span className="ml-2 text-amber-400">· unsaved changes</span>}
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={handleCancel}
              className="text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] border border-border text-text-secondary hover:bg-[rgba(255,255,255,0.03)]"
            >
              Cancel
            </button>
            <button
              onClick={handleSave}
              disabled={!dirty || saving || !state.defaultModel}
              className="text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50"
            >
              {saving ? "Saving..." : "Save & apply"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------

function SelectionPanel({
  state,
  byId,
  showOpenAIRuntime,
  onOpenAIRuntimeChange,
  onRemoveFallback,
  onMoveFallback,
  onClearDefault,
}: {
  state: ConfigureModelsState;
  byId: Map<string, ModelEntry>;
  showOpenAIRuntime: boolean;
  onOpenAIRuntimeChange: (runtime: OpenAIAgentRuntime) => void;
  onRemoveFallback: (id: string) => void;
  onMoveFallback: (id: string, dir: -1 | 1) => void;
  onClearDefault: () => void;
}) {
  const def = state.defaultModel ? byId.get(state.defaultModel) : undefined;
  return (
    <div className="space-y-5">
      <div>
        <div className="text-[11px] uppercase tracking-wide text-text-muted font-semibold mb-2">
          Default
        </div>
        {def ? (
          <div className="bg-elevated border border-border rounded-[var(--radius-sm)] p-3">
            <div className="text-sm font-semibold text-text-primary">{def.label}</div>
            <div className="text-[11px] text-text-tertiary mt-0.5">
              {providerLabel(def.provider)}
            </div>
            <button
              onClick={onClearDefault}
              className="text-[11px] text-text-muted hover:text-red-400 mt-2"
            >
              Clear
            </button>
          </div>
        ) : state.defaultModel ? (
          <div className="bg-elevated border border-dashed border-border rounded-[var(--radius-sm)] p-3 text-[11px] text-text-tertiary">
            Unknown model: <span className="font-mono">{state.defaultModel}</span>
          </div>
        ) : (
          <div className="bg-elevated border border-dashed border-border rounded-[var(--radius-sm)] p-3 text-[11px] text-text-tertiary">
            No default set. Pick a model on the right.
          </div>
        )}
      </div>

      <div>
        <div className="text-[11px] uppercase tracking-wide text-text-muted font-semibold mb-2">
          Fallbacks ({state.fallbacks.length}/{MAX_FALLBACKS})
        </div>
        {state.fallbacks.length === 0 ? (
          <div className="bg-elevated border border-dashed border-border rounded-[var(--radius-sm)] p-3 text-[11px] text-text-tertiary">
            No fallbacks. Use "+ Fallback" on a model row to add one.
          </div>
        ) : (
          <div className="space-y-1.5">
            {state.fallbacks.map((id, i) => {
              const m = byId.get(id);
              return (
                <div
                  key={id}
                  className="bg-elevated border border-border rounded-[var(--radius-sm)] p-2 flex items-center gap-2"
                >
                  <div className="flex-1 min-w-0">
                    <div className="text-xs font-medium text-text-primary truncate">
                      {i + 1}. {m?.label || id}
                    </div>
                    <div className="text-[10px] text-text-tertiary">
                      {m ? providerLabel(m.provider) : "unknown provider"}
                    </div>
                  </div>
                  <div className="flex items-center gap-1">
                    <button
                      onClick={() => onMoveFallback(id, -1)}
                      disabled={i === 0}
                      className="text-[11px] text-text-muted hover:text-text-primary disabled:opacity-30 w-5"
                      aria-label="Move up"
                    >
                      ↑
                    </button>
                    <button
                      onClick={() => onMoveFallback(id, 1)}
                      disabled={i === state.fallbacks.length - 1}
                      className="text-[11px] text-text-muted hover:text-text-primary disabled:opacity-30 w-5"
                      aria-label="Move down"
                    >
                      ↓
                    </button>
                    <button
                      onClick={() => onRemoveFallback(id)}
                      className="text-[11px] text-text-muted hover:text-red-400 w-5"
                      aria-label="Remove"
                    >
                      ×
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      <div>
        <div className="text-[11px] uppercase tracking-wide text-text-muted font-semibold mb-2">
          Enabled on this machine
        </div>
        <div className="text-xs text-text-secondary">
          {state.enabled.length} model{state.enabled.length === 1 ? "" : "s"}
        </div>
      </div>

      {showOpenAIRuntime && (
        <div>
          <div className="text-[11px] uppercase tracking-wide text-text-muted font-semibold mb-2">
            OpenAI runtime
          </div>
          <div className="grid grid-cols-3 gap-1 bg-elevated border border-border rounded-[var(--radius-sm)] p-1">
            {(Object.keys(OPENAI_RUNTIME_LABELS) as OpenAIAgentRuntime[]).map((runtime) => {
              const active = state.openaiAgentRuntime === runtime;
              return (
                <button
                  key={runtime}
                  type="button"
                  onClick={() => onOpenAIRuntimeChange(runtime)}
                  className={`text-[11px] font-medium px-2 py-1 rounded-[var(--radius-sm)] transition-colors ${
                    active
                      ? "bg-brand-600 text-white"
                      : "text-text-secondary hover:bg-card-hover"
                  }`}
                >
                  {OPENAI_RUNTIME_LABELS[runtime]}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------

function ModelRow({
  model,
  enabled,
  isDefault,
  fallbackSlot,
  canAddFallback,
  onToggleEnabled,
  onSetDefault,
  onAddFallback,
  onRemoveRole,
}: {
  model: ModelEntry;
  enabled: boolean;
  isDefault: boolean;
  fallbackSlot: number; // -1 if not a fallback
  canAddFallback: boolean;
  onToggleEnabled: () => void;
  onSetDefault: () => void;
  onAddFallback: () => void;
  onRemoveRole: () => void;
}) {
  const hasRole = isDefault || fallbackSlot >= 0;
  return (
    <div className="flex items-start gap-3 p-3">
      <input
        type="checkbox"
        checked={enabled}
        onChange={onToggleEnabled}
        className="mt-1 cursor-pointer"
      />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm font-medium text-text-primary">{model.label}</span>
          {isDefault && (
            <span className="text-[10px] font-semibold px-1.5 py-0.5 rounded bg-brand-600/20 text-brand-300 border border-brand-600/40">
              ✦ DEFAULT
            </span>
          )}
          {fallbackSlot >= 0 && (
            <span className="text-[10px] font-semibold px-1.5 py-0.5 rounded bg-blue-600/20 text-blue-300 border border-blue-600/40">
              ● FALLBACK {fallbackSlot + 1}
            </span>
          )}
        </div>
        <div className="text-[11px] text-text-tertiary mt-0.5 truncate">{model.description}</div>
        {model.tier && (
          <div className="text-[10px] text-text-muted font-mono mt-1 uppercase">
            {model.tier}
          </div>
        )}
      </div>
      <div className="flex flex-col items-end gap-1 flex-shrink-0">
        {hasRole ? (
          <button
            onClick={onRemoveRole}
            className="text-[11px] font-medium px-2 py-1 rounded border border-border text-text-muted hover:text-red-400"
          >
            Remove role
          </button>
        ) : (
          <>
            <button
              onClick={onSetDefault}
              disabled={!enabled}
              className="text-[11px] font-medium px-2 py-1 rounded border border-border text-text-secondary hover:bg-[rgba(255,255,255,0.03)] disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Set default
            </button>
            <button
              onClick={onAddFallback}
              disabled={!enabled || !canAddFallback}
              className="text-[11px] font-medium px-2 py-1 rounded border border-border text-text-secondary hover:bg-[rgba(255,255,255,0.03)] disabled:opacity-40 disabled:cursor-not-allowed"
            >
              + Fallback
            </button>
          </>
        )}
      </div>
    </div>
  );
}
