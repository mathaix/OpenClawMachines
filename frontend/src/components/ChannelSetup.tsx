import { useEffect, useState, useCallback, useReducer } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import {
  listMachines,
  listMachineCapabilities,
  listMachineCredentials,
  connectChannel,
  disconnectChannel,
  updateChannelSettings,
  updateChannelToken,
  machineExec,
} from "../lib/api";
import type { Machine } from "../lib/types";
import type { MachineCapability } from "../lib/api";
import { isRestartRequiredLiveUpdate } from "../lib/liveUpdate";
import { useToast } from "./Toast";

interface ChannelSetupProps {
  accountId: number;
}

// --- Channel definitions ---

const CHANNELS = [
  {
    id: "telegram" as const,
    label: "Telegram",
    iconLetter: "T",
    iconBg: "bg-sky-600",
    tokenField: "Bot Token",
    instructions: {
      title: "Create a Telegram Bot",
      steps: [
        "Open Telegram and search for @BotFather",
        "Send /newbot and follow the prompts to name your bot",
        "BotFather will give you a bot token — copy it",
      ],
      link: "https://t.me/BotFather",
      linkLabel: "Open BotFather",
    },
  },
  {
    id: "discord" as const,
    label: "Discord",
    iconLetter: "D",
    iconBg: "bg-indigo-600",
    tokenField: "Bot Token",
    instructions: {
      title: "Create a Discord Bot",
      steps: [
        "Go to the Discord Developer Portal",
        "Create a New Application, then go to the Bot tab",
        "Click Reset Token to generate a bot token — copy it",
        "Under Privileged Gateway Intents, enable Message Content Intent",
      ],
      link: "https://discord.com/developers/applications",
      linkLabel: "Open Developer Portal",
    },
  },
  {
    id: "whatsapp" as const,
    label: "WhatsApp",
    iconLetter: "W",
    iconBg: "bg-green-600",
    tokenField: null, // QR-based, no token
    instructions: null,
    comingSoon: true,
  },
] as const;

type ChannelId = "telegram" | "discord" | "whatsapp";

// --- Wizard state ---

type WizardStep = "select" | "instructions" | "token" | "machine" | "groups" | "completing";

interface PipelineStep {
  label: string;
  status: "pending" | "running" | "done" | "error";
  error?: string;
}

interface GroupEntry {
  id: string;
  requireMention: boolean;
}

interface WizardState {
  step: WizardStep;
  channel: ChannelId | null;
  tokenValue: string;
  tokenLabel: string;
  selectedMachineId: string | null;
  allowedUsers: string;
  groups: GroupEntry[];
  pipeline: PipelineStep[];
  pipelineError: string | null;
}

type WizardAction =
  | { type: "SET_STEP"; step: WizardStep }
  | { type: "SET_CHANNEL"; channel: ChannelId }
  | { type: "SET_TOKEN"; value: string }
  | { type: "SET_TOKEN_LABEL"; label: string }
  | { type: "SET_MACHINE"; machineId: string }
  | { type: "SET_ALLOWED_USERS"; value: string }
  | { type: "ADD_GROUP" }
  | { type: "REMOVE_GROUP"; index: number }
  | { type: "UPDATE_GROUP"; index: number; field: "id" | "requireMention"; value: string | boolean }
  | { type: "SET_PIPELINE"; pipeline: PipelineStep[] }
  | { type: "UPDATE_PIPELINE_STEP"; index: number; status: PipelineStep["status"]; error?: string }
  | { type: "SET_PIPELINE_ERROR"; error: string }
  | { type: "SET_GROUPS"; groups: GroupEntry[] }
  | { type: "RESET" };

const initialWizardState: WizardState = {
  step: "select",
  channel: null,
  tokenValue: "",
  tokenLabel: "",
  selectedMachineId: null,
  allowedUsers: "",
  groups: [],
  pipeline: [],
  pipelineError: null,
};

function wizardReducer(state: WizardState, action: WizardAction): WizardState {
  switch (action.type) {
    case "SET_STEP":
      return { ...state, step: action.step };
    case "SET_CHANNEL":
      return { ...state, channel: action.channel };
    case "SET_TOKEN":
      return { ...state, tokenValue: action.value };
    case "SET_TOKEN_LABEL":
      return { ...state, tokenLabel: action.label };
    case "SET_MACHINE":
      return { ...state, selectedMachineId: action.machineId };
    case "SET_ALLOWED_USERS":
      return { ...state, allowedUsers: action.value };
    case "ADD_GROUP":
      return { ...state, groups: [...state.groups, { id: "", requireMention: true }] };
    case "REMOVE_GROUP":
      return { ...state, groups: state.groups.filter((_, i) => i !== action.index) };
    case "UPDATE_GROUP": {
      const groups = [...state.groups];
      groups[action.index] = { ...groups[action.index], [action.field]: action.value };
      return { ...state, groups };
    }
    case "SET_GROUPS":
      return { ...state, groups: action.groups };
    case "SET_PIPELINE":
      return { ...state, pipeline: action.pipeline, pipelineError: null };
    case "UPDATE_PIPELINE_STEP": {
      const pipeline = [...state.pipeline];
      pipeline[action.index] = { ...pipeline[action.index], status: action.status, error: action.error };
      return { ...state, pipeline };
    }
    case "SET_PIPELINE_ERROR":
      return { ...state, pipelineError: action.error };
    case "RESET":
      return initialWizardState;
    default:
      return state;
  }
}

// --- Status types ---

interface ChannelGroupConfig {
  id: string;
  requireMention: boolean;
  groupPolicy: string;
}

interface MachineChannelStatus {
  machine: Machine;
  capabilities: MachineCapability[];
  channels: {
    channelId: ChannelId;
    enabled: boolean;
    hasCredential: boolean;
    credentialId?: number;
    credentialLabel?: string;
    credentialLastFour?: string;
    allowedUsers?: string;
    groups?: ChannelGroupConfig[];
  }[];
}

function readObject(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  return value as Record<string, unknown>;
}

function readStringSetting(source: unknown, keys: string[]): string {
  const record = readObject(source);
  if (!record) return "";
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string") return value;
    if (Array.isArray(value)) {
      return value.filter((item): item is string => typeof item === "string").join(", ");
    }
  }
  return "";
}

function channelConfigFromOverrides(raw: unknown, channelId: ChannelId): Record<string, unknown> | undefined {
  if (!raw) return undefined;
  let overrides: unknown = raw;
  if (typeof raw === "string") {
    try {
      overrides = JSON.parse(raw);
    } catch {
      return undefined;
    }
  }
  const channels = readObject(readObject(overrides)?.channels);
  return readObject(channels?.[channelId]);
}

function isHermesMachine(machine: Machine | undefined): boolean {
  return (machine?.kind ?? "openclaw") === "hermes";
}

// --- Main component ---

export function ChannelSetup({ accountId }: ChannelSetupProps) {
  const { toast } = useToast();
  const [machines, setMachines] = useState<Machine[]>([]);
  const [machineStatuses, setMachineStatuses] = useState<MachineChannelStatus[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Wizard
  const [wizardOpen, setWizardOpen] = useState(false);
  const [wizard, dispatch] = useReducer(wizardReducer, initialWizardState);

  // Pairing form
  const [pairingMachineId, setPairingMachineId] = useState("");
  const [pairingChannel, setPairingChannel] = useState("");
  const [pairingCode, setPairingCode] = useState("");
  const [pairingSubmitting, setPairingSubmitting] = useState(false);
  const [pairingResult, setPairingResult] = useState<{ type: "success" | "error"; message: string } | null>(null);

  // Change Token state
  const [changeTokenKey, setChangeTokenKey] = useState<string | null>(null); // "machineId:channelId"
  const [changeTokenValue, setChangeTokenValue] = useState("");
  const [changeTokenLabel, setChangeTokenLabel] = useState("");
  const [changeTokenSaving, setChangeTokenSaving] = useState(false);

  // Inline group editor state
  const [editingGroupsKey, setEditingGroupsKey] = useState<string | null>(null); // machineId or null
  const [editingGroups, setEditingGroups] = useState<GroupEntry[]>([]);
  const [savingGroups, setSavingGroups] = useState(false);
  const [editingAllowedUsersKey, setEditingAllowedUsersKey] = useState<string | null>(null);
  const [editingAllowedUsers, setEditingAllowedUsers] = useState("");
  const [savingAllowedUsers, setSavingAllowedUsers] = useState(false);

  // Disable state
  const [disabling, setDisabling] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    try {
      const machineList = await listMachines(accountId);
      setMachines(machineList || []);

      // Fetch capabilities per machine
      const statuses: MachineChannelStatus[] = [];
      for (const machine of machineList || []) {
        try {
          const [caps, machineCreds] = await Promise.all([
            listMachineCapabilities(accountId, machine.id),
            listMachineCredentials(accountId, machine.id),
          ]);
          const channelCreds = (machineCreds || []).filter(
            (c) => ["telegram", "discord", "whatsapp"].includes(c.provider)
          );
          statuses.push({
            machine,
            capabilities: caps || [],
            channels: CHANNELS.map((ch) => {
              const cap = (caps || []).find((c) => c.entry_id === ch.id && c.enabled);
              const isEnabled = !!cap;
              const linkedCred = channelCreds.find((c) => c.provider === ch.id);

              // Extract Telegram channel settings from capability overrides.
              let groups: ChannelGroupConfig[] | undefined;
              let allowedUsers: string | undefined;
              if (cap?.config_overrides && ch.id === "telegram") {
                try {
                  const tgConfig = channelConfigFromOverrides(cap.config_overrides, ch.id);
                  allowedUsers = readStringSetting(tgConfig, ["allowedUsers", "allowed_users", "allowedUserIds", "allowed_user_ids"]) || undefined;
                  const groupsObj = tgConfig?.groups as Record<string, { groupPolicy?: string; requireMention?: boolean }> | undefined;
                  if (groupsObj && Object.keys(groupsObj).length > 0) {
                    groups = Object.entries(groupsObj).map(([id, cfg]) => ({
                      id,
                      requireMention: cfg.requireMention !== false,
                      groupPolicy: cfg.groupPolicy || "open",
                    }));
                  }
                } catch { /* ignore malformed overrides */ }
              }

              return {
                channelId: ch.id,
                enabled: isEnabled,
                hasCredential: !!linkedCred,
                credentialId: linkedCred?.id,
                credentialLabel: linkedCred?.label,
                credentialLastFour: linkedCred?.last_four,
                allowedUsers,
                groups,
              };
            }),
          });
        } catch {
          // skip machine if capabilities can't be loaded
        }
      }
      setMachineStatuses(statuses);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load data");
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  // --- Wizard handlers ---

  const openWizard = (channelId?: ChannelId) => {
    dispatch({ type: "RESET" });
    if (channelId) {
      dispatch({ type: "SET_CHANNEL", channel: channelId });
      dispatch({ type: "SET_STEP", step: "instructions" });
    }
    setWizardOpen(true);
  };

  const handleWizardClose = (open: boolean) => {
    if (!open) {
      setWizardOpen(false);
      fetchData();
    }
  };

  const runPipeline = async () => {
    if (!wizard.channel || !wizard.selectedMachineId) return;
    const selectedMachine = machines.find((m) => m.id === wizard.selectedMachineId);
    const isHermesTelegram = isHermesMachine(selectedMachine) && wizard.channel === "telegram";
    if (isHermesTelegram && !wizard.allowedUsers.trim()) {
      toast({
        title: "Allowed users required",
        description: "Add at least one numeric Telegram user ID.",
        variant: "error",
      });
      return;
    }
    dispatch({ type: "SET_STEP", step: "completing" });

    const hasGroups = !isHermesTelegram && wizard.channel === "telegram" && wizard.groups.length > 0 && wizard.groups.every((g) => g.id.trim());

    const pipelineSteps: PipelineStep[] = [
      { label: "Connecting channel", status: "pending" },
      ...(hasGroups ? [{ label: "Saving group config", status: "pending" as const }] : []),
    ];
    dispatch({ type: "SET_PIPELINE", pipeline: pipelineSteps });

    let currentStep = 0;
    const run = async (index: number, fn: () => Promise<unknown>) => {
      currentStep = index;
      dispatch({ type: "UPDATE_PIPELINE_STEP", index, status: "running" });
      await fn();
      dispatch({ type: "UPDATE_PIPELINE_STEP", index, status: "done" });
    };

    try {
      let stepIdx = 0;

      let connectRes: Awaited<ReturnType<typeof connectChannel>> | undefined;
      await run(stepIdx++, async () => {
        connectRes = await connectChannel(accountId, wizard.selectedMachineId!, wizard.channel!, {
          token: wizard.tokenValue,
          ...(isHermesTelegram ? { settings: { allowedUsers: wizard.allowedUsers.trim() } } : {}),
        });
      });

      if (hasGroups) {
        await run(stepIdx++, () => {
          const groupsObj: Record<string, { groupPolicy: string; requireMention: boolean }> = {};
          for (const g of wizard.groups) {
            groupsObj[g.id.trim()] = { groupPolicy: "open", requireMention: g.requireMention };
          }
          return updateChannelSettings(accountId, wizard.selectedMachineId!, wizard.channel!, {
            groups: groupsObj,
          });
        });
      }

      if (connectRes?.live_update === "failed") {
        const restartRequired = isRestartRequiredLiveUpdate(connectRes.live_update_error);
        toast({
          title: restartRequired
            ? "Channel saved, restart Hermes to apply"
            : "Channel saved but failed to push config to VM",
          description: connectRes.live_update_error,
          variant: restartRequired ? "success" : "error",
        });
      } else if (connectRes?.live_update === "not_running") {
        toast({ title: `${wizard.channel} enabled (will apply on next start)`, variant: "success" });
      } else {
        toast({ title: "Channel set up", description: `${wizard.channel} is now enabled`, variant: "success" });
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : "Setup failed";
      dispatch({ type: "UPDATE_PIPELINE_STEP", index: currentStep, status: "error", error: message });
    }
  };

  const handleDisableChannel = async (machineId: string, channelId: ChannelId) => {
    const key = `${machineId}:${channelId}`;
    setDisabling(key);
    try {
      const res = await disconnectChannel(accountId, machineId, channelId);
      if (res.live_update === "failed") {
        const restartRequired = isRestartRequiredLiveUpdate(res.live_update_error);
        toast({
          title: restartRequired
            ? "Channel removed, restart Hermes to apply"
            : "Channel removed but failed to update running VM",
          description: res.live_update_error,
          variant: restartRequired ? "success" : "error",
        });
      } else {
        toast({ title: "Channel disabled", description: `${channelId} has been disabled`, variant: "success" });
      }
      fetchData();
    } catch (err) {
      toast({ title: "Error", description: err instanceof Error ? err.message : "Failed to disable", variant: "error" });
    } finally {
      setDisabling(null);
    }
  };

  // --- Inline Group Editor ---

  const handleSaveGroups = async (machineId: string) => {
    setSavingGroups(true);
    try {
      const groupsObj: Record<string, { groupPolicy: string; requireMention: boolean }> = {};
      for (const g of editingGroups) {
        groupsObj[g.id.trim()] = { groupPolicy: "open", requireMention: g.requireMention };
      }
      const res = await updateChannelSettings(accountId, machineId, "telegram", {
        groups: groupsObj,
      });
      setEditingGroupsKey(null);
      await fetchData();
      if (res.live_update === "failed") {
        const restartRequired = isRestartRequiredLiveUpdate(res.live_update_error);
        toast({
          title: restartRequired
            ? "Groups saved, restart Hermes to apply"
            : "Groups saved but failed to push to VM",
          description: res.live_update_error,
          variant: restartRequired ? "success" : "error",
        });
      } else {
        toast({ title: "Groups updated", description: "Telegram group config saved", variant: "success" });
      }
    } catch (err) {
      toast({ title: "Failed to save groups", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setSavingGroups(false);
    }
  };

  const handleSaveAllowedUsers = async (machineId: string) => {
    if (!editingAllowedUsers.trim()) {
      toast({
        title: "Allowed users required",
        description: "Add at least one numeric Telegram user ID.",
        variant: "error",
      });
      return;
    }
    setSavingAllowedUsers(true);
    try {
      const res = await updateChannelSettings(accountId, machineId, "telegram", {
        allowedUsers: editingAllowedUsers.trim(),
      });
      setEditingAllowedUsersKey(null);
      await fetchData();
      if (res.live_update === "failed") {
        const restartRequired = isRestartRequiredLiveUpdate(res.live_update_error);
        toast({
          title: restartRequired
            ? "Allowed users saved, restart Hermes to apply"
            : "Allowed users saved but failed to push to VM",
          description: res.live_update_error,
          variant: restartRequired ? "success" : "error",
        });
      } else {
        toast({ title: "Allowed users updated", description: "Telegram access settings saved", variant: "success" });
      }
    } catch (err) {
      toast({ title: "Failed to save allowed users", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setSavingAllowedUsers(false);
    }
  };

  // --- Change Token ---

  const openChangeToken = (machineId: string, channelId: ChannelId) => {
    const key = `${machineId}:${channelId}`;
    if (changeTokenKey === key) {
      setChangeTokenKey(null);
      return;
    }
    setChangeTokenKey(key);
    setChangeTokenValue("");
    setChangeTokenLabel("");
  };

  const handleChangeToken = async (machineId: string, channelId: ChannelId) => {
    setChangeTokenSaving(true);
    try {
      const res = await updateChannelToken(accountId, machineId, channelId, changeTokenValue);
      if (res.live_update === "failed") {
        const restartRequired = isRestartRequiredLiveUpdate(res.live_update_error);
        toast({
          title: restartRequired
            ? "Token saved, restart Hermes to apply"
            : "Token saved but failed to push to VM",
          description: res.live_update_error,
          variant: restartRequired ? "success" : "error",
        });
      } else {
        toast({ title: "Token changed", description: `${channelId} credential updated`, variant: "success" });
      }
      setChangeTokenKey(null);
      fetchData();
    } catch (err) {
      toast({ title: "Error", description: err instanceof Error ? err.message : "Failed to change token", variant: "error" });
    } finally {
      setChangeTokenSaving(false);
    }
  };

  // --- Pairing ---

  const handlePairingSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!pairingMachineId || !pairingChannel || !pairingCode) return;
    setPairingSubmitting(true);
    setPairingResult(null);

    try {
      const result = await machineExec(accountId, pairingMachineId, [
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

  const runningMachinesWithChannels = machineStatuses.filter(
    (ms) => ms.machine.status === "running" && ms.channels.some((c) => c.enabled)
  );
  const selectedWizardMachine = machines.find((m) => m.id === wizard.selectedMachineId);
  const wizardMachineIsHermes = isHermesMachine(selectedWizardMachine);

  // --- Render ---

  if (loading) {
    return <p className="text-sm text-gray-500 dark:text-gray-400">Loading channels...</p>;
  }

  if (error) {
    return (
      <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-4">
        <p className="text-sm text-red-700 dark:text-red-400">{error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      {/* Section: Channel Status Overview */}
      <div>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">Channel Status</h2>
          <button
            onClick={() => openWizard()}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg transition-colors"
          >
            <span className="text-base leading-none">+</span> Set Up Channel
          </button>
        </div>

        {machines.length === 0 ? (
          <div className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border p-6 text-center">
            <p className="text-sm text-gray-500 dark:text-gray-400">No machines yet. Create a machine first to set up channels.</p>
          </div>
        ) : (
          <div className="space-y-3">
            {machineStatuses.map((ms) => (
              <div
                key={ms.machine.id}
                className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border p-4"
              >
                <div className="flex items-center gap-2 mb-3">
                  <span className="font-medium text-gray-900 dark:text-gray-100">{ms.machine.name}</span>
                  <StatusBadge status={ms.machine.status} />
                </div>
                <div className="space-y-2">
                  {ms.channels.map((ch) => {
                    const channelDef = CHANNELS.find((c) => c.id === ch.channelId)!;
                    const isComingSoon = "comingSoon" in channelDef && channelDef.comingSoon;
                    const isHermesTelegram = isHermesMachine(ms.machine) && ch.channelId === "telegram";
                    return (
                      <div key={ch.channelId} className="text-sm">
                        <div className="flex items-center justify-between">
                          <div className="flex items-center gap-2">
                            <span
                              className={`w-5 h-5 rounded text-[10px] font-bold text-white flex items-center justify-center ${channelDef.iconBg}`}
                            >
                              {channelDef.iconLetter}
                            </span>
                            <span className="text-gray-700 dark:text-gray-300">{channelDef.label}</span>
                            {isComingSoon ? (
                              <span className="text-xs text-gray-400 dark:text-gray-500 italic">Coming soon</span>
                            ) : ch.enabled ? (
                              <span className="inline-flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
                                <span className="w-1.5 h-1.5 rounded-full bg-emerald-500" />
                                Enabled
                              </span>
                            ) : (
                              <span className="text-xs text-gray-400 dark:text-gray-500">Not set up</span>
                            )}
                          </div>
                          <div className="flex items-center gap-2">
                            {isComingSoon ? null : ch.enabled ? (
                              <>
                                {ch.hasCredential && (
                                  <button
                                    onClick={() => openChangeToken(ms.machine.id, ch.channelId)}
                                    className={`text-xs ${
                                      changeTokenKey === `${ms.machine.id}:${ch.channelId}`
                                        ? "text-brand-700 dark:text-brand-300 font-medium"
                                        : "text-brand-600 dark:text-brand-400 hover:text-brand-700 dark:hover:text-brand-300"
                                    }`}
                                  >
                                    Change Token
                                  </button>
                                )}
                                {ch.channelId === "telegram" && (
                                  <button
                                    onClick={() => {
                                      if (isHermesTelegram) {
                                        if (editingAllowedUsersKey === ms.machine.id) {
                                          setEditingAllowedUsersKey(null);
                                        } else {
                                          setEditingAllowedUsers(ch.allowedUsers || "");
                                          setEditingAllowedUsersKey(ms.machine.id);
                                          setEditingGroupsKey(null);
                                        }
                                      } else if (editingGroupsKey === ms.machine.id) {
                                        setEditingGroupsKey(null);
                                      } else {
                                        setEditingGroups(ch.groups?.map(g => ({ id: g.id, requireMention: g.requireMention })) || []);
                                        setEditingGroupsKey(ms.machine.id);
                                        setEditingAllowedUsersKey(null);
                                      }
                                    }}
                                    className={`text-xs ${
                                      (isHermesTelegram ? editingAllowedUsersKey : editingGroupsKey) === ms.machine.id
                                        ? "text-brand-700 dark:text-brand-300 font-medium"
                                        : "text-brand-600 dark:text-brand-400 hover:text-brand-700 dark:hover:text-brand-300"
                                    }`}
                                  >
                                    {isHermesTelegram ? "Configure Users" : "Configure Groups"}
                                  </button>
                                )}
                                <button
                                  onClick={() => handleDisableChannel(ms.machine.id, ch.channelId)}
                                  disabled={disabling === `${ms.machine.id}:${ch.channelId}`}
                                  className="text-xs text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300 disabled:opacity-50"
                                >
                                  {disabling === `${ms.machine.id}:${ch.channelId}` ? "Disabling..." : "Disable"}
                                </button>
                              </>
                            ) : (
                              <button
                                onClick={() => openWizard(ch.channelId)}
                                className="text-xs text-brand-600 dark:text-brand-400 hover:text-brand-700 dark:hover:text-brand-300"
                              >
                                Set Up
                              </button>
                            )}
                          </div>
                        </div>

                        {/* Configuration details for enabled channels */}
                        {ch.enabled && !isComingSoon && (
                          <div className="ml-7 mt-1.5 space-y-1">
                            {/* Credential info */}
                            {ch.hasCredential && ch.credentialLabel && (
                              <div className="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
                                <span className="text-gray-400 dark:text-gray-500">Token:</span>
                                <span>{ch.credentialLabel}</span>
                                {ch.credentialLastFour && (
                                  <span className="font-mono text-gray-400 dark:text-gray-500">····{ch.credentialLastFour}</span>
                                )}
                              </div>
                            )}

                            {/* Telegram settings */}
                            {ch.channelId === "telegram" && (
                              <div className="text-xs">
                                {isHermesTelegram ? (
                                  <div className="space-y-1">
                                    <span className="text-gray-400 dark:text-gray-500">Allowed users:</span>
                                    {ch.allowedUsers ? (
                                      <span className="ml-2 font-mono text-gray-600 dark:text-gray-400">{ch.allowedUsers}</span>
                                    ) : (
                                      <span className="ml-2 text-amber-600 dark:text-amber-400">Required before Telegram messages work</span>
                                    )}
                                  </div>
                                ) : ch.groups && ch.groups.length > 0 ? (
                                  <div className="space-y-1">
                                    <span className="text-gray-400 dark:text-gray-500">Groups:</span>
                                    {ch.groups.map((g) => (
                                      <div key={g.id} className="flex items-center gap-2 ml-2 text-gray-600 dark:text-gray-400">
                                        <span className="font-mono">{g.id}</span>
                                        <span className={`inline-flex items-center px-1.5 py-0 rounded text-[10px] ${
                                          g.requireMention
                                            ? "bg-blue-50 text-blue-600 dark:bg-blue-900/20 dark:text-blue-400"
                                            : "bg-amber-50 text-amber-600 dark:bg-amber-900/20 dark:text-amber-400"
                                        }`}>
                                          {g.requireMention ? "@mention required" : "responds to all"}
                                        </span>
                                      </div>
                                    ))}
                                  </div>
                                ) : (
                                  <span className="text-gray-400 dark:text-gray-500">No groups configured — bot responds to DMs only</span>
                                )}
                              </div>
                            )}

                            {/* Inline Hermes Telegram allowed users editor */}
                            {isHermesTelegram && editingAllowedUsersKey === ms.machine.id && (
                              <div className="mt-2 p-3 rounded-lg border border-gray-200 dark:border-border bg-gray-50 dark:bg-surface-elevated space-y-3">
                                <div className="space-y-1.5">
                                  <label className="block text-xs font-medium text-gray-700 dark:text-gray-300">
                                    Allowed Telegram user IDs
                                  </label>
                                  <input
                                    type="text"
                                    value={editingAllowedUsers}
                                    onChange={(e) => setEditingAllowedUsers(e.target.value)}
                                    placeholder="123456789, 987654321"
                                    className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-md px-2.5 py-1.5 text-xs focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none font-mono"
                                  />
                                  <p className="text-xs text-gray-500 dark:text-gray-400">
                                    Hermes only accepts Telegram messages from these numeric IDs.
                                  </p>
                                </div>
                                <div className="flex justify-end gap-2">
                                  <button
                                    onClick={() => setEditingAllowedUsersKey(null)}
                                    className="px-2.5 py-1 text-xs text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                                  >
                                    Cancel
                                  </button>
                                  <button
                                    onClick={() => handleSaveAllowedUsers(ms.machine.id)}
                                    disabled={savingAllowedUsers || !editingAllowedUsers.trim()}
                                    className="px-3 py-1 text-xs font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-md disabled:opacity-50 disabled:cursor-not-allowed"
                                  >
                                    {savingAllowedUsers ? "Saving..." : "Save"}
                                  </button>
                                </div>
                              </div>
                            )}

                            {/* Inline group editor panel */}
                            {ch.channelId === "telegram" && !isHermesTelegram && editingGroupsKey === ms.machine.id && (
                              <div className="mt-2 p-3 rounded-lg border border-gray-200 dark:border-border bg-gray-50 dark:bg-surface-elevated space-y-3">
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
                                      className="flex-1 border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-md px-2.5 py-1.5 text-xs focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none font-mono"
                                    />
                                    <label className="flex items-center gap-1 text-xs text-gray-600 dark:text-gray-400 whitespace-nowrap cursor-pointer">
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
                                      className="text-gray-400 hover:text-red-500 dark:text-gray-500 dark:hover:text-red-400 p-0.5"
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
                                  className="text-xs text-brand-600 dark:text-brand-400 hover:text-brand-700 dark:hover:text-brand-300"
                                >
                                  + Add Group
                                </button>
                                <div className="flex justify-end gap-2">
                                  <button
                                    onClick={() => setEditingGroupsKey(null)}
                                    className="px-2.5 py-1 text-xs text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                                  >
                                    Cancel
                                  </button>
                                  <button
                                    onClick={() => handleSaveGroups(ms.machine.id)}
                                    disabled={savingGroups || editingGroups.some(g => !g.id.trim())}
                                    className="px-3 py-1 text-xs font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-md disabled:opacity-50 disabled:cursor-not-allowed"
                                  >
                                    {savingGroups ? "Saving..." : "Save"}
                                  </button>
                                </div>
                              </div>
                            )}

                            {/* Change Token inline panel */}
                            {changeTokenKey === `${ms.machine.id}:${ch.channelId}` && ch.credentialId && (() => {
                              const canApply = changeTokenValue.trim().length > 0;

                              return (
                                <div className="mt-2 p-3 rounded-lg border border-gray-200 dark:border-border bg-gray-50 dark:bg-surface-elevated space-y-3">
                                  <div className="space-y-2">
                                    <input
                                      type="password"
                                      value={changeTokenValue}
                                      onChange={(e) => setChangeTokenValue(e.target.value)}
                                      placeholder={`Paste your ${ch.channelId} bot token`}
                                      className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-md px-2.5 py-1.5 text-xs focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none"
                                    />
                                    <input
                                      type="text"
                                      value={changeTokenLabel}
                                      onChange={(e) => setChangeTokenLabel(e.target.value)}
                                      placeholder="Label (optional)"
                                      className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-md px-2.5 py-1.5 text-xs focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none"
                                    />
                                  </div>

                                  {/* Actions */}
                                  <div className="flex justify-end gap-2">
                                    <button
                                      onClick={() => setChangeTokenKey(null)}
                                      className="px-2.5 py-1 text-xs text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                                    >
                                      Cancel
                                    </button>
                                    <button
                                      onClick={() => handleChangeToken(ms.machine.id, ch.channelId)}
                                      disabled={!canApply || changeTokenSaving}
                                      className="px-3 py-1 text-xs font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-md disabled:opacity-50 disabled:cursor-not-allowed"
                                    >
                                      {changeTokenSaving ? "Applying..." : "Apply"}
                                    </button>
                                  </div>
                                </div>
                              );
                            })()}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Section: Setup Wizard Modal */}
      <Dialog.Root open={wizardOpen} onOpenChange={handleWizardClose}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/50 z-50" />
          <Dialog.Content className="fixed left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 bg-white dark:bg-surface-card rounded-xl border border-gray-200 dark:border-border shadow-xl w-full max-w-lg max-h-[85vh] overflow-y-auto p-6 z-50">
            <Dialog.Title className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-4">
              {wizard.step === "select" && "Select Channel"}
              {wizard.step === "instructions" && `Set Up ${CHANNELS.find((c) => c.id === wizard.channel)?.label || ""}`}
              {wizard.step === "token" && "Enter Bot Token"}
              {wizard.step === "machine" && "Select Machine"}
              {wizard.step === "groups" && (wizardMachineIsHermes ? "Allowed Telegram Users" : "Group Settings (Optional)")}
              {wizard.step === "completing" && "Setting Up..."}
            </Dialog.Title>

            {/* Step: Select Channel */}
            {wizard.step === "select" && (
              <div className="grid grid-cols-3 gap-3">
                {CHANNELS.map((ch) => {
                  const isComingSoon = "comingSoon" in ch && ch.comingSoon;
                  return (
                    <button
                      key={ch.id}
                      disabled={isComingSoon}
                      onClick={() => {
                        dispatch({ type: "SET_CHANNEL", channel: ch.id });
                        dispatch({ type: "SET_STEP", step: "instructions" });
                      }}
                      className={`flex flex-col items-center gap-2 p-4 rounded-lg border transition-colors ${
                        isComingSoon
                          ? "opacity-50 cursor-not-allowed border-gray-200 dark:border-border"
                          : "border-gray-200 dark:border-border hover:border-brand-300 dark:hover:border-brand-600 hover:bg-brand-50 dark:hover:bg-brand-900/20 cursor-pointer"
                      }`}
                    >
                      <span
                        className={`w-10 h-10 rounded-lg text-lg font-bold text-white flex items-center justify-center ${ch.iconBg}`}
                      >
                        {ch.iconLetter}
                      </span>
                      <span className="text-sm font-medium text-gray-900 dark:text-gray-100">{ch.label}</span>
                      {isComingSoon && (
                        <span className="text-[10px] text-gray-400 dark:text-gray-500">Coming Soon</span>
                      )}
                    </button>
                  );
                })}
              </div>
            )}

            {/* Step: Instructions */}
            {wizard.step === "instructions" && wizard.channel && (() => {
              const ch = CHANNELS.find((c) => c.id === wizard.channel);
              if (!ch || !ch.instructions) return null;
              return (
                <div className="space-y-4">
                  <ol className="space-y-2">
                    {ch.instructions.steps.map((step, i) => (
                      <li key={i} className="flex gap-3 text-sm text-gray-700 dark:text-gray-300">
                        <span className="flex-shrink-0 w-5 h-5 rounded-full bg-gray-100 dark:bg-surface-elevated text-gray-500 dark:text-gray-400 text-xs flex items-center justify-center font-medium">
                          {i + 1}
                        </span>
                        {step}
                      </li>
                    ))}
                  </ol>
                  <a
                    href={ch.instructions.link}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 text-sm text-brand-600 dark:text-brand-400 hover:underline"
                  >
                    {ch.instructions.linkLabel} &rarr;
                  </a>
                  <div className="flex justify-end gap-2 pt-2">
                    <button
                      onClick={() => dispatch({ type: "SET_STEP", step: "select" })}
                      className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                    >
                      Back
                    </button>
                    <button
                      onClick={() => dispatch({ type: "SET_STEP", step: "token" })}
                      className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg"
                    >
                      I have my token
                    </button>
                  </div>
                </div>
              );
            })()}

            {/* Step: Enter Token */}
            {wizard.step === "token" && (
              <div className="space-y-4">
                <div>
                  <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                    Bot Token
                  </label>
                  <input
                    type="password"
                    value={wizard.tokenValue}
                    onChange={(e) => dispatch({ type: "SET_TOKEN", value: e.target.value })}
                    placeholder={`Paste your ${wizard.channel} bot token`}
                    className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none"
                    autoFocus
                  />
                </div>
                <div>
                  <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                    Label <span className="text-gray-400 font-normal">(optional)</span>
                  </label>
                  <input
                    type="text"
                    value={wizard.tokenLabel}
                    onChange={(e) => dispatch({ type: "SET_TOKEN_LABEL", label: e.target.value })}
                    placeholder="e.g. My Telegram Bot"
                    className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none"
                  />
                </div>
                <div className="flex justify-end gap-2 pt-2">
                  <button
                    onClick={() => dispatch({ type: "SET_STEP", step: "instructions" })}
                    className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                  >
                    Back
                  </button>
                  <button
                    onClick={() => dispatch({ type: "SET_STEP", step: "machine" })}
                    disabled={!wizard.tokenValue.trim()}
                    className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    Next
                  </button>
                </div>
              </div>
            )}

            {/* Step: Select Machine */}
            {wizard.step === "machine" && (
              <div className="space-y-4">
                {machines.length === 0 ? (
                  <p className="text-sm text-gray-500 dark:text-gray-400">No machines available. Create a machine first.</p>
                ) : (
                  <div className="space-y-2">
                    {machines.map((m) => (
                      <label
                        key={m.id}
                        className={`flex items-center gap-3 p-3 rounded-lg border cursor-pointer transition-colors ${
                          wizard.selectedMachineId === m.id
                            ? "border-brand-500 bg-brand-50 dark:bg-brand-900/20"
                            : "border-gray-200 dark:border-border hover:border-gray-300 dark:hover:border-gray-600"
                        }`}
                      >
                        <input
                          type="radio"
                          name="wizard-machine"
                          checked={wizard.selectedMachineId === m.id}
                          onChange={() => dispatch({ type: "SET_MACHINE", machineId: m.id })}
                          className="accent-brand-600"
                        />
                        <div className="flex-1 min-w-0">
                          <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{m.name}</p>
                        </div>
                        <StatusBadge status={m.status} />
                      </label>
                    ))}
                  </div>
                )}
                <div className="flex justify-end gap-2 pt-2">
                  <button
                    onClick={() => dispatch({ type: "SET_STEP", step: "token" })}
                    className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                  >
                    Back
                  </button>
                  <button
                    onClick={() => wizard.channel === "telegram"
                      ? dispatch({ type: "SET_STEP", step: "groups" })
                      : runPipeline()
                    }
                    disabled={!wizard.selectedMachineId}
                    className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {wizard.channel === "telegram" ? "Next" : "Set Up"}
                  </button>
                </div>
              </div>
            )}

            {/* Step: Group Settings (Telegram only) */}
            {wizard.step === "groups" && (
              <div className="space-y-4">
                {wizardMachineIsHermes ? (
                  <>
                    <div className="space-y-2">
                      <p className="text-sm text-gray-600 dark:text-gray-400">
                        Hermes only accepts Telegram messages from allowed numeric user IDs.
                      </p>
                      <label className="block text-sm font-medium text-gray-700 dark:text-gray-300">
                        Allowed Telegram user IDs
                      </label>
                      <input
                        type="text"
                        value={wizard.allowedUsers}
                        onChange={(e) => dispatch({ type: "SET_ALLOWED_USERS", value: e.target.value })}
                        placeholder="123456789, 987654321"
                        className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none font-mono"
                        autoFocus
                      />
                    </div>
                    <div className="flex justify-end gap-2 pt-2">
                      <button
                        onClick={() => dispatch({ type: "SET_STEP", step: "machine" })}
                        className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                      >
                        Back
                      </button>
                      <button
                        onClick={runPipeline}
                        disabled={!wizard.allowedUsers.trim()}
                        className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        Set Up
                      </button>
                    </div>
                  </>
                ) : (
                  <>
                    <p className="text-sm text-gray-600 dark:text-gray-400">
                      Configure which Telegram groups your bot responds in. Skip this step to use default settings (require @mention in all groups).
                    </p>

                    {wizard.groups.map((group, i) => (
                      <div key={i} className="flex items-center gap-2">
                        <input
                          type="text"
                          value={group.id}
                          onChange={(e) => dispatch({ type: "UPDATE_GROUP", index: i, field: "id", value: e.target.value })}
                          placeholder="Group ID (e.g. -1001234567890)"
                          className="flex-1 border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm focus:ring-2 focus:ring-brand-500 focus:border-transparent outline-none"
                        />
                        <label className="flex items-center gap-1.5 text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap cursor-pointer">
                          <input
                            type="checkbox"
                            checked={group.requireMention}
                            onChange={(e) => dispatch({ type: "UPDATE_GROUP", index: i, field: "requireMention", value: e.target.checked })}
                            className="accent-brand-600"
                          />
                          @mention
                        </label>
                        <button
                          onClick={() => dispatch({ type: "REMOVE_GROUP", index: i })}
                          className="text-gray-400 hover:text-red-500 dark:text-gray-500 dark:hover:text-red-400 p-1"
                          aria-label="Remove group"
                        >
                          <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                          </svg>
                        </button>
                      </div>
                    ))}

                    <button
                      onClick={() => dispatch({ type: "ADD_GROUP" })}
                      className="text-sm text-brand-600 dark:text-brand-400 hover:text-brand-700 dark:hover:text-brand-300"
                    >
                      + Add Group
                    </button>

                    <div className="flex justify-end gap-2 pt-2">
                      <button
                        onClick={() => dispatch({ type: "SET_STEP", step: "machine" })}
                        className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                      >
                        Back
                      </button>
                      <button
                        onClick={runPipeline}
                        className="px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800 dark:hover:text-gray-200"
                      >
                        Skip
                      </button>
                      <button
                        onClick={runPipeline}
                        disabled={wizard.groups.length === 0 || wizard.groups.some((g) => !g.id.trim())}
                        className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        Next
                      </button>
                    </div>
                  </>
                )}
              </div>
            )}

            {/* Step: Completing */}
            {wizard.step === "completing" && (
              <div className="space-y-3">
                {wizard.pipeline.map((step, i) => (
                  <div key={i} data-pipeline-step={i} data-status={step.status} className="flex items-center gap-3">
                    <div className="w-5 h-5 flex items-center justify-center flex-shrink-0">
                      {step.status === "pending" && (
                        <span className="w-2 h-2 rounded-full bg-gray-300 dark:bg-gray-600" />
                      )}
                      {step.status === "running" && (
                        <span className="w-4 h-4 border-2 border-brand-500 border-t-transparent rounded-full animate-spin" />
                      )}
                      {step.status === "done" && (
                        <svg className="w-5 h-5 text-emerald-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" />
                        </svg>
                      )}
                      {step.status === "error" && (
                        <svg className="w-5 h-5 text-red-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                        </svg>
                      )}
                    </div>
                    <div className="flex-1">
                      <span className={`text-sm ${
                        step.status === "done" ? "text-gray-500 dark:text-gray-400" :
                        step.status === "error" ? "text-red-600 dark:text-red-400" :
                        step.status === "running" ? "text-gray-900 dark:text-gray-100 font-medium" :
                        "text-gray-400 dark:text-gray-500"
                      }`}>
                        {step.label}
                      </span>
                      {step.error && (
                        <p className="text-xs text-red-500 dark:text-red-400 mt-0.5">{step.error}</p>
                      )}
                    </div>
                  </div>
                ))}
                {wizard.pipelineError && (
                  <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3 mt-3">
                    <p className="text-sm text-red-700 dark:text-red-400">{wizard.pipelineError}</p>
                  </div>
                )}
                {wizard.pipeline.every((s) => s.status === "done") && (
                  <div className="flex justify-end pt-2">
                    <button
                      onClick={() => { setWizardOpen(false); fetchData(); }}
                      className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg"
                    >
                      Done
                    </button>
                  </div>
                )}
              </div>
            )}

            <Dialog.Close asChild>
              <button
                className="absolute right-4 top-4 text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300"
                aria-label="Close"
              >
                <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                </svg>
              </button>
            </Dialog.Close>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>

      {/* Section: Pairing Approval */}
      <div>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-4">Approve Pairing</h2>
        <div className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border p-4">
          {runningMachinesWithChannels.length === 0 ? (
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Machine must be running with a channel enabled to approve pairing codes.
            </p>
          ) : (
            <form onSubmit={handlePairingSubmit} className="space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                <div>
                  <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">Machine</label>
                  <select
                    value={pairingMachineId}
                    onChange={(e) => {
                      setPairingMachineId(e.target.value);
                      setPairingChannel("");
                      setPairingResult(null);
                    }}
                    className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
                  >
                    <option value="">Select machine</option>
                    {runningMachinesWithChannels.map((ms) => (
                      <option key={ms.machine.id} value={ms.machine.id}>
                        {ms.machine.name}
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">Channel</label>
                  <select
                    value={pairingChannel}
                    onChange={(e) => { setPairingChannel(e.target.value); setPairingResult(null); }}
                    disabled={!pairingMachineId}
                    className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm disabled:opacity-50"
                  >
                    <option value="">Select channel</option>
                    {pairingMachineId && runningMachinesWithChannels
                      .find((ms) => ms.machine.id === pairingMachineId)
                      ?.channels.filter((c) => c.enabled)
                      .map((c) => (
                        <option key={c.channelId} value={c.channelId}>
                          {CHANNELS.find((ch) => ch.id === c.channelId)?.label}
                        </option>
                      ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">Pairing Code</label>
                  <input
                    type="text"
                    value={pairingCode}
                    onChange={(e) => { setPairingCode(e.target.value.toUpperCase()); setPairingResult(null); }}
                    placeholder="ABCD1234"
                    maxLength={8}
                    className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm font-mono tracking-wider uppercase"
                  />
                </div>
              </div>
              <div className="flex items-center gap-3">
                <button
                  type="submit"
                  disabled={!pairingMachineId || !pairingChannel || pairingCode.length < 4 || pairingSubmitting}
                  className="px-4 py-1.5 text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 rounded-lg disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  {pairingSubmitting ? "Approving..." : "Approve"}
                </button>
                {pairingResult && (
                  <span className={`text-sm ${
                    pairingResult.type === "success"
                      ? "text-emerald-600 dark:text-emerald-400"
                      : "text-red-600 dark:text-red-400"
                  }`}>
                    {pairingResult.message}
                  </span>
                )}
              </div>
            </form>
          )}
        </div>
      </div>
    </div>
  );
}

// --- Helpers ---

function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400",
    stopped: "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400",
    provisioning: "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400",
    error: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400",
  };
  return (
    <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium uppercase tracking-wide ${colors[status] || colors.stopped}`}>
      {status}
    </span>
  );
}
