import { useEffect, useState, useCallback, useRef } from "react";
import type { Machine, MachineCredential, ModelEntry, CredentialProvider, OpenAIAgentRuntime } from "../../lib/types";
import { CREDENTIAL_PROVIDERS } from "../../lib/types";
import {
  getMachineModel, setMachineModel, pushMachineConfig, listMachineModels,
  listMachineCredentials, putMachineCredential, deleteMachineCredential,
} from "../../lib/api";
import { OpenAICodexConnect } from "../../components/OpenAICodexConnect";
import { AnthropicSubscriptionConnect } from "../../components/AnthropicSubscriptionConnect";
import { ConfigureModelsModal, type ConfigureModelsState } from "../../components/ConfigureModelsModal";
import { useToast } from "../../components/Toast";
import { isRestartRequiredLiveUpdate } from "../../lib/liveUpdate";

interface ModelTabProps {
  machine: Machine;
  accountId: number;
}

const BYOK_PROVIDERS = ["anthropic", "openai", "google", "openrouter"] as const;
const HERMES_DEFAULT_MODEL = "openai/gpt-oss-120b";

const PROVIDER_SHORT_DESC: Record<string, string> = {
  anthropic: "Claude models",
  openai: "GPT-4o, o3, and more",
  google: "Gemini models",
  openrouter: "200+ models",
};

export function ModelTab({ machine, accountId }: ModelTabProps) {
  const { toast } = useToast();
  const isHermes = machine.kind === "hermes";
  const [models, setModels] = useState<ModelEntry[]>([]);
  const [currentModel, setCurrentModel] = useState<string>("");
  const [openaiAgentRuntime, setOpenAIAgentRuntime] = useState<OpenAIAgentRuntime>("auto");
  const [linkedCredentials, setLinkedCredentials] = useState<MachineCredential[]>([]);
  const [loadingModels, setLoadingModels] = useState(true);
  const [addingProvider, setAddingProvider] = useState<string | null>(null);
  const [keyInput, setKeyInput] = useState("");
  const [addingKey, setAddingKey] = useState(false);
  const [configureOpen, setConfigureOpen] = useState(false);
  // Enabled-model allowlist is still client-side; fallbacks are persisted.
  // See ConfigureModelsModal.tsx "BACKEND TODO".
  const [fallbacks, setFallbacks] = useState<string[]>([]);
  const [enabled, setEnabled] = useState<string[]>([]);

  const fetchCredentials = useCallback(async () => {
    const linked = await listMachineCredentials(accountId, machine.id);
    setLinkedCredentials(linked || []);
  }, [accountId, machine.id]);

  const fetchModels = useCallback(async () => {
    const modelList = await listMachineModels(accountId, machine.id);
    setModels(modelList);
  }, [accountId, machine.id]);

  useEffect(() => {
    Promise.all([
      fetchModels(),
      getMachineModel(accountId, machine.id),
      fetchCredentials(),
    ])
      .then(([, modelResp]) => {
        setCurrentModel(modelResp.model || "");
        setOpenAIAgentRuntime(modelResp.openai_agent_runtime || "auto");
        setFallbacks(modelResp.fallbacks || []);
      })
      .catch((err) => {
        console.error("Failed to load model configuration:", err);
        toast({
          title: "Failed to load model configuration",
          description: err instanceof Error ? err.message : "Please try refreshing the page.",
          variant: "error",
        });
      })
      .finally(() => setLoadingModels(false));
  }, [accountId, machine.id, fetchCredentials, fetchModels]);

  // Reconcile local allowlist/fallbacks with the current catalog.
  // Runs every time the catalog changes (e.g. after credentials are added/removed).
  //  - New models become enabled by default.
  //  - Removed models are pruned from `enabled` and `fallbacks`.
  //  - If `currentModel` disappears, clear it.
  //  - On first catalog load, seed fallbacks with the non-default Nebius tiers.
  const seededFallbacksRef = useRef(false);
  const effectiveCurrentModel = currentModel || (isHermes ? HERMES_DEFAULT_MODEL : "");

  useEffect(() => {
    if (loadingModels) return;
    const validIds = new Set(models.map((m) => m.id));

    setEnabled((prev) => {
      const prevSet = new Set(prev);
      const kept = prev.filter((id) => validIds.has(id));
      const added = models.map((m) => m.id).filter((id) => !prevSet.has(id));
      return [...kept, ...added];
    });

    setFallbacks((prev) => {
      const pruned = prev.filter((id) => validIds.has(id));
      if (seededFallbacksRef.current || models.length === 0) return pruned;
      seededFallbacksRef.current = true;
      if (pruned.length > 0) return pruned;
      const tierOrder: Record<string, number> = { smart: 0, balanced: 1, fast: 2 };
      return models
        .filter((m) => m.source === "platform" && m.id !== effectiveCurrentModel && m.tier)
        .sort((a, b) => (tierOrder[a.tier!] ?? 99) - (tierOrder[b.tier!] ?? 99))
        .slice(0, 2)
        .map((m) => m.id);
    });

    if (currentModel && !validIds.has(currentModel)) {
      setCurrentModel("");
    }
  }, [loadingModels, models, currentModel, effectiveCurrentModel]);

  const modelPushToast = (liveUpdate: string, error?: string) => {
    if (liveUpdate === "failed") {
      if (isRestartRequiredLiveUpdate(error)) {
        toast({
          title: isHermes ? "Model saved, restart Hermes to apply" : "Model configuration saved for next start",
          description: error,
          variant: "success",
        });
        return;
      }
      toast({
        title: isHermes ? "Saved but Hermes sync failed" : "Saved but failed to push config to VM",
        description: error,
        variant: "error",
      });
      return;
    }
    if (liveUpdate === "not_running") {
      toast({
        title: isHermes ? "Model saved for next Hermes start" : "Model configuration saved",
        variant: "success",
      });
      return;
    }
    if (liveUpdate === "skipped") {
      toast({
        title: "Model configuration saved",
        description: "Live VM sync was skipped.",
        variant: "success",
      });
      return;
    }
    toast({
      title: isHermes ? "Hermes model synced" : "Model configuration updated",
      variant: "success",
    });
  };

  const handleConfigureSave = async (next: ConfigureModelsState) => {
    try {
      // Persist the default through the existing API.
      const nextDefault = next.defaultModel || (isHermes ? HERMES_DEFAULT_MODEL : "");
      const defaultChanged = nextDefault !== currentModel && !(isHermes && !currentModel && nextDefault === HERMES_DEFAULT_MODEL);
      const runtimeChanged = next.openaiAgentRuntime !== openaiAgentRuntime;
      const fallbacksChanged = next.fallbacks.join(",") !== fallbacks.join(",");
      if (nextDefault && (defaultChanged || runtimeChanged || fallbacksChanged)) {
        await setMachineModel(accountId, machine.id, next.defaultModel, next.openaiAgentRuntime, next.fallbacks);
      }

      const pushRes = await pushMachineConfig(accountId, machine.id);

      // All async ops succeeded — now update local state.
      if (nextDefault && nextDefault !== currentModel) {
        setCurrentModel(nextDefault);
      }
      setOpenAIAgentRuntime(next.openaiAgentRuntime);
      // Allowlist: client-side only for now. See modal's BACKEND TODO.
      setFallbacks(next.fallbacks);
      setEnabled(next.enabled);
      modelPushToast(pushRes.live_update, pushRes.live_update_error);
    } catch (err) {
      toast({
        title: "Failed to update model configuration",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
      throw err;
    }
  };

  const handleAddKey = async (providerId: string) => {
    if (!keyInput.trim()) return;
    setAddingKey(true);
    try {
      const providerInfo = CREDENTIAL_PROVIDERS.find((p) => p.id === providerId);
      await putMachineCredential(accountId, machine.id, providerId as CredentialProvider, {
        value: keyInput.trim(),
        credential_type: providerInfo?.defaultCredentialType || "api_key",
        label: `${providerInfo?.label || providerId} API key`,
      });
      await Promise.all([fetchCredentials(), fetchModels()]);
      if (machine.status === "running") {
        const pushRes = await pushMachineConfig(accountId, machine.id);
        if (pushRes.live_update === "failed") {
          setAddingProvider(null);
          setKeyInput("");
          if (isRestartRequiredLiveUpdate(pushRes.live_update_error)) {
            toast({
              title: `${providerInfo?.label || providerId} connected`,
              description: pushRes.live_update_error,
              variant: "success",
            });
            return;
          }
          toast({
            title: `${providerInfo?.label || providerId} connected`,
            description: isHermes ? "Key saved but Hermes sync failed." : "Key saved but failed to push config to running VM.",
            variant: "error",
          });
          return;
        }
      }
      setAddingProvider(null);
      setKeyInput("");
      toast({
        title: `${providerInfo?.label || providerId} connected`,
        description: isHermes && machine.status === "running" ? "Hermes model config synced." : undefined,
        variant: "success",
      });
    } catch (err) {
      toast({
        title: "Failed to add key",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setAddingKey(false);
    }
  };

  const handleDisconnect = async (provider: string, providerLabel: string) => {
    try {
      await deleteMachineCredential(accountId, machine.id, provider);
      await Promise.all([fetchCredentials(), fetchModels()]);
      if (machine.status === "running") {
        const pushRes = await pushMachineConfig(accountId, machine.id);
        if (pushRes.live_update === "failed") {
          if (isRestartRequiredLiveUpdate(pushRes.live_update_error)) {
            toast({
              title: `${providerLabel} disconnected`,
              description: pushRes.live_update_error,
              variant: "success",
            });
            return;
          }
          toast({
            title: `${providerLabel} disconnected`,
            description: isHermes ? "Credential removed but Hermes sync failed." : "Credential removed but failed to push config to running VM.",
            variant: "error",
          });
          return;
        }
      }
      toast({
        title: `${providerLabel} disconnected`,
        description: isHermes && machine.status === "running" ? "Hermes model config synced." : undefined,
        variant: "success",
      });
    } catch (err) {
      toast({
        title: "Failed to disconnect",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    }
  };

  const defaultEntry = models.find((m) => m.id === effectiveCurrentModel);
  const providerCount = new Set(
    models.filter((m) => enabled.includes(m.id)).map((m) => m.provider),
  ).size;

  return (
    <div className="space-y-4">
      {/* Section 1: Your model configuration (summary + Configure button) */}
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card">
        <div className="p-4 md:p-6">
          <div className="flex items-start justify-between gap-3 mb-4">
            <div>
              <div className="text-lg md:text-xl font-semibold text-text-primary mb-1">
                Your model configuration
              </div>
              <div className="text-xs md:text-sm text-text-tertiary leading-relaxed">
                The default runs first; fallbacks are tried in order if it fails.
              </div>
            </div>
            <button
              onClick={() => setConfigureOpen(true)}
              disabled={loadingModels}
              className="text-xs md:text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 whitespace-nowrap"
            >
              Configure models
            </button>
          </div>

          {loadingModels ? (
            <div className="h-20 bg-card-hover rounded-[var(--radius)] animate-pulse" />
          ) : (
            <div className="space-y-3">
              <div>
                <div className="text-[11px] uppercase tracking-wide text-text-muted font-semibold mb-1">
                  Default
                </div>
                {defaultEntry ? (
                  <div className="bg-elevated border border-border rounded-[var(--radius-sm)] p-3">
                    <div className="text-sm font-semibold text-text-primary">
                      {defaultEntry.label}
                    </div>
                    <div className="text-[11px] text-text-tertiary mt-0.5">
                      {defaultEntry.description}
                    </div>
                  </div>
                ) : (
                  <div className="bg-elevated border border-dashed border-border rounded-[var(--radius-sm)] p-3 text-[11px] text-text-tertiary">
                    No default set. Click "Configure models" to pick one.
                  </div>
                )}
              </div>

              <div>
                <div className="text-[11px] uppercase tracking-wide text-text-muted font-semibold mb-1">
                  Fallbacks
                </div>
                {fallbacks.length === 0 ? (
                  <div className="text-[11px] text-text-tertiary">None</div>
                ) : (
                  <div className="flex flex-wrap gap-1.5">
                    {fallbacks.map((id, i) => {
                      const m = models.find((x) => x.id === id);
                      return (
                        <span
                          key={id}
                          className="text-[11px] px-2 py-1 rounded bg-elevated border border-border text-text-secondary"
                        >
                          {i + 1}. {m?.label || id}
                        </span>
                      );
                    })}
                  </div>
                )}
              </div>

              <div className="text-[11px] text-text-tertiary pt-1">
                {enabled.length} models enabled · {providerCount} provider
                {providerCount === 1 ? "" : "s"}
              </div>
            </div>
          )}
        </div>
      </div>

      <ConfigureModelsModal
        open={configureOpen}
        onClose={() => setConfigureOpen(false)}
        models={models}
        initial={{
          defaultModel: effectiveCurrentModel,
          fallbacks,
          enabled,
          openaiAgentRuntime,
        }}
        onSave={handleConfigureSave}
      />

      {!isHermes && (
        <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
          <div className="p-4 md:p-6">
            <div className="text-lg md:text-xl font-semibold text-text-primary mb-1">Subscription Models</div>
            <div className="text-xs md:text-sm text-text-tertiary mb-4 leading-relaxed">
              Use your existing AI subscriptions to access models. Connect your Anthropic or OpenAI account below.
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <AnthropicSubscriptionConnect
                accountId={accountId}
                machineId={machine.id}
                initialConnected={linkedCredentials.some(
                  (c) => c.provider === "anthropic" && c.credential_type === "subscription_key"
                )}
                onCredentialChange={() => {
                  Promise.all([fetchCredentials(), fetchModels()]).catch((err) => {
                    console.error("Failed to refresh after credential change:", err);
                    toast({
                      title: "Failed to refresh models",
                      description: "Please reload the page to see updated models.",
                      variant: "error",
                    });
                  });
                }}
              />
              <OpenAICodexConnect
                accountId={accountId}
                machineId={machine.id}
                machineStatus={machine.status}
              />
            </div>
          </div>
        </div>
      )}

      {/* Section 3: Bring Your Own API Key */}
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <div className="text-lg md:text-xl font-semibold text-text-primary mb-1">Bring Your Own API Key</div>
          <div className="text-xs md:text-sm text-text-tertiary mb-4 leading-relaxed">
            Set up your own provider API key. The same flow works for all supported providers: authenticate, then choose the model.
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {BYOK_PROVIDERS.map((providerId) => {
              const providerInfo = CREDENTIAL_PROVIDERS.find((p) => p.id === providerId);
              const linkedCred = linkedCredentials.find((c) => c.provider === providerId);
              const isConnected = linkedCred != null && linkedCred.credential_type !== "subscription_key";
              const isAdding = addingProvider === providerId;

              return (
                <div key={providerId} className="flex flex-col">
                  <div
                    className={`flex items-center gap-3 bg-elevated border rounded-[var(--radius-sm)] p-3 md:p-[14px] transition-all hover:bg-card-hover ${
                      isConnected
                        ? "border-border hover:border-[var(--border-hover)]"
                        : "border-dashed border-[var(--border-hover)]"
                    }`}
                  >
                    <div className="flex-shrink-0 w-[36px] h-[36px] md:w-10 md:h-10 rounded-lg flex items-center justify-center bg-white p-1.5">
                      <img
                        src={`/providers/${providerId}.svg`}
                        alt={providerInfo?.label || providerId}
                        className="w-full h-full object-contain"
                      />
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="text-[12px] md:text-[13px] font-semibold text-text-primary mb-px">
                        {providerInfo?.label || providerId}
                      </div>
                      {isConnected && linkedCred ? (
                        <div className="text-[11px] text-text-tertiary font-mono">
                          ····{linkedCred.last_four}
                        </div>
                      ) : (
                        <div className="text-[11px] text-text-tertiary">
                          {PROVIDER_SHORT_DESC[providerId]}
                        </div>
                      )}
                    </div>
                    <div className="flex-shrink-0">
                      {isConnected ? (
                        <div className="flex items-center gap-2">
                          <span className={`text-[11px] font-medium ${linkedCred?.status === "reauth_required" ? "text-red-400" : "text-green-500"}`}>
                            {linkedCred?.status === "reauth_required" ? "Re-auth" : "Connected"}
                          </span>
                          <button
                            onClick={() => handleDisconnect(providerId, providerInfo?.label || providerId)}
                            className="text-[10px] text-text-muted hover:text-red-400 transition-colors"
                          >
                            ×
                          </button>
                        </div>
                      ) : (
                        <button
                          onClick={() => { setAddingProvider(isAdding ? null : providerId); setKeyInput(""); }}
                          className="text-xs md:text-sm font-medium px-2.5 py-1 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary transition-colors"
                        >
                          {isAdding ? "Cancel" : "Add key"}
                        </button>
                      )}
                    </div>
                  </div>

                  {/* Inline key input */}
                  {isAdding && (
                    <div className="flex gap-2 mt-1.5 px-2">
                      <input
                        type="password"
                        value={keyInput}
                        onChange={(e) => setKeyInput(e.target.value)}
                        placeholder={providerInfo?.placeholder || "Paste API key..."}
                        className="flex-1 px-3 py-1.5 text-[12px] bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                        onKeyDown={(e) => e.key === "Enter" && handleAddKey(providerId)}
                        autoFocus
                      />
                      <button
                        onClick={() => handleAddKey(providerId)}
                        disabled={!keyInput.trim() || addingKey}
                        className="text-[11px] font-medium px-2.5 py-1 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
                      >
                        {addingKey ? "Saving..." : "Save"}
                      </button>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}
