import type { User, Account, AccountMember, AccountInvitation, Machine, MachineKind, MachineSize, RegionOption, FileEntry, MachineCredential, CredentialProvider, AccountUsage, MachineUsageDetail, MachineBudgetResponse, ModelEntry, UsageBreakdown, ProviderCatalogEntry, OpikTraceListResponse, OpikTraceDetail, OpikFeedbackScore, VMMetricsResponse, ChannelCatalogEntry, OpenAIAgentRuntime } from "./types";
import { ApiError } from "./errors";

// In production VITE_API_URL is set at build time (e.g. "https://api.openclawmachines.com/api").
// In dev the vite proxy forwards /api to localhost:8080.
const BASE = import.meta.env.VITE_API_URL || "/api";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(init?.headers as Record<string, string>),
  };
  // ocm_token cookie is sent automatically via credentials: "include"
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    credentials: "include",
    headers,
  });
  if (!res.ok) {
    // Expired or invalid token — force logout immediately
    if (res.status === 401 && !path.startsWith("/auth/")) {
      window.location.href = "/signed-out";
      throw new ApiError("Session expired", "unauthorized", 401, false);
    }
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new ApiError(
      body.error || res.statusText,
      body.code || "unknown",
      res.status,
      res.status === 429 || res.status >= 500,
    );
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}


// Waitlist (public, no auth)
export const joinWaitlist = async (email: string, source = "landing") => {
  const result = await request<{ status: string }>("/waitlist", {
    method: "POST",
    body: JSON.stringify({ email, source }),
  });
  // Track conversion in GA4
  if (typeof window.gtag === "function") {
    window.gtag("event", "sign_up", { method: "waitlist", source: source });
  }
  return result;
};

export const updateWaitlistSurvey = (email: string, survey: Record<string, unknown>) =>
  request<{ status: string }>("/waitlist", {
    method: "PUT",
    body: JSON.stringify({ email, survey }),
  });

// Auth
export const authMe = () =>
  request<{ user: User }>("/auth/me");

// Machine token (for direct VM access)
export const getMachineToken = (accountId: number, machineId: string) =>
  request<{ token: string; expires_at: string; hostname: string }>(
    `/accounts/${accountId}/machines/${machineId}/token`
  );

// Accounts
export const listAccounts = () => request<Account[]>("/accounts");
export const createAccount = (data: { name: string; slug: string }) =>
  request<Account>("/accounts", { method: "POST", body: JSON.stringify(data) });
export const getAccount = (accountId: number) =>
  request<Account>(`/accounts/${accountId}`);
export const updateAccount = (accountId: number, data: { name: string }) =>
  request<Account>(`/accounts/${accountId}`, { method: "PATCH", body: JSON.stringify(data) });
export const listMembers = (accountId: number) =>
  request<AccountMember[]>(`/accounts/${accountId}/members`);

// Invitations (account-scoped)
export const createInvitation = (accountId: number, data: { email: string; role: string }) =>
  request<AccountInvitation & { token: string }>(`/accounts/${accountId}/invitations`, {
    method: "POST", body: JSON.stringify(data),
  });
export const listInvitations = (accountId: number) =>
  request<AccountInvitation[]>(`/accounts/${accountId}/invitations`);
export const revokeInvitation = (accountId: number, invitationId: number) =>
  request<void>(`/accounts/${accountId}/invitations/${invitationId}`, { method: "DELETE" });

// Invitations (user-scoped)
export const listPendingInvitations = () =>
  request<AccountInvitation[]>("/invitations/pending");
export const getInvitation = (token: string) =>
  request<{
    id: number; account_id: number; account_name: string;
    email: string; role: string; status: string;
    inviter_email: string; expires_at: string;
  }>(`/invitations/${token}`);
export const getInvitationPublic = (token: string) =>
  request<{
    account_name: string; role: string; status: string;
    inviter_email: string; expires_at: string;
  }>(`/invitations/${token}/public`);
export const acceptInvitation = (token: string) =>
  request<{ account_id: number; role: string }>(`/invitations/${token}/accept`, { method: "POST" });
export const declineInvitation = (token: string) =>
  request<void>(`/invitations/${token}/decline`, { method: "POST" });

// Members
export const updateMemberRole = (accountId: number, userId: number, role: string) =>
  request<{ role: string }>(`/accounts/${accountId}/members/${userId}/role`, {
    method: "PUT", body: JSON.stringify({ role }),
  });
export const removeMember = (accountId: number, userId: number) =>
  request<void>(`/accounts/${accountId}/members/${userId}`, { method: "DELETE" });
export const leaveAccount = (accountId: number) =>
  request<void>(`/accounts/${accountId}/members/leave`, { method: "POST" });

// Machines (scoped to account)
export const listMachines = (accountId: number) =>
  request<Machine[]>(`/accounts/${accountId}/machines`);
export const getMachine = (accountId: number, id: string) =>
  request<Machine>(`/accounts/${accountId}/machines/${id}`);

// VM resource metrics. Two query forms: range (from+to) for History, cursor
// (since) for Live. Server returns 1s or 1m resolution depending on range.
export const getMachineMetrics = (
  accountId: number,
  machineId: string,
  params: { from?: string; to?: string; since?: string },
) => {
  const qs = new URLSearchParams();
  if (params.since) qs.set("since", params.since);
  if (params.from) qs.set("from", params.from);
  if (params.to) qs.set("to", params.to);
  const suffix = qs.toString() ? `?${qs}` : "";
  return request<VMMetricsResponse>(
    `/accounts/${accountId}/machines/${machineId}/metrics${suffix}`,
  );
};

export const getBrowserVMMetrics = (
  accountId: number,
  browserVmId: string,
  params: { from?: string; to?: string; since?: string },
) => {
  const qs = new URLSearchParams();
  if (params.since) qs.set("since", params.since);
  if (params.from) qs.set("from", params.from);
  if (params.to) qs.set("to", params.to);
  const suffix = qs.toString() ? `?${qs}` : "";
  return request<VMMetricsResponse>(
    `/accounts/${accountId}/browser-vms/${browserVmId}/metrics${suffix}`,
  );
};
export const listSizes = () => request<MachineSize[]>("/sizes");
export const listRegions = () => request<RegionOption[]>("/regions");
export const createMachine = (
  accountId: number,
  data: {
    name: string;
    kind?: MachineKind;
    size: string;
    preferred_region?: string;
    rootfs_version?: string;
    openclaw_version?: string;
    hermes_version?: string;
    channel?: "stable" | "rc" | "dev";
    runtime_source?: "artifact";
    auto_start?: boolean;
    secrets?: Record<string, string>;
  }
) =>
  request<Machine & { start_error?: string }>(
    `/accounts/${accountId}/machines`,
    { method: "POST", body: JSON.stringify(data) }
  );
export const updateMachine = (accountId: number, id: string, data: Partial<Machine>) =>
  request<Machine>(`/accounts/${accountId}/machines/${id}`, { method: "PUT", body: JSON.stringify(data) });
export const deleteMachine = (accountId: number, id: string) =>
  request<void>(`/accounts/${accountId}/machines/${id}`, { method: "DELETE" });
export const startMachine = (accountId: number, id: string) =>
  request<{ status: string; host_id: number; vm_ip: string }>(`/accounts/${accountId}/machines/${id}/start`, { method: "POST" });
export const stopMachine = (accountId: number, id: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${id}/stop`, { method: "POST" });

// OpenClaw version management
import type { Backup, OpenClawChangeResponse, RootfsChangeResponse, BrowserVM } from "./types";

export interface OpenClawRelease {
  version: string;
  exact_version: string;
  channel: string;
  created_at: string;
}

export interface RuntimeRelease {
  version: string;
  exact_version: string;
  channel: string;
  created_at: string;
}

const releaseQuery = (channel: string, kind?: MachineKind) => {
  const params = new URLSearchParams({ channel });
  if (kind) params.set("kind", kind);
  return params.toString();
};

export const listOpenClawReleases = (accountId: number, channel = "stable", kind?: MachineKind) =>
  request<RuntimeRelease[]>(`/accounts/${accountId}/openclaw/releases?${releaseQuery(channel, kind)}`);

export const listRootfsReleases = (accountId: number, channel = "stable", kind?: MachineKind) =>
  request<RuntimeRelease[]>(`/accounts/${accountId}/rootfs/releases?${releaseQuery(channel, kind)}`);

export const upgradeMachineOpenClaw = (
  accountId: number,
  machineId: string,
  version: string,
  applyNow = false,
  runtimeSource = "artifact",
) =>
  request<OpenClawChangeResponse>(
    `/accounts/${accountId}/machines/${machineId}/openclaw/upgrade`,
    {
      method: "POST",
      body: JSON.stringify({ version, apply_now: applyNow, runtime_source: runtimeSource }),
    },
  );

export const upgradeMachineRuntime = (
  accountId: number,
  machineId: string,
  rootfsVersion: string,
  openclawVersion: string,
  runtimeSource = "artifact",
) =>
  request<RootfsChangeResponse>(
    `/accounts/${accountId}/machines/${machineId}/runtime/upgrade`,
    {
      method: "POST",
      body: JSON.stringify({
        rootfs_version: rootfsVersion,
        openclaw_version: openclawVersion,
        runtime_source: runtimeSource,
      }),
    },
  );

export const upgradeMachineRootfs = (
  accountId: number,
  machineId: string,
  version: string,
  applyNow = false,
  runtimeSource = "artifact",
) =>
  request<RootfsChangeResponse>(
    `/accounts/${accountId}/machines/${machineId}/rootfs/upgrade`,
    {
      method: "POST",
      body: JSON.stringify({ version, apply_now: applyNow, runtime_source: runtimeSource }),
    },
  );

export const rollbackMachineRootfs = (
  accountId: number,
  machineId: string,
  version: string,
  applyNow = false,
  runtimeSource = "artifact",
) =>
  request<RootfsChangeResponse>(
    `/accounts/${accountId}/machines/${machineId}/rootfs/rollback`,
    {
      method: "POST",
      body: JSON.stringify({ version, apply_now: applyNow, runtime_source: runtimeSource }),
    },
  );

export const rollbackMachineOpenClaw = (
  accountId: number,
  machineId: string,
  version: string,
  applyNow = false,
  runtimeSource = "artifact",
) =>
  request<OpenClawChangeResponse>(
    `/accounts/${accountId}/machines/${machineId}/openclaw/rollback`,
    {
      method: "POST",
      body: JSON.stringify({ version, apply_now: applyNow, runtime_source: runtimeSource }),
    },
  );

// Backups (per-machine)

export const listMachineBackups = (accountId: number, machineId: string) =>
  request<Backup[]>(`/accounts/${accountId}/machines/${machineId}/backups`);

export const createMachineBackup = (accountId: number, machineId: string) =>
  request<Backup>(`/accounts/${accountId}/machines/${machineId}/backups`, {
    method: "POST",
  });

export const restoreMachineBackup = (accountId: number, machineId: string, backupId: number) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/backups/${backupId}/restore`, {
    method: "POST",
  });

export const deleteMachineBackup = (accountId: number, machineId: string, backupId: number) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/backups/${backupId}`, {
    method: "DELETE",
  });

export const downloadMachineBackup = async (accountId: number, machineId: string, backupId: number, format = "tar.gz") => {
  let res = await fetch(`${BASE}/accounts/${accountId}/machines/${machineId}/backups/${backupId}/download?format=${format}`, {
    credentials: "include",
  });
  // If tar.gz fails with 400 (host unavailable), fall back to ext4 (direct GCS)
  if (!res.ok && res.status === 400 && format === "tar.gz") {
    format = "ext4";
    res = await fetch(`${BASE}/accounts/${accountId}/machines/${machineId}/backups/${backupId}/download?format=${format}`, {
      credentials: "include",
    });
  }
  if (!res.ok) throw new Error(`Download failed: ${res.statusText}`);

  const filename = `${machineId}-backup-${backupId}.${format === "ext4" ? "ext4" : "tar.gz"}`;

  // Stream to disk via File System Access API (Chrome/Edge 86+)
  if ("showSaveFilePicker" in window) {
    let handle;
    try {
      handle = await (window as any).showSaveFilePicker({
        suggestedName: filename,
      });
    } catch (e: any) {
      if (e?.name === "AbortError") return; // user cancelled save dialog
      throw e;
    }
    const writable = await handle.createWritable();
    try {
      await res.body!.pipeTo(writable);
    } catch (e) {
      await writable.abort();
      throw e;
    }
    return;
  }

  // Fallback: buffer as blob (Firefox/Safari — no streaming download API available)
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
};

// Secrets (per-machine)
export interface SecretEntry {
  id: number;
  machine_id: string;
  key: string;
  created_at: string;
  updated_at: string;
}

export const listSecrets = (accountId: number, machineId: string) =>
  request<SecretEntry[]>(`/accounts/${accountId}/machines/${machineId}/secrets`);
export const setSecret = (accountId: number, machineId: string, key: string, value: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/secrets/${key}`, {
    method: "PUT",
    body: JSON.stringify({ value }),
  });
export const deleteSecret = (accountId: number, machineId: string, key: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/secrets/${key}`, { method: "DELETE" });

// Machine Credentials (directly scoped to machines, no account-wide pool)
export const listMachineCredentials = (accountId: number, machineId: string) =>
  request<MachineCredential[]>(`/accounts/${accountId}/machines/${machineId}/credentials`);
export const putMachineCredential = (accountId: number, machineId: string, provider: CredentialProvider, data: { value: string; credential_type?: string; label: string }) =>
  request<MachineCredential>(`/accounts/${accountId}/machines/${machineId}/credentials/${provider}`, {
    method: "PUT",
    body: JSON.stringify(data),
  });
export const deleteMachineCredential = (accountId: number, machineId: string, provider: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/credentials/${provider}`, { method: "DELETE" });
export const testMachineCredential = (accountId: number, machineId: string, provider: string) =>
  request<{ ok: boolean; error?: string; expires_in_hours?: number }>(`/accounts/${accountId}/machines/${machineId}/credentials/${provider}/test`, { method: "POST" });

// Provider catalog
export const listProviderCatalog = (category?: string) => {
  const params = category ? `?category=${category}` : "";
  return request<ProviderCatalogEntry[]>(`/catalog/providers${params}`);
};

// Search provider override
export const getSearchProvider = (accountId: number, machineId: string) =>
  request<{ provider: string; resolved: string }>(`/accounts/${accountId}/machines/${machineId}/search-provider`);

export const setSearchProvider = (accountId: number, machineId: string, provider: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/search-provider`, {
    method: "PUT",
    body: JSON.stringify({ provider }),
  });

export const deleteSearchProvider = (accountId: number, machineId: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/search-provider`, {
    method: "DELETE",
  });

// Files (per-machine)
export const listFiles = (accountId: number, machineId: string, path: string = "/") =>
  request<FileEntry[]>(`/accounts/${accountId}/machines/${machineId}/files?path=${encodeURIComponent(path)}`);

// Data plane URL helpers
//
// In production, data plane traffic goes through subdomain routing:
//   https://{accountSlug}.openclawmachines.com/{machineSlug}/{path}
//
// In dev (localhost), we fall back to the control plane proxy using numeric IDs,
// because the backend's AccountMiddleware expects integer accountId, not slugs.

const DATA_PLANE_DOMAIN = import.meta.env.VITE_DATA_PLANE_DOMAIN || "openclawmachines.com";

/** Check if running in development mode (localhost) */
function isDev(): boolean {
  return window.location.hostname === "localhost";
}

/** Get API host for WebSocket connections */
function getApiHost(): string {
  const apiUrl = import.meta.env.VITE_API_URL;
  if (apiUrl) {
    try { return new URL(apiUrl).host; } catch { /* fall through */ }
  }
  return window.location.host;
}

interface DataPlaneParams {
  accountSlug: string | undefined;
  machineSlug: string | undefined;
  path: string;
  accountId?: number;
  machineId?: string;
}

/**
 * Build a data plane URL (HTTP or WebSocket).
 * Production: subdomain routing via Cloudflare Worker
 * Dev: control plane proxy with numeric IDs
 */
function buildDataPlaneUrl(params: DataPlaneParams, protocol: "https" | "wss"): string {
  const { accountSlug, machineSlug, path, accountId, machineId } = params;

  // Production: subdomain routing
  if (accountSlug && machineSlug && !isDev()) {
    return `${protocol}://${accountSlug}.${DATA_PLANE_DOMAIN}/${machineSlug}/${path}`;
  }

  // Dev: fall back to control plane proxy with numeric IDs
  const aId = accountId ?? accountSlug;
  const mId = machineId ?? machineSlug;

  if (protocol === "wss") {
    return `ws://${getApiHost()}/api/accounts/${aId}/machines/${mId}/${path}`;
  }
  return `${BASE}/accounts/${aId}/machines/${mId}/${path}`;
}

/** Build an HTTP data plane URL */
export function dataPlaneUrl(
  accountSlug: string | undefined,
  machineSlug: string | undefined,
  path: string,
  accountId?: number,
  machineId?: string,
): string {
  return buildDataPlaneUrl({ accountSlug, machineSlug, path, accountId, machineId }, "https");
}

/** Build a WebSocket data plane URL */
export function dataPlaneWsUrl(
  accountSlug: string | undefined,
  machineSlug: string | undefined,
  path: string,
  accountId?: number,
  machineId?: string,
): string {
  return buildDataPlaneUrl({ accountSlug, machineSlug, path, accountId, machineId }, "wss");
}

// Gateway health & restart (data plane)
export interface GatewayHealthResponse {
  gateway: "running" | "crash-loop" | "unknown" | "unreachable";
}

export async function getGatewayHealth(
  accountSlug: string | undefined,
  machineSlug: string | undefined,
  accountId?: number,
  machineId?: string,
): Promise<GatewayHealthResponse> {
  const url = dataPlaneUrl(accountSlug, machineSlug, "gateway-health", accountId, machineId);
  const res = await fetch(url, { credentials: "include" });
  if (!res.ok) return { gateway: "unreachable" };
  return res.json();
}

export async function restartGateway(
  accountSlug: string | undefined,
  machineSlug: string | undefined,
  accountId?: number,
  machineId?: string,
): Promise<{ status: string }> {
  const url = dataPlaneUrl(accountSlug, machineSlug, "restart-gateway", accountId, machineId);
  const res = await fetch(url, { method: "POST", credentials: "include" });
  if (!res.ok) throw new Error("Failed to restart gateway");
  return res.json();
}

// Usage & Billing
export const getAccountUsage = (accountId: number) =>
  request<AccountUsage>(`/accounts/${accountId}/usage`);
export const getMachineUsage = (accountId: number, machineId: string, since?: string) => {
  const params = since ? `?since=${encodeURIComponent(since)}` : "";
  return request<MachineUsageDetail>(`/accounts/${accountId}/machines/${machineId}/usage${params}`);
};
export const getMachineUsageBreakdown = (
  accountId: number,
  machineId: string,
  period: "hour" | "day",
  since?: string,
) => {
  const params = new URLSearchParams({ period });
  if (since) params.set("since", since);
  return request<UsageBreakdown>(`/accounts/${accountId}/machines/${machineId}/usage/breakdown?${params}`);
};
export const getMachineTraces = (accountId: number, machineId: string, since?: string, limit = 50) => {
  const params = new URLSearchParams({ limit: String(limit) });
  if (since) params.set("since", since);
  return request<OpikTraceListResponse>(`/accounts/${accountId}/machines/${machineId}/traces?${params}`);
};
export const getMachineTrace = (accountId: number, machineId: string, traceId: string) =>
  request<OpikTraceDetail>(`/accounts/${accountId}/machines/${machineId}/traces/${traceId}`);

export interface AccountTraceFilters {
  since?: string;
  limit?: number;
  q?: string;
  machine_id?: string;
  project?: string;
  status?: "ok" | "error" | "";
  thread_id?: string;
  feedback?: "reviewed" | "unreviewed" | "low" | "";
  max_feedback_score?: number;
  tags?: string[];
  min_tokens?: number;
  min_cost?: number;
  min_duration_ms?: number;
}

export const getAccountTraces = (accountId: number, filters: AccountTraceFilters = {}) => {
  const params = new URLSearchParams({ limit: String(filters.limit ?? 50) });
  if (filters.since) params.set("since", filters.since);
  if (filters.q) params.set("q", filters.q);
  if (filters.machine_id) params.set("machine_id", filters.machine_id);
  if (filters.project) params.set("project", filters.project);
  if (filters.status) params.set("status", filters.status);
  if (filters.thread_id) params.set("thread_id", filters.thread_id);
  if (filters.feedback) params.set("feedback", filters.feedback);
  for (const tag of filters.tags ?? []) {
    if (tag.trim()) params.append("tag", tag.trim());
  }
  if (filters.max_feedback_score !== undefined) params.set("max_feedback_score", String(filters.max_feedback_score));
  if (filters.min_tokens !== undefined) params.set("min_tokens", String(filters.min_tokens));
  if (filters.min_cost !== undefined) params.set("min_cost", String(filters.min_cost));
  if (filters.min_duration_ms !== undefined) params.set("min_duration_ms", String(filters.min_duration_ms));
  return request<OpikTraceListResponse>(`/accounts/${accountId}/traces?${params}`);
};

export const getAccountTrace = (accountId: number, traceId: string) =>
  request<OpikTraceDetail>(`/accounts/${accountId}/traces/${traceId}`);

export const updateTraceTags = (accountId: number, traceId: string, tags: string[]) =>
  request<void>(`/accounts/${accountId}/traces/${traceId}/tags`, {
    method: "PUT",
    body: JSON.stringify({ tags }),
  });

export const createTraceFeedback = (
  accountId: number,
  traceId: string,
  data: { name: string; value: number; reason?: string; span_id?: string },
) =>
  request<OpikFeedbackScore>(`/accounts/${accountId}/traces/${traceId}/feedback`, {
    method: "POST",
    body: JSON.stringify(data),
  });
export const setMachineBudget = (accountId: number, machineId: string, limitCents: number) =>
  request<MachineBudgetResponse>(`/accounts/${accountId}/machines/${machineId}/budget`, {
    method: "PUT",
    body: JSON.stringify({ limit_cents: limitCents }),
  });
export const deleteMachineBudget = (accountId: number, machineId: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/budget`, { method: "DELETE" });

// Registry (catalog of available channels, skills, tools)
export interface RegistryEntry {
  id: string;
  type: string;
  name: string;
  description?: string;
  version: string;
  tier: string;
  status: string;
  sort_order: number;
  required_credentials?: string[];
}

export const listRegistrySkills = () =>
  request<RegistryEntry[]>(`/registry/skills`);

// Machine Capabilities (channel/skill/tool management)
export interface MachineCapability {
  id: number;
  machine_id: string;
  entry_id: string;
  mode?: string;
  enabled: boolean;
  config_overrides?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  entry_name?: string;
}

export const listMachineCapabilities = (accountId: number, machineId: string) =>
  request<MachineCapability[]>(`/accounts/${accountId}/machines/${machineId}/capabilities`);

export const enableMachineCapability = (accountId: number, machineId: string, entryId: string, mode?: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/capabilities`, {
    method: "POST",
    body: JSON.stringify({ entry_id: entryId, mode }),
  });

export const disableMachineCapability = (accountId: number, machineId: string, entryId: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/capabilities/${entryId}`, { method: "DELETE" });

export const updateMachineCapabilityOverrides = (accountId: number, machineId: string, entryId: string, overrides: Record<string, unknown>) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/capabilities/${entryId}`, {
    method: "PUT",
    body: JSON.stringify({ config_overrides: overrides }),
  });

// Machine Plugins (plugin catalog + per-machine plugin management)
export interface PluginCatalogEntry {
  id: string;
  name: string;
  description?: string;
  slot: string;
  version: string;
  install_kind: string;
  status: string;
  sort_order: number;
  config_template?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface MachinePlugin {
  id: string;
  machine_id: string;
  plugin_id: string;
  slot: string;
  enabled: boolean;
  config_overrides?: Record<string, unknown>;
  install_status: string;
  installed_version?: string;
  installed_at?: string;
  created_at: string;
  updated_at: string;
}

export const listPluginCatalog = () =>
  request<PluginCatalogEntry[]>(`/admin/plugins`);

export const listMachinePlugins = (accountId: number, machineId: string) =>
  request<MachinePlugin[]>(`/accounts/${accountId}/machines/${machineId}/plugins`);

export const enableMachinePlugin = (accountId: number, machineId: string, pluginId: string, configOverrides?: Record<string, unknown>) =>
  request<{ status: string; reconcile_warning?: string }>(`/accounts/${accountId}/machines/${machineId}/plugins`, {
    method: "POST",
    body: JSON.stringify({ plugin_id: pluginId, config_overrides: configOverrides }),
  });

export const disableMachinePlugin = (accountId: number, machineId: string, pluginId: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/plugins/${pluginId}`, { method: "DELETE" });

// ---- Integrations (Composio) ----

export interface Integration {
  id: string;
  name: string;
  icon: string;
  category: string;
  connected: boolean;
  connected_account_id?: string;
  connected_at?: string;
}

export const listIntegrations = (accountId: number, machineId: string) =>
  request<Integration[]>(`/accounts/${accountId}/machines/${machineId}/integrations`);

export const createConnectLink = (accountId: number, machineId: string, integration: string) =>
  request<{ url: string }>(`/accounts/${accountId}/machines/${machineId}/integrations/${integration}/connect`, { method: "POST" });

export const deleteIntegration = (accountId: number, machineId: string, connId: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/integrations/${connId}`, { method: "DELETE" });

// Gateway WebSocket RPC — opens a one-shot WebSocket through the gateway proxy,
// sends a JSON-RPC call, waits for the result, and closes.
export function gatewayRPC(
  accountId: number,
  machineId: string,
  method: string,
  params: Record<string, unknown>,
): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const wsUrl = `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${getApiHost()}/api/accounts/${accountId}/machines/${machineId}/gateway/`;
    const ws = new WebSocket(wsUrl);
    const timeout = setTimeout(() => {
      ws.close();
      reject(new ApiError("Gateway RPC timed out", "rpc_timeout", 0, true));
    }, 15000);

    ws.onopen = () => {
      ws.send(JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }));
    };
    ws.onmessage = (event) => {
      clearTimeout(timeout);
      try {
        const data = JSON.parse(event.data);
        if (data.error) {
          reject(new ApiError(data.error.message || "RPC error", "rpc_error", 0, false));
        } else {
          resolve(data.result);
        }
      } catch {
        reject(new ApiError("Invalid RPC response", "rpc_parse_error", 0, false));
      }
      ws.close();
    };
    ws.onerror = () => {
      clearTimeout(timeout);
      reject(new ApiError("WebSocket connection failed", "ws_connect_failed", 0, true));
    };
    ws.onclose = (event) => {
      clearTimeout(timeout);
      if (event.code !== 1000 && event.code !== 1005) {
        reject(new ApiError(`WebSocket closed: ${event.reason || event.code}`, "ws_closed", 0, true));
      }
    };
  });
}

// Structured exec — runs an openclaw CLI command inside the VM and returns
// exit code, stdout, stderr as JSON.
export interface ExecResult {
  exit_code: number;
  stdout: string;
  stderr: string;
}

export const machineExec = (accountId: number, machineId: string, command: string[]) =>
  request<ExecResult>(`/accounts/${accountId}/machines/${machineId}/exec`, {
    method: "POST",
    body: JSON.stringify({ command }),
  });

export const getMachineModel = (accountId: number, machineId: string) =>
  request<{ model: string; openai_agent_runtime?: OpenAIAgentRuntime; fallbacks?: string[] }>(
    `/accounts/${accountId}/machines/${machineId}/model`
  );

export const setMachineModel = (
  accountId: number,
  machineId: string,
  model: string,
  openaiAgentRuntime?: OpenAIAgentRuntime,
  fallbacks?: string[],
) =>
  request<{ status: string; model: string; openai_agent_runtime?: OpenAIAgentRuntime; fallbacks?: string[] }>(`/accounts/${accountId}/machines/${machineId}/model`, {
    method: "PUT",
    body: JSON.stringify({
      model,
      ...(openaiAgentRuntime ? { openai_agent_runtime: openaiAgentRuntime } : {}),
      ...(fallbacks ? { fallbacks } : {}),
    }),
  });

export const listModels = (kind?: MachineKind) => {
  const suffix = kind ? `?kind=${kind}` : "";
  return request<ModelEntry[]>(`/models${suffix}`);
};

export const listChannels = (kind?: MachineKind) => {
  const suffix = kind ? `?kind=${kind}` : "";
  return request<ChannelCatalogEntry[]>(`/channels${suffix}`);
};

export const listMachineModels = (accountId: number, machineId: string) =>
  request<ModelEntry[]>(`/accounts/${accountId}/machines/${machineId}/models`);

export const getMachineAssembledConfig = (accountId: number, machineId: string) =>
  request<Record<string, unknown>>(`/accounts/${accountId}/machines/${machineId}/assembled-config`);

export interface PushConfigResponse {
  status: string;
  config_version: number;
  live_update: "sent" | "failed" | "not_running" | "skipped";
  live_update_error?: string;
  warnings?: string[];
}

export const pushMachineConfig = (accountId: number, machineId: string) =>
  request<PushConfigResponse>(`/accounts/${accountId}/machines/${machineId}/config/push`, {
    method: "POST",
  });

// Channel state machine transitions
export interface ChannelResponse {
  status: string;
  channel: string;
  live_update: "sent" | "failed" | "not_running" | "skipped";
  live_update_error?: string;
}

export const connectChannel = (accountId: number, machineId: string, channel: string, data: { token: string; app_token?: string; settings?: Record<string, unknown> }) =>
  request<ChannelResponse>(`/accounts/${accountId}/machines/${machineId}/channels/${channel}/connect`, {
    method: "POST",
    body: JSON.stringify(data),
  });

export const disconnectChannel = (accountId: number, machineId: string, channel: string) =>
  request<ChannelResponse>(`/accounts/${accountId}/machines/${machineId}/channels/${channel}/disconnect`, {
    method: "POST",
  });

export const updateChannelSettings = (accountId: number, machineId: string, channel: string, settings: Record<string, unknown>) =>
  request<ChannelResponse>(`/accounts/${accountId}/machines/${machineId}/channels/${channel}/settings`, {
    method: "PUT",
    body: JSON.stringify({ settings }),
  });

export const updateChannelToken = (accountId: number, machineId: string, channel: string, token: string) =>
  request<ChannelResponse>(`/accounts/${accountId}/machines/${machineId}/channels/${channel}/token`, {
    method: "PUT",
    body: JSON.stringify({ token }),
  });

export const setMachineIdentity = (accountId: number, machineId: string, data: { name?: string; avatar?: string }) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/identity`, {
    method: "PUT",
    body: JSON.stringify(data),
  });

// Agents (personas per machine)
export interface MachineAgent {
  id: string;
  machine_id: string;
  agent_id: string;
  name: string;
  model?: string;
  identity_emoji?: string;
  identity_avatar?: string;
  soul?: string;
  is_default: boolean;
  sort_order: number;
  created_at: string;
  updated_at: string;
}

export interface CreateAgentRequest {
  agent_id: string;
  name: string;
  model?: string;
  identity_emoji?: string;
  identity_avatar?: string;
  soul?: string;
  is_default?: boolean;
  sort_order?: number;
}

export const listMachineAgents = (accountId: number, machineId: string) =>
  request<MachineAgent[]>(`/accounts/${accountId}/machines/${machineId}/agents`);

export const createMachineAgent = (accountId: number, machineId: string, data: CreateAgentRequest) =>
  request<MachineAgent>(`/accounts/${accountId}/machines/${machineId}/agents`, {
    method: "POST",
    body: JSON.stringify(data),
  });

export const updateMachineAgent = (accountId: number, machineId: string, agentId: string, data: Partial<CreateAgentRequest>) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/agents/${agentId}`, {
    method: "PUT",
    body: JSON.stringify(data),
  });

export const deleteMachineAgent = (accountId: number, machineId: string, agentId: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/agents/${agentId}`, {
    method: "DELETE",
  });

// Hosts (admin)
export interface Host {
  id: number;
  vm_name: string;
  external_ip?: string;
  internal_ip?: string;
  tunnel_url?: string;
  status: string;
  status_message?: string;
  machine_type: string;
  zone: string;
  region: string;
  source_image?: string;
  capacity_vcpus: number;
  capacity_memory_mb: number;
  used_vcpus: number;
  used_memory_mb: number;
  machine_count: number;
  agent_version?: string;
  openclaw_version?: string;
  rootfs_version?: string;
  browser_rootfs_version?: string;
  last_heartbeat?: string;
  created_at: string;
  provider?: string;
  provider_class?: string;
  lifecycle_mode?: string;
  agent_endpoint?: string;
  host_pool?: string;
  maintenance_mode?: boolean;
  capabilities?: {
    browser_storage?: {
      state_dir?: string;
      browser_state_dir?: string;
      mount_point?: string;
      fs_type?: string;
      reflink_supported?: boolean;
      reflink_error?: string;
    };
  };
}

// Enrollment
export interface EnrollmentToken {
  id: string;
  token: string;
  provider: string;
  provider_class: string;
  labels: Record<string, string>;
  used_by_host_id?: number;
  expires_at: string;
  created_at: string;
}

export const createEnrollmentToken = (provider: string, expiresInHours = 24) =>
  request<{ token: string; expires_at: string; install_command: string }>("/admin/hosts/enrollment-tokens", {
    method: "POST",
    body: JSON.stringify({ provider, provider_class: "registered", expires_in_hours: expiresInHours }),
  });

export const listEnrollmentTokens = () =>
  request<EnrollmentToken[]>("/admin/hosts/enrollment-tokens");

// Machine summary for admin view
export interface HostMachine {
  id: string;
  name: string;
  slug: string;
  status: string;
  vm_ip?: string;
  data_volume_gb: number;
  rootfs_snapshot?: string;
  openclaw_version?: string;
  created_at: string;
  started_at?: string;
}

export const provisionHost = (machineType?: string, zone?: string) =>
  request<{ status: string; message: string }>("/admin/hosts", {
    method: "POST",
    body: JSON.stringify({
      machine_type: machineType || "n2-standard-2",
      zone: zone || "",
    }),
  });
export const listHosts = () => request<Host[]>("/admin/hosts");
export const destroyHost = (id: number) =>
  request<{ status: string; message: string }>(`/admin/hosts/${id}`, { method: "DELETE" });
export const listHostMachines = (hostId: number) =>
  request<HostMachine[]>(`/admin/hosts/${hostId}/machines`);

export interface VMDiskStats {
  machine_id: string;
  disk_total_mb: number;
  disk_used_mb: number;
}

export const getHostVMStats = (hostId: number) =>
  request<VMDiskStats[]>(`/admin/hosts/${hostId}/vm-stats`);
export const refreshHostRootfs = (hostId: number) =>
  request<{ status: string; message: string }>(`/admin/hosts/${hostId}/refresh-rootfs`, { method: "POST" });
export const triggerHostUpdate = (hostId: number) =>
  request<{ status: string; message: string }>(`/admin/hosts/${hostId}/trigger-update`, { method: "POST" });
export const drainHostUpdate = (hostId: number) =>
  request<{ status: string; message: string }>(`/admin/hosts/${hostId}/drain-update`, { method: "POST" });
export const configureHostBrowserStorage = (
  hostId: number,
  body: { device: string; mount_point?: string; format: boolean },
) =>
  request<{ status: string; operation_id: string }>(`/admin/hosts/${hostId}/configure-browser-storage`, {
    method: "POST",
    body: JSON.stringify(body),
  });
export const setHostMaintenanceMode = (hostId: number, enabled: boolean) =>
  request<{ status: string; maintenance_mode: string }>(`/admin/hosts/${hostId}/maintenance`, {
    method: "POST",
    body: JSON.stringify({ enabled }),
  });
export const getHostLogs = (hostId: number, lines = 100) =>
  request<{ logs: string }>(`/admin/hosts/${hostId}/logs?lines=${lines}`);

export interface LatestVersions {
  agent?: { version: string; built_at?: string };
  rootfs?: { version: string; openclaw_version?: string; built_at?: string };
  browser_rootfs?: { version: string; built_at?: string; lineage?: string; stability?: string; manifest_uri?: string };
  experimental_browser_rootfs?: { version: string; built_at?: string; lineage?: string; stability?: string; manifest_uri?: string };
}

export const getLatestVersions = () =>
  request<LatestVersions>("/admin/latest-versions");

export type AdminOpenClawRelease = {
  version: string;
  channel: string;
  created_at: string;
  min_rootfs_version?: string;
};

export const listAdminOpenClawReleases = () =>
  request<{ releases: AdminOpenClawRelease[] }>("/admin/openclaw-releases");

export type AdminRootfsRelease = {
  version: string;
  channel: string;
  created_at: string;
};

export const listAdminRootfsReleases = () =>
  request<{ releases: AdminRootfsRelease[] }>("/admin/rootfs-releases");

export const listAllMachines = () =>
  request<Machine[]>("/admin/machines");

export const adminResetMachine = (machineId: string) =>
  request<{ status: string }>(`/admin/machines/${machineId}/reset`, { method: "POST" });

export const adminStartMachine = (machineId: string) =>
  request<{ status: string; host_id: number; vm_ip: string }>(`/admin/machines/${machineId}/start`, { method: "POST" });

export const adminStopMachine = (machineId: string) =>
  request<{ status: string }>(`/admin/machines/${machineId}/stop`, { method: "POST" });

export const adminClearMigration = (machineId: string) =>
  request<{ status: string }>(`/admin/machines/${machineId}/clear-migration`, { method: "POST" });
export const adminFlagMigration = (machineId: string) =>
  request<{ status: string; message: string }>(`/admin/machines/${machineId}/flag-migration`, { method: "POST" });

export interface MigrateResult {
  status: string;
  machine_id: string;
  target_host_id: number;
  backup_id?: number;
  workflow_id?: string;
}

export interface WorkflowRun<TOutput = unknown> {
  id: string;
  kind: string;
  status: string;
  current_phase?: string;
  output_json?: TOutput;
  summary_json?: unknown;
  error_code?: string;
  error_message?: string;
}

interface QueuedMigrateResult {
  status: string;
  workflow_id: string;
  machine_id: string;
  target_host_id: number;
}

export const getWorkflow = <TOutput = unknown>(workflowId: string) =>
  request<WorkflowRun<TOutput>>(`/workflows/${workflowId}`);

// Admin workflow types
export interface AdminWorkflowRun {
  id: string;
  kind: string;
  scope_type: string;
  scope_id: string;
  status: string;
  current_phase?: string;
  requested_by?: number;
  account_id?: number;
  priority: string;
  input_json?: unknown;
  output_json?: unknown;
  summary_json?: unknown;
  error_code?: string;
  error_message?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
  updated_at: string;
  // Enriched fields
  machine_name?: string;
  machine_slug?: string;
  host_id?: number;
}

export interface WorkflowEvent {
  id: number;
  workflow_id: string;
  phase?: string;
  level: string;
  event_type: string;
  message: string;
  details_json?: unknown;
  created_at: string;
}

export const listAdminWorkflows = (params?: { kind?: string; status?: string; limit?: number }) => {
  const q = new URLSearchParams();
  if (params?.kind) q.set("kind", params.kind);
  if (params?.status) q.set("status", params.status);
  if (params?.limit) q.set("limit", String(params.limit));
  const qs = q.toString();
  return request<AdminWorkflowRun[]>(`/admin/workflows${qs ? `?${qs}` : ""}`);
};

export const listAdminWorkflowEvents = (workflowId: string, limit = 200) =>
  request<WorkflowEvent[]>(`/admin/workflows/${workflowId}/events?limit=${limit}`);

// ---- Admin Events (activity_log) ----

export interface ActivityLogEntry {
  id: number;
  event_id: string;
  category: string;
  action: string;
  status: string;
  actor_type: string;
  actor_id?: number;
  actor_name?: string;
  account_id?: number;
  account_name?: string;
  machine_id?: string;
  machine_name?: string;
  host_id?: number;
  host_name?: string;
  agent_version?: string;
  rootfs_version?: string;
  summary: string;
  detail?: Record<string, unknown>;
  started_at: string;
  duration_ms?: number;
  error_code?: string;
  error_message?: string;
}

export interface ActivityLogResponse {
  events: ActivityLogEntry[];
  next_cursor_time?: string;
  next_cursor_id?: number;
}

export const listAdminEvents = (params?: {
  limit?: number;
  category?: string;
  action?: string;
  status?: string;
  account_id?: number;
  machine_id?: string;
  host_id?: number;
  actor_type?: string;
  cursor_time?: string;
  cursor_id?: number;
}) => {
  const q = new URLSearchParams();
  if (params?.limit) q.set("limit", String(params.limit));
  if (params?.category) q.set("category", params.category);
  if (params?.action) q.set("action", params.action);
  if (params?.status) q.set("status", params.status);
  if (params?.account_id) q.set("account_id", String(params.account_id));
  if (params?.machine_id) q.set("machine_id", params.machine_id);
  if (params?.host_id) q.set("host_id", String(params.host_id));
  if (params?.actor_type) q.set("actor_type", params.actor_type);
  if (params?.cursor_time) q.set("cursor_time", params.cursor_time);
  if (params?.cursor_id) q.set("cursor_id", String(params.cursor_id));
  const qs = q.toString();
  return request<ActivityLogResponse>(`/admin/events${qs ? `?${qs}` : ""}`);
};

// Browser VMs
export const listBrowserVMs = (accountId: number) =>
  request<BrowserVM[]>(`/accounts/${accountId}/browser-vms`);

export const createBrowserVM = (
  accountId: number,
  data: {
    name?: string;
    vcpus?: number;
    memory_mb?: number;
    browser_image?: "default" | "kernel-stable" | "kernel-experimental" | "kernel-latest";
    rootfs_manifest?: string;
    rootfs_version?: string;
  },
) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms`, {
    method: "POST",
    body: JSON.stringify(data),
  });

export const getBrowserVM = (accountId: number, id: string) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms/${id}`);

export const browserVMLiveUrl = (
  accountId: number,
  id: string,
  options?: { width?: number; height?: number; rate?: number },
) => {
  const q = new URLSearchParams();
  if (options?.width) q.set("width", String(options.width));
  if (options?.height) q.set("height", String(options.height));
  if (options?.rate) q.set("rate", String(options.rate));
  const suffix = q.toString();
  return `${BASE}/accounts/${accountId}/browser-vms/${id}/live/${suffix ? `?${suffix}` : ""}`;
};

export const startBrowserVM = (accountId: number, id: string, data?: { host_id?: number; region?: string }) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms/${id}/start`, {
    method: "POST",
    body: JSON.stringify(data ?? {}),
  });

export const stopBrowserVM = (accountId: number, id: string) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms/${id}/stop`, {
    method: "POST",
  });

export const deleteBrowserVM = (accountId: number, id: string) =>
  request<void>(`/accounts/${accountId}/browser-vms/${id}`, {
    method: "DELETE",
  });

export const pairBrowser = (accountId: number, machineId: string, browserVmId: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/pair-browser`, {
    method: "POST",
    body: JSON.stringify({ browser_vm_id: browserVmId }),
  });

export const unpairBrowser = (accountId: number, machineId: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/pair-browser`, {
    method: "DELETE",
  });

export const migrateMachine = async (machineId: string, targetHostId: number, force = false) => {
  const result = await request<MigrateResult | QueuedMigrateResult>("/admin/machines/migrate", {
    method: "POST",
    body: JSON.stringify({ machine_id: machineId, target_host_id: targetHostId, force }),
  });

  // Return immediately — migration runs as a background workflow on the worker fleet.
  // The machine list will reflect status changes via polling.
  return {
    status: "workflow_id" in result && result.workflow_id ? "queued" : "completed",
    machine_id: machineId,
    target_host_id: targetHostId,
    workflow_id: "workflow_id" in result ? result.workflow_id : undefined,
  };
};
