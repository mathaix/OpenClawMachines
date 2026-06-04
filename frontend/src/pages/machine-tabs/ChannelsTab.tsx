import { useEffect, useState, useCallback } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import type { Machine, CredentialProvider, MachineCredential, ChannelCatalogEntry, MachineKind } from "../../lib/types";
import { CREDENTIAL_PROVIDERS } from "../../lib/types";
import {
  listMachineCapabilities, listMachineCredentials,
  putMachineCredential,
  testMachineCredential,
  connectChannel, disconnectChannel, updateChannelSettings,
  machineExec,
  listChannels,
} from "../../lib/api";
import type { MachineCapability } from "../../lib/api";
import { useToast } from "../../components/Toast";
import { isRestartRequiredLiveUpdate } from "../../lib/liveUpdate";

interface ChannelsTabProps {
  machine: Machine;
  accountId: number;
}

interface ChannelDef {
  id: string;
  label: string;
  shortDesc: string;
  credentialProvider?: CredentialProvider;
  hasSettings?: boolean;
  status?: "available" | "coming_soon";
  instructions?: {
    title: string;
    steps: string[];
    link: string;
    linkLabel: string;
    manifest?: string;
    note?: string;
    guideImage?: string;
  };
}

function slackManifest(kind: MachineKind): string {
  const appName = kind === "hermes" ? "hermes" : "openclaw";
  const description = kind === "hermes" ? "Hermes AI agent gateway" : "OpenClaw AI agent gateway";

  return JSON.stringify({
    display_information: { name: appName, description, background_color: "#2c2d30" },
    features: { bot_user: { display_name: appName, always_online: true } },
    oauth_config: { scopes: { bot: ["app_mentions:read","channels:history","channels:read","chat:write","files:read","files:write","groups:history","groups:read","im:history","im:read","im:write","reactions:read","reactions:write","team:read","users:read","users:read.email"] } },
    settings: { event_subscriptions: { bot_events: ["app_mention","message.channels","message.groups","message.im"] }, interactivity: { is_enabled: false }, org_deploy_enabled: false, socket_mode_enabled: true, token_rotation_enabled: false },
  }, null, 2);
}

const MESSAGING_CHANNELS: ChannelDef[] = [
  {
    id: "telegram",
    label: "Telegram",
    shortDesc: "Bot token · Telegram app",
    credentialProvider: "telegram",
    hasSettings: true,
    instructions: {
      title: "Connect Telegram",
      steps: [
        "Open Telegram and search for @BotFather",
        "Send /newbot and follow the prompts to name your bot",
        "BotFather will give you a bot token — copy it and paste it below",
      ],
      link: "https://t.me/BotFather",
      linkLabel: "Open BotFather",
    },
  },
  {
    id: "discord",
    label: "Discord",
    shortDesc: "Bot token · Channels & DMs",
    credentialProvider: "discord",
    hasSettings: true,
    instructions: {
      title: "Connect Discord",
      steps: [
        "Go to the Discord Developer Portal and create a New Application",
        "Go to the Bot tab, click Reset Token, and copy the token",
        "Under Privileged Gateway Intents, enable Message Content Intent",
      ],
      link: "https://discord.com/developers/applications",
      linkLabel: "Open Developer Portal",
    },
  },
  {
    id: "whatsapp",
    label: "WhatsApp",
    shortDesc: "WhatsApp Business",
  },
  {
    id: "slack",
    label: "Slack",
    shortDesc: "Bot token · Socket Mode",
    credentialProvider: "slack",
    hasSettings: true,
    instructions: {
      title: "Connect Slack",
      steps: [
        "Click \"Open Slack API\" below, then Create New App → From an app manifest",
        "Pick your workspace, then clear the default JSON and paste the manifest (use Copy below)",
        "Click Next, review the scopes, then click Create",
        "Go to Install App (left sidebar) → Install to Workspace → Allow",
        "Go to OAuth & Permissions (left sidebar) → copy the Bot User OAuth Token (starts with xoxb-)",
        "Go to Basic Information (left sidebar) → scroll to App-Level Tokens → Generate Token → name it anything, add connections:write scope → Generate",
        "Copy the token (starts with xapp-) and paste both tokens below",
      ],
      link: "https://api.slack.com/apps",
      linkLabel: "Open Slack API",
      note: "Each machine needs its own Slack app — sharing a bot token between machines causes race conditions and dropped messages.",
      guideImage: "/guides/slack-setup-guide.gif",
      manifest: slackManifest("openclaw"),
    },
  },
];

function withKindSpecificContent(channel: ChannelDef, kind: MachineKind): ChannelDef {
  if (channel.id !== "slack" || !channel.instructions) {
    return channel;
  }
  return {
    ...channel,
    instructions: {
      ...channel.instructions,
      manifest: slackManifest(kind),
    },
  };
}

function fallbackChannels(kind: MachineKind): ChannelDef[] {
  return MESSAGING_CHANNELS.map((ch) => withKindSpecificContent(ch, kind));
}

function channelsFromCatalog(catalog: ChannelCatalogEntry[], kind: MachineKind): ChannelDef[] {
  const fallbackByID = new Map(fallbackChannels(kind).map((ch) => [ch.id, ch]));
  return catalog.map((entry) => {
    const fallback = fallbackByID.get(entry.id);
    return withKindSpecificContent({
      id: entry.id,
      label: entry.label,
      shortDesc: entry.short_desc,
      credentialProvider: entry.credential_provider,
      hasSettings: entry.has_settings,
      status: entry.status,
      instructions: fallback?.instructions,
    }, kind);
  });
}

function readStringSetting(source: unknown, keys: string[]): string {
  if (!source || typeof source !== "object") return "";
  const record = source as Record<string, unknown>;
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string") return value;
    if (Array.isArray(value)) {
      return value.filter((item): item is string => typeof item === "string").join(", ");
    }
  }
  return "";
}

export function ChannelsTab({ machine, accountId }: ChannelsTabProps) {
  const { toast } = useToast();
  const machineKind = machine.kind ?? "openclaw";
  const isHermes = machineKind === "hermes";

  const [capabilities, setCapabilities] = useState<MachineCapability[]>([]);
  const [credentials, setCredentials] = useState<MachineCredential[]>([]);
  const [channels, setChannels] = useState<ChannelDef[]>(() => fallbackChannels(machineKind));
  const [loading, setLoading] = useState(true);

  // Setup dialog state
  const [setupChannel, setSetupChannel] = useState<ChannelDef | null>(null);
  const [tokenInput, setTokenInput] = useState("");
  const [appTokenInput, setAppTokenInput] = useState("");
  const [telegramAllowedUsersInput, setTelegramAllowedUsersInput] = useState("");
  const [validating, setValidating] = useState(false);
  const [saving, setSaving] = useState(false);
  const [validationResult, setValidationResult] = useState<{ ok: boolean; error?: string } | null>(null);

  // Manage panel state
  const [managingChannel, setManagingChannel] = useState<string | null>(null);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; error?: string } | null>(null);
  const [disconnectingChannels, setDisconnectingChannels] = useState<Set<string>>(new Set());
  const [disconnectedChannels, setDisconnectedChannels] = useState<Set<string>>(new Set());

  // Settings state
  const [dmPolicy, setDmPolicy] = useState<"pairing" | "open">("pairing");
  const [requireMention, setRequireMention] = useState(true);
  const [telegramAllowedUsersSetting, setTelegramAllowedUsersSetting] = useState("");
  const [savingSettings, setSavingSettings] = useState(false);

  // Group editor state
  const [editingGroups, setEditingGroups] = useState<{ id: string; requireMention: boolean }[]>([]);

  // Pairing state
  const [pairingChannel, setPairingChannel] = useState("");
  const [pairingCode, setPairingCode] = useState("");
  const [pairingSubmitting, setPairingSubmitting] = useState(false);
  const [pairingResult, setPairingResult] = useState<{ type: "success" | "error"; message: string } | null>(null);

  const fetchStatus = useCallback(async () => {
    try {
      const [caps, creds] = await Promise.all([
        listMachineCapabilities(accountId, machine.id),
        listMachineCredentials(accountId, machine.id),
      ]);
      setCapabilities(caps || []);
      setCredentials(creds || []);
      // Only clear tombstones for channels the server confirms are disconnected
      setDisconnectedChannels((prev) => {
        const enabledIds = new Set((caps || []).filter((c: MachineCapability) => c.enabled).map((c: MachineCapability) => c.entry_id));
        const next = new Set<string>();
        for (const id of prev) {
          if (enabledIds.has(id)) next.add(id); // still in server, keep suppressing
        }
        return next;
      });
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, [accountId, machine.id]);

  useEffect(() => {
    fetchStatus();
  }, [fetchStatus]);

  useEffect(() => {
    let cancelled = false;
    listChannels(machineKind)
      .then((catalog) => {
        if (!cancelled) setChannels(channelsFromCatalog(catalog, machineKind));
      })
      .catch(() => {
        if (!cancelled) setChannels(fallbackChannels(machineKind));
      });
    return () => {
      cancelled = true;
    };
  }, [machineKind]);

  const getCapability = (channelId: string) =>
    disconnectedChannels.has(channelId) ? undefined : capabilities.find((c) => c.entry_id === channelId && c.enabled);

  const getCredential = (providerId: string) =>
    credentials.find((c) => c.provider === providerId);

  const channelLiveUpdateToast = (
    label: string,
    action: "connected" | "disconnected" | "settings",
    liveUpdate: string,
    error?: string,
  ) => {
    if (liveUpdate === "failed") {
      if (isRestartRequiredLiveUpdate(error)) {
        toast({
          title:
            action === "settings"
              ? "Settings saved, restart Hermes to apply"
              : `${label} ${action === "connected" ? "saved" : "removed"}, restart Hermes to apply`,
          description: error,
          variant: "success",
        });
        return;
      }
      const title =
        action === "settings"
          ? "Settings saved but sync failed"
          : `${label} ${action === "connected" ? "saved" : "removed"} but sync failed`;
      toast({
        title: isHermes ? title.replace("sync", "Hermes sync") : title.replace("sync", "VM update"),
        description: error,
        variant: "error",
      });
      return;
    }
    if (liveUpdate === "not_running") {
      toast({
        title:
          action === "settings"
            ? "Settings saved"
            : `${label} ${action} (will apply on next start)`,
        variant: "success",
      });
      return;
    }
    if (liveUpdate === "skipped") {
      toast({
        title:
          action === "settings"
            ? "Settings saved"
            : `${label} ${action}`,
        description: "Live VM sync was skipped.",
        variant: "success",
      });
      return;
    }
    toast({
      title:
        action === "settings"
          ? "Settings saved"
          : `${label} ${action}`,
      description: isHermes ? "Hermes messaging config synced." : undefined,
      variant: "success",
    });
  };

  // Load settings when manage panel opens
  const openManagePanel = (channelId: string) => {
    setManagingChannel(channelId);
    setTestResult(null);

    // Load overrides from capability
    const cap = getCapability(channelId);
    if (cap?.config_overrides) {
      try {
        const overrides = typeof cap.config_overrides === "string"
          ? JSON.parse(cap.config_overrides)
          : cap.config_overrides;
        const channelConfig = overrides?.channels?.[channelId];
        setTelegramAllowedUsersSetting(
          readStringSetting(channelConfig, ["allowedUsers", "allowed_users", "allowedUserIds", "allowed_user_ids"]),
        );
        if (channelConfig?.dmPolicy) setDmPolicy(channelConfig.dmPolicy);
        if (channelConfig?.groups?.["*"]?.requireMention !== undefined) {
          setRequireMention(channelConfig.groups["*"].requireMention);
        }
        // Load per-group config
        const groupsObj = channelConfig?.groups as Record<string, { requireMention?: boolean }> | undefined;
        if (groupsObj) {
          const groups = Object.entries(groupsObj)
            .filter(([key]) => key !== "*")
            .map(([id, cfg]) => ({ id, requireMention: cfg.requireMention !== false }));
          setEditingGroups(groups);
        } else {
          setEditingGroups([]);
        }
      } catch {
        // Use defaults
        setTelegramAllowedUsersSetting("");
      }
    } else {
      setDmPolicy("pairing");
      setRequireMention(true);
      setEditingGroups([]);
      setTelegramAllowedUsersSetting("");
    }
  };

  // --- Setup flow ---
  const handleValidateToken = async () => {
    if (!setupChannel?.credentialProvider || !tokenInput.trim()) return;
    setValidating(true);
    setValidationResult(null);
    try {
      const providerInfo = CREDENTIAL_PROVIDERS.find((p) => p.id === setupChannel.credentialProvider);
      // Save credential first (needed for test endpoint)
      await putMachineCredential(accountId, machine.id, setupChannel.credentialProvider, {
        value: tokenInput.trim(),
        credential_type: providerInfo?.defaultCredentialType || "api_key",
        label: `${setupChannel.label} Bot Token`,
      });
      // Test it
      const result = await testMachineCredential(accountId, machine.id, setupChannel.credentialProvider);
      setValidationResult(result);
    } catch (err) {
      setValidationResult({ ok: false, error: err instanceof Error ? err.message : "Validation failed" });
    } finally {
      setValidating(false);
    }
  };

  const handleConnect = async () => {
    if (!setupChannel) return;
    const settings =
      isHermes && setupChannel.id === "telegram"
        ? { allowedUsers: telegramAllowedUsersInput.trim() }
        : undefined;
    setSaving(true);
    try {
      const res = await connectChannel(accountId, machine.id, setupChannel.id, {
        token: tokenInput.trim(),
        ...(appTokenInput.trim() ? { app_token: appTokenInput.trim() } : {}),
        ...(settings ? { settings } : {}),
      });
      await fetchStatus();
      setSetupChannel(null);
      setTokenInput("");
      setAppTokenInput("");
      setTelegramAllowedUsersInput("");
      setValidationResult(null);
      channelLiveUpdateToast(setupChannel.label, "connected", res.live_update, res.live_update_error);
    } catch (err) {
      toast({
        title: "Setup failed",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setSaving(false);
    }
  };

  // --- Manage flow ---
  const handleTestConnection = async (channelId: string) => {
    const channel = channels.find((c) => c.id === channelId);
    if (!channel?.credentialProvider) return;
    setTesting(true);
    setTestResult(null);
    try {
      const result = await testMachineCredential(accountId, machine.id, channel.credentialProvider);
      setTestResult(result);
    } catch (err) {
      setTestResult({ ok: false, error: err instanceof Error ? err.message : "Test failed" });
    } finally {
      setTesting(false);
    }
  };

  const handleSaveSettings = async (channelId: string) => {
    setSavingSettings(true);
    try {
      let settings: Record<string, unknown>;
      if (isHermes && channelId === "telegram") {
        if (!telegramAllowedUsersSetting.trim()) {
          toast({
            title: "Allowed users required",
            description: "Add at least one numeric Telegram user ID.",
            variant: "error",
          });
          return;
        }
        settings = { allowedUsers: telegramAllowedUsersSetting.trim() };
      } else {
        const groupsObj: Record<string, { requireMention: boolean }> = {
          "*": { requireMention },
        };
        for (const g of editingGroups) {
          if (g.id.trim()) {
            groupsObj[g.id.trim()] = { requireMention: g.requireMention };
          }
        }
        settings = {
          dmPolicy,
          groups: groupsObj,
        };
      }
      const res = await updateChannelSettings(accountId, machine.id, channelId, settings);
      await fetchStatus();
      channelLiveUpdateToast(channelId, "settings", res.live_update, res.live_update_error);
    } catch (err) {
      toast({
        title: "Failed to save settings",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setSavingSettings(false);
    }
  };

  // --- Pairing ---
  const handlePairingSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!pairingChannel || !pairingCode) return;
    setPairingSubmitting(true);
    setPairingResult(null);
    try {
      const result = await machineExec(accountId, machine.id, [
        "openclaw", "pairing", "approve", pairingChannel, pairingCode.toUpperCase(),
      ]);
      if (result.exit_code !== 0) {
        throw new Error(result.stderr || result.stdout || "Pairing failed");
      }
      setPairingResult({ type: "success", message: result.stdout || "Pairing approved successfully" });
      setPairingCode("");
    } catch (err) {
      setPairingResult({ type: "error", message: err instanceof Error ? err.message : "Pairing failed" });
    } finally {
      setPairingSubmitting(false);
    }
  };

  const enabledChannels = channels.filter((ch) => getCapability(ch.id));

  const handleDisconnect = async (channel: ChannelDef) => {
    setDisconnectingChannels((prev) => new Set(prev).add(channel.id));
    try {
      const res = await disconnectChannel(accountId, machine.id, channel.id);
      // Optimistic: add to tombstone so UI shows disconnected immediately
      setDisconnectedChannels((prev) => new Set(prev).add(channel.id));
      setManagingChannel(null);
      // Background fetch for server truth
      fetchStatus();
      channelLiveUpdateToast(channel.label, "disconnected", res.live_update, res.live_update_error);
    } catch (err) {
      toast({
        title: "Failed to disconnect",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setDisconnectingChannels((prev) => {
        const next = new Set(prev);
        next.delete(channel.id);
        return next;
      });
    }
  };

  return (
    <div className="space-y-4">
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card">
        <div className="p-4 md:p-6">
          <div className="text-lg md:text-xl font-semibold text-text-primary mb-1">
            {isHermes ? "Hermes Messaging" : "Messaging Channels"}
          </div>
          <div className="text-xs md:text-sm text-text-tertiary mb-4 leading-relaxed">
            {isHermes
              ? "Connect bot credentials. Running Hermes machines sync these tokens into .env and restart the gateway."
              : "Connect messaging platforms so users can chat with your machine from their favorite apps."}
          </div>

          {loading ? (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              {[1, 2, 3, 4].map((i) => (
                <div key={i} className="h-[60px] bg-elevated border border-border rounded-[var(--radius-sm)] animate-pulse" />
              ))}
            </div>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              {channels.map((channel) => {
                const cap = getCapability(channel.id);
                const cred = channel.credentialProvider ? getCredential(channel.credentialProvider) : null;
                const connected = !!cap;
                const hasSetup = channel.status !== "coming_soon" && !!channel.instructions;
                const isManaging = managingChannel === channel.id;
                const supportsChannelSettings = (!isHermes && channel.hasSettings) || (isHermes && channel.id === "telegram");

                return (
                  <div key={channel.id} className="flex flex-col">
                    {/* Channel card */}
                    <div
                      className={`flex items-center gap-3 bg-elevated border rounded-[var(--radius-sm)] p-3 md:p-[14px] transition-all hover:bg-card-hover ${
                        connected
                          ? "border-border hover:border-[var(--border-hover)]"
                          : "border-dashed border-[var(--border-hover)]"
                      } ${isManaging ? "rounded-b-none border-b-0" : ""}`}
                    >
                      {/* Icon */}
                      <div className="flex-shrink-0 w-[36px] h-[36px] md:w-10 md:h-10 rounded-lg flex items-center justify-center bg-white p-1.5">
                        <img
                          src={`/channels/${channel.id}.svg`}
                          alt={channel.label}
                          className="w-full h-full object-contain"
                        />
                      </div>

                      {/* Info */}
                      <div className="flex-1 min-w-0">
                        <div className="text-[12px] md:text-[13px] font-semibold text-text-primary mb-px">
                          {channel.label}
                        </div>
                        {connected && cred ? (
                          <div className="text-[11px] text-text-tertiary font-mono">
                            ····{cred.last_four}
                            {cred.status === "reauth_required" && (
                              <span className="ml-1.5 text-red-400 font-sans">Re-auth</span>
                            )}
                          </div>
                        ) : (
                          <div className="text-[11px] text-text-tertiary">{channel.shortDesc}</div>
                        )}
                      </div>

                      {/* Action */}
                      <div className="flex-shrink-0">
                        {connected ? (
                          <div className="flex items-center gap-2">
                            <span className="text-[11px] font-medium text-green-500">Connected</span>
                            <button
                              onClick={() => isManaging ? setManagingChannel(null) : openManagePanel(channel.id)}
                              className="text-[10px] text-text-muted hover:text-text-secondary transition-colors"
                            >
                              {isManaging ? "Close" : "Manage"}
                            </button>
                            <button
                              onClick={() => handleDisconnect(channel)}
                              disabled={disconnectingChannels.has(channel.id)}
                              className="text-[10px] text-text-muted hover:text-red-400 disabled:opacity-50 transition-colors"
                            >
                              ×
                            </button>
                          </div>
                        ) : hasSetup ? (
                          <button
                            onClick={() => { setSetupChannel(channel); setTokenInput(""); setAppTokenInput(""); setTelegramAllowedUsersInput(""); setValidationResult(null); }}
                            className="text-xs md:text-sm font-medium px-2.5 py-1 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary transition-colors"
                          >
                            Connect
                          </button>
                        ) : (
                          <span className="text-[10px] px-2 py-0.5 rounded-full border border-border text-text-muted">
                            Soon
                          </span>
                        )}
                      </div>
                    </div>

                    {/* Inline manage panel */}
                    {isManaging && connected && (
                      <div className="bg-elevated border border-border border-t-0 rounded-b-[var(--radius-sm)] px-3 md:px-4 py-3 space-y-3">
                        {/* Settings */}
                        {supportsChannelSettings && (
                          <>
                            <div className="text-xs font-medium text-text-secondary uppercase tracking-wider mb-2">Settings</div>
                            {isHermes && channel.id === "telegram" ? (
                              <div className="space-y-2">
                                <label className="text-xs font-medium text-text-secondary">
                                  Allowed Telegram user IDs
                                </label>
                                <input
                                  type="text"
                                  value={telegramAllowedUsersSetting}
                                  onChange={(e) => setTelegramAllowedUsersSetting(e.target.value)}
                                  placeholder="123456789, 987654321"
                                  className="w-full border border-border bg-input text-text-primary rounded-[var(--radius-sm)] px-2.5 py-1.5 text-xs focus:border-brand-500 outline-none font-mono"
                                />
                                <p className="text-xs text-text-tertiary">
                                  Hermes ignores Telegram messages from IDs not listed here.
                                </p>
                              </div>
                            ) : (
                              <>
                                <div className="space-y-3">
                                  {/* DM Policy */}
                                  <div className="flex items-center justify-between">
                                    <div>
                                      <div className="text-sm text-text-primary font-medium">DM Policy</div>
                                      <div className="text-xs text-text-tertiary">How the bot handles direct messages</div>
                                    </div>
                                    <select
                                      value={dmPolicy}
                                      onChange={(e) => setDmPolicy(e.target.value as "pairing" | "open")}
                                      className="text-xs bg-input border border-border rounded-[var(--radius-sm)] px-2.5 py-1.5 text-text-primary outline-none focus:border-brand-500"
                                    >
                                      <option value="pairing">Pairing (require approval)</option>
                                      <option value="open">Open (anyone can DM)</option>
                                    </select>
                                  </div>

                                  {/* Require Mention */}
                                  <div className="flex items-center justify-between">
                                    <div>
                                      <div className="text-sm text-text-primary font-medium">Require @mention in groups</div>
                                      <div className="text-xs text-text-tertiary">Bot only responds when mentioned by name</div>
                                    </div>
                                    <button
                                      onClick={() => setRequireMention(!requireMention)}
                                      className={`relative w-10 h-[22px] rounded-full transition-colors ${
                                        requireMention ? "bg-brand-600" : "bg-border"
                                      }`}
                                    >
                                      <span
                                        className={`absolute top-[3px] left-[3px] w-4 h-4 rounded-full bg-white transition-transform ${
                                          requireMention ? "translate-x-[18px]" : ""
                                        }`}
                                      />
                                    </button>
                                  </div>
                                </div>

                                {/* Group Configuration (Telegram) */}
                                {channel.id === "telegram" && (
                                  <div className="space-y-2 pt-1">
                                    <div className="text-xs font-medium text-text-secondary uppercase tracking-wider">Groups</div>
                                    <p className="text-xs text-text-tertiary">
                                      Add Telegram groups your bot should respond in. Use the group chat ID (starts with -100).
                                    </p>
                                    {editingGroups.map((group, i) => (
                                      <div key={i} className="flex items-center gap-2">
                                    <input
                                      type="text"
                                      value={group.id}
                                      onChange={(e) => {
                                        const updated = [...editingGroups];
                                        updated[i] = { ...updated[i], id: e.target.value };
                                        setEditingGroups(updated);
                                      }}
                                      placeholder="Group ID (e.g. -1001234567890)"
                                      className="flex-1 border border-border bg-input text-text-primary rounded-[var(--radius-sm)] px-2.5 py-1.5 text-xs focus:border-brand-500 outline-none font-mono"
                                    />
                                    <label className="flex items-center gap-1 text-xs text-text-tertiary whitespace-nowrap cursor-pointer">
                                      <input
                                        type="checkbox"
                                        checked={group.requireMention}
                                        onChange={(e) => {
                                          const updated = [...editingGroups];
                                          updated[i] = { ...updated[i], requireMention: e.target.checked };
                                          setEditingGroups(updated);
                                        }}
                                        className="accent-brand-600"
                                      />
                                      @mention
                                    </label>
                                    <button
                                      onClick={() => setEditingGroups(editingGroups.filter((_, idx) => idx !== i))}
                                      className="text-text-muted hover:text-red-400 p-0.5"
                                      aria-label="Remove group"
                                    >
                                      <svg className="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                                      </svg>
                                    </button>
                                  </div>
                                    ))}
                                    <button
                                      onClick={() => setEditingGroups([...editingGroups, { id: "", requireMention: true }])}
                                      className="text-xs text-brand-500 hover:text-brand-400"
                                    >
                                      + Add group
                                    </button>
                                  </div>
                                )}
                              </>
                            )}

                            {/* Save settings */}
                            <button
                              onClick={() => handleSaveSettings(channel.id)}
                              disabled={savingSettings}
                              className="text-xs font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
                            >
                              {savingSettings ? "Saving..." : "Save settings"}
                            </button>

                            <div className="border-t border-border-subtle" />
                          </>
                        )}

                        {/* Test connection */}
                        <div className="flex items-center gap-3">
                          <button
                            onClick={() => handleTestConnection(channel.id)}
                            disabled={testing}
                            className="text-xs font-medium px-3 py-1.5 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary disabled:opacity-50 transition-colors"
                          >
                            {testing ? "Testing..." : "Test connection"}
                          </button>
                          {testResult && (
                            <span className={`text-xs font-medium ${testResult.ok ? "text-green-500" : "text-red-400"}`}>
                              {testResult.ok ? "Connection OK" : testResult.error || "Connection failed"}
                            </span>
                          )}
                        </div>

                        {/* Disconnect */}
                        <div className="border-t border-border-subtle pt-3">
                          <button
                            onClick={() => handleDisconnect(channel)}
                            disabled={disconnectingChannels.has(channel.id)}
                            className="text-[11px] font-medium px-2.5 py-1 rounded-[var(--radius-sm)] text-red-400 border border-[rgba(248,113,113,0.15)] hover:bg-[rgba(248,113,113,0.06)] disabled:opacity-50 transition-colors"
                          >
                            {disconnectingChannels.has(channel.id) ? "Disconnecting..." : `Disconnect ${channel.label}`}
                          </button>
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </div>

      {/* Pairing Approval */}
      {machineKind === "openclaw" && machine.status === "running" && enabledChannels.length > 0 && (
        <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card">
          <div className="p-4 md:p-6">
            <div className="text-base md:text-lg font-semibold text-text-primary mb-1">Approve Pairing</div>
            <div className="text-xs text-text-tertiary mb-4">
              When DM policy is set to "Pairing", users must send a pairing code to the bot. Enter the code here to approve access.
            </div>
            <form onSubmit={handlePairingSubmit} className="space-y-3">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs font-medium text-text-secondary mb-1">Channel</label>
                  <select
                    value={pairingChannel}
                    onChange={(e) => { setPairingChannel(e.target.value); setPairingResult(null); }}
                    className="w-full border border-border bg-input text-text-primary rounded-[var(--radius-sm)] px-3 py-2 text-sm outline-none focus:border-brand-500"
                  >
                    <option value="">Select channel</option>
                    {enabledChannels.map((ch) => (
                      <option key={ch.id} value={ch.id}>{ch.label}</option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs font-medium text-text-secondary mb-1">Pairing Code</label>
                  <input
                    type="text"
                    value={pairingCode}
                    onChange={(e) => { setPairingCode(e.target.value.toUpperCase()); setPairingResult(null); }}
                    placeholder="ABCD1234"
                    maxLength={8}
                    className="w-full border border-border bg-input text-text-primary rounded-[var(--radius-sm)] px-3 py-2 text-sm font-mono tracking-wider uppercase outline-none focus:border-brand-500 placeholder:text-text-muted"
                  />
                </div>
              </div>
              <div className="flex items-center gap-3">
                <button
                  type="submit"
                  disabled={!pairingChannel || pairingCode.length < 4 || pairingSubmitting}
                  className="text-xs font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
                >
                  {pairingSubmitting ? "Approving..." : "Approve"}
                </button>
                {pairingResult && (
                  <span className={`text-xs font-medium ${
                    pairingResult.type === "success" ? "text-green-500" : "text-red-400"
                  }`}>
                    {pairingResult.message}
                  </span>
                )}
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Setup Dialog */}
      <Dialog.Root open={!!setupChannel} onOpenChange={(open) => { if (!open) { setSetupChannel(null); setTokenInput(""); setAppTokenInput(""); setTelegramAllowedUsersInput(""); setValidationResult(null); } }}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/60 z-50" />
          <Dialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 z-50 w-full max-w-md mx-4 bg-card border border-border rounded-[var(--radius-lg)] p-5 md:p-6 shadow-modal animate-modal-in">
            {setupChannel && (
              <>
                <Dialog.Title className="text-base md:text-lg font-semibold text-text-primary mb-1">
                  {setupChannel.instructions?.title}
                </Dialog.Title>
                <Dialog.Description className="text-xs md:text-sm text-text-tertiary mb-4">
                  Follow these steps to connect {setupChannel.label} to your machine.
                </Dialog.Description>

                {/* Steps */}
                <ol className="space-y-2.5 mb-5">
                  {setupChannel.instructions?.steps.map((step, i) => (
                    <li key={i} className="flex gap-2.5 text-sm text-text-secondary">
                      <span className="flex-shrink-0 w-5 h-5 rounded-full bg-brand-600/15 text-brand-500 text-xs font-bold flex items-center justify-center mt-0.5">
                        {i + 1}
                      </span>
                      <span>{step}</span>
                    </li>
                  ))}
                </ol>

                {/* Visual guide */}
                {setupChannel.instructions?.guideImage && (
                  <details className="mb-4 group">
                    <summary className="text-xs font-medium text-brand-500 hover:text-brand-400 cursor-pointer select-none">
                      Show visual guide
                    </summary>
                    <img
                      src={setupChannel.instructions.guideImage}
                      alt={`${setupChannel.label} setup walkthrough`}
                      className="mt-2 rounded-[var(--radius-sm)] border border-border w-full"
                    />
                  </details>
                )}

                {/* Manifest (copyable) */}
                {setupChannel.instructions?.manifest && (
                  <div className="mb-4">
                    <div className="flex items-center justify-between mb-1.5">
                      <span className="text-xs font-medium text-text-secondary">App Manifest</span>
                      <button
                        type="button"
                        onClick={() => {
                          navigator.clipboard.writeText(setupChannel.instructions!.manifest!);
                          toast({ title: "Manifest copied to clipboard" });
                        }}
                        className="text-xs font-medium text-brand-500 hover:text-brand-400"
                      >
                        Copy
                      </button>
                    </div>
                    <pre className="p-3 rounded-[var(--radius-sm)] bg-surface-secondary border border-border text-xs font-mono text-text-secondary overflow-x-auto max-h-32 overflow-y-auto whitespace-pre">
                      {setupChannel.instructions.manifest}
                    </pre>
                  </div>
                )}

                {/* Note / warning */}
                {setupChannel.instructions?.note && (
                  <p className="text-xs text-amber-500 mb-4">
                    {setupChannel.instructions.note}
                  </p>
                )}

                {/* Link to external service */}
                {setupChannel.instructions?.link && (
                  <a
                    href={setupChannel.instructions.link}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1.5 text-sm font-medium text-brand-500 hover:text-brand-400 mb-4"
                  >
                    {setupChannel.instructions.linkLabel} &rarr;
                  </a>
                )}

                {/* Token input */}
                <div className="space-y-2">
                  <label className="text-xs font-medium text-text-secondary">
                    Bot Token
                  </label>
                  <div className="flex gap-2">
                    <input
                      type="password"
                      value={tokenInput}
                      onChange={(e) => { setTokenInput(e.target.value); setValidationResult(null); }}
                      placeholder="Paste your bot token here..."
                      className="flex-1 px-3 py-2 text-sm bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                      onKeyDown={(e) => e.key === "Enter" && !validationResult?.ok && handleValidateToken()}
                      autoFocus
                    />
                    <button
                      onClick={handleValidateToken}
                      disabled={!tokenInput.trim() || validating}
                      className="text-xs font-medium px-3 py-2 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary disabled:opacity-50 transition-colors"
                    >
                      {validating ? "Checking..." : "Validate"}
                    </button>
                  </div>

                  {/* Validation result */}
                  {validationResult && (
                    <div className={`text-xs font-medium px-3 py-2 rounded-[var(--radius-sm)] ${
                      validationResult.ok
                        ? "bg-green-500/10 text-green-500 border border-green-500/20"
                        : "bg-red-500/10 text-red-400 border border-red-500/20"
                    }`}>
                      {validationResult.ok ? "Token is valid — ready to connect." : validationResult.error || "Invalid token."}
                    </div>
                  )}
                  {isHermes && (
                    <p className="text-[11px] text-text-tertiary">
                      This token is written to Hermes .env and applied through the Hermes gateway sync.
                    </p>
                  )}
                </div>

                {/* Hermes Telegram allow-list */}
                {isHermes && setupChannel.id === "telegram" && (
                  <div className="space-y-2 mt-3">
                    <label className="text-xs font-medium text-text-secondary">
                      Allowed Telegram user IDs
                    </label>
                    <input
                      type="text"
                      value={telegramAllowedUsersInput}
                      onChange={(e) => setTelegramAllowedUsersInput(e.target.value)}
                      placeholder="123456789, 987654321"
                      className="w-full px-3 py-2 text-sm bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                    />
                    <p className="text-[11px] text-text-tertiary">
                      Hermes only accepts Telegram messages from these numeric IDs.
                    </p>
                  </div>
                )}

                {/* App Token input (Slack only) */}
                {setupChannel.id === "slack" && (
                  <div className="space-y-2 mt-3">
                    <label className="text-xs font-medium text-text-secondary">
                      App Token
                    </label>
                    <input
                      type="password"
                      value={appTokenInput}
                      onChange={(e) => setAppTokenInput(e.target.value)}
                      placeholder="Paste your app-level token (xapp-...)..."
                      className="w-full px-3 py-2 text-sm bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                    />
                  </div>
                )}

                {/* Actions */}
                <div className="flex justify-end gap-2 mt-5">
                  <Dialog.Close asChild>
                    <button className="text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary transition-colors">
                      Cancel
                    </button>
                  </Dialog.Close>
                  <button
                    onClick={handleConnect}
                    disabled={!validationResult?.ok || saving || (setupChannel?.id === "slack" && !appTokenInput.trim()) || (isHermes && setupChannel?.id === "telegram" && !telegramAllowedUsersInput.trim())}
                    className="text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
                  >
                    {saving ? "Connecting..." : "Connect"}
                  </button>
                </div>
              </>
            )}
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </div>
  );
}
