export interface User {
  id: number;
  email: string;
  name: string;
  avatar_url?: string;
  auth_provider: string;
  status?: string;
  cf_sub?: string;
  created_at: string;
}

export interface Account {
  id: number;
  name: string;
  slug: string;
  plan: string;
  billing_email?: string;
  created_by: number;
  created_at: string;
}

export interface AccountMember {
  account_id: number;
  user_id: number;
  role: "owner" | "admin" | "member";
  email?: string;
  name?: string;
  created_at: string;
}

export interface AccountInvitation {
  id: number;
  account_id: number;
  account_name?: string;
  email: string;
  role: "admin" | "member";
  token?: string;
  status: "pending" | "accepted" | "declined" | "expired";
  invited_by: number;
  inviter_email?: string;
  accepted_by?: number;
  expires_at: string;
  created_at: string;
  responded_at?: string;
}

export interface MachineSize {
  id: string;
  label: string;
  vcpus: number;
  memory_mb: number;
  swap_mb: number;
  disk_gb: number;
  price_cents_mo: number;
  description: string;
  is_default?: boolean;
}

export interface RegionOption {
  code: string;
  name: string;
  available: boolean;
}

export interface FileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  modified: string;
}

export type CredentialProvider = "anthropic" | "openai" | "openai-codex" | "google" | "openrouter" | "discord" | "telegram" | "whatsapp" | "slack" | "slack-app";
export type MachineKind = "openclaw" | "hermes";
export type OpenAIAgentRuntime = "auto" | "codex" | "pi";

export type CredentialType = "api_key" | "subscription_key" | "token" | "oauth";

export interface CredentialProviderInfo {
  id: CredentialProvider;
  label: string;
  placeholder: string;
  category: "ai" | "automation" | "other";
  iconLetter: string;
  iconBg: string;
  defaultCredentialType: CredentialType;
}

export const CREDENTIAL_TYPE_LABELS: Record<CredentialType, string> = {
  api_key: "API Key",
  subscription_key: "Subscription Key",
  token: "Token",
  oauth: "OAuth",
};

export const CREDENTIAL_PROVIDERS: CredentialProviderInfo[] = [
  { id: "anthropic", label: "Anthropic Claude", placeholder: "sk-ant-...", category: "ai", iconLetter: "A", iconBg: "bg-amber-600", defaultCredentialType: "api_key" },
  { id: "openai", label: "OpenAI", placeholder: "sk-...", category: "ai", iconLetter: "O", iconBg: "bg-emerald-600", defaultCredentialType: "api_key" },
  { id: "google", label: "Google AI", placeholder: "AIza...", category: "ai", iconLetter: "G", iconBg: "bg-blue-600", defaultCredentialType: "api_key" },
  { id: "openrouter", label: "OpenRouter", placeholder: "sk-or-v1-...", category: "ai", iconLetter: "R", iconBg: "bg-purple-600", defaultCredentialType: "api_key" },
  { id: "discord", label: "Discord Bot", placeholder: "Bot token...", category: "automation", iconLetter: "D", iconBg: "bg-indigo-600", defaultCredentialType: "token" },
  { id: "telegram", label: "Telegram Bot", placeholder: "Bot token from @BotFather...", category: "automation", iconLetter: "T", iconBg: "bg-sky-600", defaultCredentialType: "token" },
  { id: "whatsapp", label: "WhatsApp Business", placeholder: "Access token from developers.facebook.com...", category: "automation", iconLetter: "W", iconBg: "bg-green-600", defaultCredentialType: "token" },
  { id: "slack", label: "Slack Bot", placeholder: "Bot token (xoxb-...)...", category: "automation", iconLetter: "S", iconBg: "bg-purple-700", defaultCredentialType: "token" },
];

export interface ModelEntry {
  id: string;
  label: string;
  description: string;
  source: "platform" | "byok" | "subscription";
  tier?: "smart" | "balanced" | "fast";
  input_price_per_m: number;
  output_price_per_m: number;
  sort_order: number;
  provider: string;
}

export interface MachineCredential {
  id: number;
  provider: CredentialProvider;
  credential_type: string;
  label: string;
  last_four: string;
  last_validated: string | null;
  status: "active" | "reauth_required" | "invalid";
  status_detail?: string;
  created_at: string;
  updated_at: string;
}

export interface Machine {
  id: string;
  account_id: number;
  kind?: MachineKind;
  name: string;
  slug: string;
  preferred_region?: string;
  status: "stopped" | "provisioning" | "starting" | "running" | "error";
  status_message?: string;
  vcpus: number;
  memory_mb: number;
  host_id?: number;
  vm_ip?: string;
  tunnel_hostname?: string;
  custom_domain?: string;
  gateway_token?: string;
  provision_step?: string;
  desired_rootfs_version?: string;
  desired_openclaw_version?: string;
  desired_channel?: "stable" | "rc" | "dev";
  resolved_rootfs_version?: string;
  resolved_openclaw_version?: string;
  actual_rootfs_version?: string;
  actual_openclaw_version?: string;
  version_source?: "default" | "channel" | "pinned";
  runtime_source?: "artifact";
  rootfs_snapshot?: string;
  openclaw_version?: string;
  data_volume_gb?: number;
  last_started_at?: string;
  backups_enabled?: boolean;
  browser_vm_id?: string | null;
  created_at: string;
  started_at?: string;
  stopped_at?: string;
}

export interface ChannelCatalogEntry {
  id: string;
  label: string;
  short_desc: string;
  credential_provider?: CredentialProvider;
  has_settings: boolean;
  status: "available" | "coming_soon";
  token_fields?: {
    name: string;
    provider: string;
    target: string;
  }[];
}

export interface BrowserVMPairedMachine {
  id: string;
  name: string;
}

export interface BrowserVM {
  id: string;
  account_id: number;
  slug: string;
  name: string;
  host_id: number | null;
  vm_ip: string | null;
  status: "stopped" | "provisioning" | "running" | "error";
  vcpus: number;
  memory_mb: number;
  cdp_port: number;
  desired_rootfs_manifest?: string | null;
  desired_rootfs_version?: string | null;
  rootfs_version: string | null;
  error_message: string | null;
  created_at: string;
  updated_at: string;
  // Populated by the list endpoint (LEFT JOIN on machines.browser_vm_id);
  // null/undefined when the browser VM is unpaired.
  paired_machine?: BrowserVMPairedMachine | null;
}

export interface VMMetricPoint {
  ts: string;
  cpu_pct: number | null;
  mem_bytes: number;
}

export interface VMMetricsResponse {
  vm_id: string;
  kind: "machine" | "browser";
  resolution: "1s" | "1m" | "mixed";
  from: string;
  to: string;
  points: VMMetricPoint[];
}

export interface OpenClawChangeResponse {
  status: "success" | "queued" | "rolled_back" | "rollback_failed";
  target_version: string;
  rolled_back_to_version?: string;
  error?: string;
  machine?: Machine;
}

export interface RootfsChangeResponse {
  status: "updated" | "pending_restart" | "failed" | "rolled_back" | "rollback_failed";
  target_version: string;
  rolled_back_to_version?: string;
  error?: string;
  machine?: Machine;
}

export interface Backup {
  id: number;
  machine_id: string;
  timestamp: string;
  size_bytes: number;
  compressed_bytes: number;
  trigger: string;
  created_at: string;
}

// Usage & Billing types

export interface MachineUsageSummary {
  machine_id: string;
  machine_name: string;
  spend_microcents: number;
  budget_microcents: number;
}

export interface AccountUsage {
  account_id: number;
  current_month_spend: number;
  per_machine: MachineUsageSummary[];
}

export interface UsageRecord {
  id: number;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cost_microcents: number;
  source: string;
  created_at: string;
}

export interface UsageBucketEntry {
  provider: string;
  model: string;
  source: string;
  input_tokens: number;
  output_tokens: number;
  cost_microcents: number;
  request_count: number;
}

export interface UsageBucket {
  timestamp: string;
  entries: UsageBucketEntry[];
}

export interface UsageBreakdown {
  period: string;
  since: string;
  buckets: UsageBucket[];
  totals: {
    input_tokens: number;
    output_tokens: number;
    cost_microcents: number;
    request_count: number;
  };
}

export interface MachineUsageDetail {
  machine_id: string;
  current_month_spend: number;
  budget_microcents: number;
  since: string;
  records: UsageRecord[];
}

export interface OpikTraceListItem {
  id: string;
  project_id?: string;
  project_name?: string;
  machine_id?: string;
  machine_name?: string;
  name?: string;
  thread_id?: string;
  start_time: string;
  end_time?: string;
  input?: unknown;
  output?: unknown;
  metadata?: unknown;
  tags?: string[];
  error_info?: unknown;
  source?: string;
  created_at?: string;
  last_updated_at?: string;
  span_count: number;
  error_count: number;
  duration_ms: number;
  total_tokens: number;
  total_estimated_cost: number;
  feedback_count: number;
  avg_feedback_score?: number;
}

export interface OpikTraceSpan {
  id: string;
  trace_id: string;
  machine_id: string;
  machine_name?: string;
  parent_span_id?: string;
  name?: string;
  type: string;
  start_time: string;
  end_time?: string;
  input?: unknown;
  output?: unknown;
  metadata?: unknown;
  model?: string;
  provider?: string;
  tags?: string[];
  usage?: unknown;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  error_info?: unknown;
  total_estimated_cost: number;
  duration?: number;
  ttft?: number;
  source?: string;
  created_at?: string;
  last_updated_at?: string;
}

export interface OpikFeedbackScore {
  id: string;
  account_id: number;
  trace_id: string;
  span_id?: string;
  name: string;
  value: number;
  reason?: string;
  source: string;
  created_by?: number;
  created_at: string;
}

export interface OpikTraceListResponse {
  machine_id?: string;
  account_id?: number;
  since: string;
  traces: OpikTraceListItem[];
}

export interface OpikTraceDetail {
  trace: OpikTraceListItem;
  spans: OpikTraceSpan[];
  feedback: OpikFeedbackScore[];
}

export interface MachineBudgetResponse {
  machine_id: string;
  budget_microcents: number;
  limit_cents: number;
}

export interface ProviderCatalogEntry {
  id: string;
  label: string;
  category: "ai" | "search" | "automation";
  credential_type: string;
  placeholder?: string;
  icon_letter?: string;
  icon_bg?: string;
  autodetect_order?: number;
  sort_order: number;
  enabled: boolean;
}
