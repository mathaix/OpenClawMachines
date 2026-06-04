import { useEffect, useState, useCallback, useRef } from "react";
import type { Machine } from "../../lib/types";
import { listIntegrations, createConnectLink, deleteIntegration } from "../../lib/api";
import type { Integration } from "../../lib/api";
import { useToast } from "../../components/Toast";

interface IntegrationsTabProps {
  machine: Machine;
}

const CATEGORY_LABELS: Record<string, string> = {
  google: "Google",
  microsoft: "Microsoft",
  productivity: "Productivity",
  dev: "Developer Tools",
  social: "Social",
  sales: "Sales & CRM",
  other: "Other",
};

const CATEGORY_ORDER = ["google", "microsoft", "productivity", "dev", "social", "sales", "other"];

// Map icon field (from DB) to the SVG filename in /integrations/
const ICON_FILE: Record<string, string> = {
  "gmail": "gmail",
  "google-calendar": "googlecalendar",
  "google-drive": "googledrive",
  "google-sheets": "googlesheets",
  "google-docs": "googledocs",
  "outlook": "outlook",
  "msteams": "msteams",
  "youtube": "youtube",
  "notion": "notion",
  "slack": "slack",
  "trello": "trello",
  "github": "github",
  "jira": "jira",
  "linkedin": "linkedin",
  "x": "twitter",
  "tiktok": "tiktok",
  "instagram": "instagram",
  "hubspot": "hubspot",
  "salesforce": "salesforce",
  "apollo": "apollo",
};

export default function IntegrationsTab({ machine }: IntegrationsTabProps) {
  const [integrations, setIntegrations] = useState<Integration[]>([]);
  const [loading, setLoading] = useState(true);
  const [connecting, setConnecting] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState<string | null>(null);
  const popupRef = useRef<Window | null>(null);
  const { toast } = useToast();

  const fetchIntegrations = useCallback(async () => {
    try {
      const data = await listIntegrations(machine.account_id, machine.id);
      setIntegrations(data);
    } catch (err) {
      console.error("Failed to fetch integrations", err);
    } finally {
      setLoading(false);
    }
  }, [machine.account_id, machine.id]);

  useEffect(() => {
    fetchIntegrations();
  }, [fetchIntegrations]);

  useEffect(() => {
    const handleFocus = () => fetchIntegrations();
    window.addEventListener("focus", handleFocus);
    return () => window.removeEventListener("focus", handleFocus);
  }, [fetchIntegrations]);

  useEffect(() => {
    const handleMessage = (event: MessageEvent) => {
      if (event.data?.type === "composio-connected") {
        fetchIntegrations();
        setConnecting(null);
      }
    };
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [fetchIntegrations]);

  useEffect(() => {
    if (!connecting || !popupRef.current) return;
    const interval = setInterval(() => {
      if (popupRef.current?.closed) {
        popupRef.current = null;
        setConnecting(null);
        fetchIntegrations();
        clearInterval(interval);
      }
    }, 500);
    return () => clearInterval(interval);
  }, [connecting, fetchIntegrations]);

  const handleConnect = async (integrationId: string) => {
    setConnecting(integrationId);
    try {
      const { url } = await createConnectLink(machine.account_id, machine.id, integrationId);
      popupRef.current = window.open(url, "composio-connect", "width=500,height=700,popup=1");
    } catch (err) {
      setConnecting(null);
      toast({ title: "Failed to start connection", variant: "error" });
    }
  };

  const handleDisconnect = async (integration: Integration) => {
    if (!integration.connected_account_id) return;
    if (!confirm(`Disconnect ${integration.name}?`)) return;

    setDisconnecting(integration.id);
    try {
      await deleteIntegration(machine.account_id, machine.id, integration.connected_account_id);
      await fetchIntegrations();
      toast({ title: `${integration.name} disconnected` });
    } catch (err) {
      toast({ title: "Failed to disconnect", variant: "error" });
    } finally {
      setDisconnecting(null);
    }
  };

  if (loading) {
    return (
      <div className="space-y-4 p-6">
        {[1, 2, 3].map((i) => (
          <div key={i} className="h-14 bg-card border border-border rounded-xl animate-shimmer" />
        ))}
      </div>
    );
  }

  if (integrations.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-text-muted">
        <p className="text-sm">No integrations available yet.</p>
        <p className="text-xs mt-1">Integrations will appear here once configured by the admin.</p>
      </div>
    );
  }

  const grouped = new Map<string, Integration[]>();
  for (const integration of integrations) {
    const cat = integration.category || "other";
    if (!grouped.has(cat)) grouped.set(cat, []);
    grouped.get(cat)!.push(integration);
  }

  return (
    <div className="space-y-8 p-6">
      {CATEGORY_ORDER.filter((cat) => grouped.has(cat)).map((cat) => (
        <div key={cat}>
          <h3 className="text-[11px] font-semibold text-text-muted uppercase tracking-widest mb-3 pl-1">
            {CATEGORY_LABELS[cat] || cat}
          </h3>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {grouped.get(cat)!.map((integration) => {
              const iconFile = ICON_FILE[integration.icon] || integration.icon;
              return (
                <div
                  key={integration.id}
                  className={`group flex items-center gap-3 p-3.5 rounded-xl border transition-all duration-150 ${
                    integration.connected
                      ? "bg-green-500/5 border-green-500/20"
                      : "bg-card border-border hover:border-text-muted/30"
                  }`}
                >
                  <div className="w-9 h-9 rounded-lg bg-white flex items-center justify-center shrink-0 p-1.5">
                    <img
                      src={`/integrations/${iconFile}.svg`}
                      alt={integration.name}
                      className="w-full h-full object-contain"
                    />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium text-text truncate">{integration.name}</div>
                    {integration.connected ? (
                      <div className="flex items-center gap-1 mt-0.5">
                        <div className="w-1.5 h-1.5 rounded-full bg-green-500" />
                        <span className="text-[11px] text-green-500 font-medium">Connected</span>
                      </div>
                    ) : (
                      <div className="text-[11px] text-text-muted mt-0.5">Not connected</div>
                    )}
                  </div>
                  <div className="shrink-0">
                    {integration.connected ? (
                      <button
                        onClick={() => handleDisconnect(integration)}
                        disabled={disconnecting === integration.id}
                        className="text-[11px] px-3 py-1.5 rounded-lg border border-transparent text-text-muted opacity-0 group-hover:opacity-100 hover:text-red-400 hover:border-red-400/30 hover:bg-red-400/5 transition-all disabled:opacity-50"
                      >
                        {disconnecting === integration.id ? "..." : "Disconnect"}
                      </button>
                    ) : (
                      <button
                        onClick={() => handleConnect(integration.id)}
                        disabled={connecting === integration.id}
                        className="text-[11px] px-3.5 py-1.5 rounded-lg bg-text/5 border border-border text-text font-medium hover:bg-text/10 hover:border-text-muted/40 transition-all disabled:opacity-50"
                      >
                        {connecting === integration.id ? "Connecting..." : "Connect"}
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}
