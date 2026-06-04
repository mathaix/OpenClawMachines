import { useEffect, useState, useCallback } from "react";
import type { Machine, MachineCredential, ProviderCatalogEntry } from "../../lib/types";
import {
  listProviderCatalog,
  listMachineCredentials,
  putMachineCredential,
  deleteMachineCredential,
  pushMachineConfig,
} from "../../lib/api";
import { useToast } from "../../components/Toast";

interface WebSearchTabProps {
  machine: Machine;
  accountId: number;
}

export function WebSearchTab({ machine, accountId }: WebSearchTabProps) {
  const { toast } = useToast();
  const [providers, setProviders] = useState<ProviderCatalogEntry[]>([]);
  const [credentials, setCredentials] = useState<MachineCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [addingProvider, setAddingProvider] = useState<string | null>(null);
  const [keyInput, setKeyInput] = useState("");
  const [savingKey, setSavingKey] = useState(false);

  const fetchCredentials = useCallback(async () => {
    const creds = await listMachineCredentials(accountId, machine.id);
    setCredentials(creds || []);
  }, [accountId, machine.id]);

  const fetchProviders = useCallback(async () => {
    const catalog = await listProviderCatalog("search");
    setProviders(catalog.sort((a, b) => a.sort_order - b.sort_order));
  }, []);

  useEffect(() => {
    Promise.all([fetchProviders(), fetchCredentials()])
      .catch((err) => {
        console.error("Failed to load web search configuration:", err);
        toast({
          title: "Failed to load web search configuration",
          description: err instanceof Error ? err.message : "Please try refreshing the page.",
          variant: "error",
        });
      })
      .finally(() => setLoading(false));
  }, [fetchProviders, fetchCredentials, toast]);

  // Find the credential for a given provider
  const getCredentialForProvider = (providerId: string): MachineCredential | undefined => {
    return credentials.find((c) => c.provider === providerId);
  };

  // Determine the active search provider: the one with the lowest autodetect_order that has a credential.
  // Sort candidates by autodetect_order (not sort_order) to match backend resolution.
  const activeProvider = [...providers]
    .filter((p) => p.autodetect_order != null && getCredentialForProvider(p.id) != null)
    .sort((a, b) => (a.autodetect_order ?? Infinity) - (b.autodetect_order ?? Infinity))[0] ?? null;

  const handleAddKey = async (providerId: string) => {
    if (!keyInput.trim()) return;
    setSavingKey(true);
    try {
      const providerInfo = providers.find((p) => p.id === providerId);
      await putMachineCredential(accountId, machine.id, providerId as any, {
        value: keyInput.trim(),
        credential_type: providerInfo?.credential_type || "api_key",
        label: `${providerInfo?.label || providerId} API key`,
      });
      await fetchCredentials();
      if (machine.status === "running") {
        const pushRes = await pushMachineConfig(accountId, machine.id);
        if (pushRes.live_update === "failed") {
          setAddingProvider(null);
          setKeyInput("");
          toast({
            title: `${providerInfo?.label || providerId} connected`,
            description: "Key saved but failed to push config to running VM.",
            variant: "error",
          });
          return;
        }
      }
      setAddingProvider(null);
      setKeyInput("");
      toast({ title: `${providerInfo?.label || providerId} connected`, variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to add key",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setSavingKey(false);
    }
  };

  const handleDisconnect = async (providerId: string, providerLabel: string) => {
    try {
      await deleteMachineCredential(accountId, machine.id, providerId);
      await fetchCredentials();
      if (machine.status === "running") {
        const pushRes = await pushMachineConfig(accountId, machine.id);
        if (pushRes.live_update === "failed") {
          toast({
            title: `${providerLabel} disconnected`,
            description: "Credential removed but failed to push config to running VM.",
            variant: "error",
          });
          return;
        }
      }
      toast({ title: `${providerLabel} disconnected`, variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to disconnect",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    }
  };

  const hasAnyCredential = providers.some((p) => getCredentialForProvider(p.id) != null);

  return (
    <div className="space-y-4">
      {/* Section 1: Active Search Provider */}
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card">
        <div className="p-4 md:p-6">
          <div className="text-lg md:text-xl font-semibold text-text-primary mb-1">
            Active Search Provider
          </div>
          <div className="text-xs md:text-sm text-text-tertiary mb-4 leading-relaxed">
            The search provider used by your machine for web searches.
          </div>

          {loading ? (
            <div className="h-12 bg-card-hover rounded-[var(--radius)] animate-pulse" />
          ) : (
            <div className="bg-elevated border border-border rounded-[var(--radius-sm)] p-3">
              {activeProvider ? (
                <div className="flex items-center gap-3">
                  <div
                    className={`w-8 h-8 rounded-full flex items-center justify-center text-white text-sm font-bold ${activeProvider.icon_bg || "bg-gray-600"}`}
                  >
                    {activeProvider.icon_letter || activeProvider.label.charAt(0)}
                  </div>
                  <div>
                    <div className="text-sm font-semibold text-text-primary">
                      Using: {activeProvider.label}
                    </div>
                    <div className="text-[11px] text-text-tertiary">
                      Active -- key configured
                    </div>
                  </div>
                </div>
              ) : (
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 rounded-full flex items-center justify-center text-white text-sm font-bold bg-gray-600">
                    D
                  </div>
                  <div>
                    <div className="text-sm font-semibold text-text-primary">
                      Using: DuckDuckGo (default, no key needed)
                    </div>
                    <div className="text-[11px] text-text-tertiary">
                      {hasAnyCredential
                        ? "No provider with autodetect priority has a key configured"
                        : "Add a search provider key below for enhanced search capabilities"}
                    </div>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Section 2: Search Providers */}
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <div className="text-lg md:text-xl font-semibold text-text-primary mb-1">Search Providers</div>
          <div className="text-xs md:text-sm text-text-tertiary mb-4 leading-relaxed">
            Add API keys for search providers to enable enhanced web search. The provider with the highest priority that has a key will be used automatically.
          </div>

          {loading ? (
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {[1, 2, 3].map((i) => (
                <div key={i} className="h-20 bg-card-hover rounded-[var(--radius-sm)] animate-pulse" />
              ))}
            </div>
          ) : providers.length === 0 ? (
            <div className="bg-elevated border border-dashed border-border rounded-[var(--radius-sm)] p-4 text-[13px] text-text-tertiary text-center">
              No search providers available.
            </div>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {providers.map((provider) => {
                const linkedCred = getCredentialForProvider(provider.id);
                const isConnected = linkedCred != null;
                const isAdding = addingProvider === provider.id;
                const isActive = activeProvider?.id === provider.id;

                return (
                  <div key={provider.id} className="flex flex-col">
                    <div
                      className={`flex items-center gap-3 bg-elevated border rounded-[var(--radius-sm)] p-3 md:p-[14px] transition-all hover:bg-card-hover ${
                        isConnected
                          ? "border-border hover:border-[var(--border-hover)]"
                          : "border-dashed border-[var(--border-hover)]"
                      }`}
                    >
                      <div
                        className={`flex-shrink-0 w-8 h-8 rounded-full flex items-center justify-center text-white text-sm font-bold ${provider.icon_bg || "bg-gray-600"}`}
                      >
                        {provider.icon_letter || provider.label.charAt(0)}
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="text-[12px] md:text-[13px] font-semibold text-text-primary mb-px">
                          {provider.label}
                        </div>
                        {isConnected && linkedCred ? (
                          <div className="text-[11px] text-text-tertiary font-mono">
                            ····{linkedCred.last_four}
                          </div>
                        ) : (
                          <div className="text-[11px] text-text-tertiary">
                            {provider.placeholder ? `Key: ${provider.placeholder}` : "API key required"}
                          </div>
                        )}
                      </div>
                      <div className="flex-shrink-0">
                        {isConnected ? (
                          <div className="flex items-center gap-2">
                            <span className={`text-[11px] font-medium ${isActive ? "text-green-500" : "text-blue-400"}`}>
                              {isActive ? "Active" : "Connected"}
                            </span>
                            <button
                              onClick={() => handleDisconnect(provider.id, provider.label)}
                              className="text-[10px] text-text-muted hover:text-red-400 transition-colors"
                            >
                              ×
                            </button>
                          </div>
                        ) : (
                          <button
                            onClick={() => {
                              setAddingProvider(isAdding ? null : provider.id);
                              setKeyInput("");
                            }}
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
                          placeholder={provider.placeholder || "Paste API key..."}
                          className="flex-1 px-3 py-1.5 text-[12px] bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                          onKeyDown={(e) => e.key === "Enter" && handleAddKey(provider.id)}
                          autoFocus
                        />
                        <button
                          onClick={() => handleAddKey(provider.id)}
                          disabled={!keyInput.trim() || savingKey}
                          className="text-[11px] font-medium px-2.5 py-1 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
                        >
                          {savingKey ? "Saving..." : "Save"}
                        </button>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
