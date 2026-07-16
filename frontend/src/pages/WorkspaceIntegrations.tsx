import { useCallback, useEffect, useMemo, useRef, useState, type ComponentType, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  AlertCircle,
  ArrowLeft,
  CheckCircle2,
  KeyRound,
  Loader2,
  Plug,
  Search,
  Server,
  ShieldCheck,
  Telescope,
  Wrench,
} from "lucide-react";

// Catalog icons render from className-only Lucide components.
type CatalogIcon = ComponentType<{ className?: string }>;
import { useAuth } from "../lib/auth";
import {
  createWorkspaceIntegrationImport,
  createWorkspaceIntegration,
  listWorkspaceIntegrationCatalog,
  listWorkspaceIntegrationHealth,
  listMembers,
  listWorkspaceIntegrations,
  previewWorkspaceIntegrationImport,
  probeWorkspaceIntegration,
  revokeWorkspaceIntegration,
  startWorkspaceIntegrationOAuth,
  testWorkspaceIntegration,
  updateWorkspaceIntegrationPolicy,
} from "../lib/api";
import type {
  WorkspaceIntegration,
  WorkspaceIntegrationCatalogItem,
  WorkspaceIntegrationImportAuthType,
  WorkspaceIntegrationImportPreview,
  WorkspaceIntegrationImportRequest,
  WorkspaceIntegrationProbeResponse,
  WorkspaceIntegrationProbeTool,
  WorkspaceIntegrationMachineConsumer,
  WorkspaceIntegrationHealthResponse,
  WorkspaceIntegrationServiceStatus,
  WorkspaceIntegrationsResponse,
} from "../lib/api";
import type { AccountMember } from "../lib/types";

type CatalogScope = "platform" | "workspace" | "personal";
type AuthKind = "bearer" | "oauth";
type PermissionLevel = "off" | "read" | "read_write";

type OAuthPermissionOption = {
  value: PermissionLevel;
  label: string;
  description: string;
};

type OAuthPermissionService = {
  id: string;
  label: string;
  description: string;
  defaultLevel: PermissionLevel;
  options: OAuthPermissionOption[];
};

type OAuthPermissionSelection = Record<string, PermissionLevel>;

type CatalogField = {
  id: string;
  label: string;
  type: "text" | "password" | "textarea" | "select";
  placeholder: string;
  required?: boolean;
  secret?: boolean;
  configKey?: string;
  options?: Array<{ value: string; label: string }>;
};

type CatalogEntry = {
  id: string;
  name: string;
  description: string;
  category: string;
  scope: CatalogScope;
  connectionSlug?: string;
  oauthProviderSlug?: string;
  googleService?: "gmail" | "drive" | "calendar";
  Icon: CatalogIcon;
  authKind: AuthKind;
  transport: "http" | "mcp-remote";
  kind?: string;
  endpoint?: string;
  tokenType?: string;
  toolManifest?: Array<Record<string, unknown>>;
  developer: string;
  website: string;
  privacy: string;
  terms: string;
  usageSuggestion: string;
  tools: Array<{ name: string; description: string; access: "Read" | "Write"; mode: "Interactive" | "Background"; source?: string }>;
  fields?: CatalogField[];
  oauthPermissions?: OAuthPermissionService[];
};

const CUSTOM_MCP_CATALOG_ID = "custom-mcp";
const GOOGLE_WORKSPACE_CONNECTION_SLUG = "google-workspace";
const GOOGLE_WORKSPACE_PERMISSION_IDS = ["gmail", "drive", "calendar", "docs"] as const;
const MAX_CUSTOM_MCP_ENABLED_TOOLS = 25;
const CUSTOM_MCP_SNAPSHOT_STALE_MS = 7 * 24 * 60 * 60 * 1000;
const EMPTY_WORKSPACE_INTEGRATIONS: WorkspaceIntegration[] = [];
const EMPTY_WORKSPACE_MACHINES: WorkspaceIntegrationMachineConsumer[] = [];
const CUSTOM_MCP_FIELDS: CatalogField[] = [
  { id: "url", label: "Remote MCP URL", type: "text", placeholder: "https://mcp.example.com", required: true, configKey: "display_target" },
  { id: "token", label: "Bearer token or API key", type: "password", placeholder: "Optional unless the server requires auth", secret: true },
];

const IMPORT_AUTH_OPTIONS = [
  { value: "none", label: "No auth" },
  { value: "bearer", label: "Bearer token" },
  { value: "api_key_header", label: "API key header" },
  { value: "oauth", label: "OAuth access token" },
];

const OPENAPI_IMPORT_FIELDS: CatalogField[] = [
  { id: "slug", label: "Connection slug", type: "text", placeholder: "linear-api", required: true },
  { id: "display_name", label: "Display name", type: "text", placeholder: "Linear API", required: true },
  { id: "spec_url", label: "Spec URL", type: "text", placeholder: "https://api.example.com/openapi.json" },
  { id: "base_url", label: "Base URL override", type: "text", placeholder: "https://api.example.com" },
  { id: "auth_type", label: "Auth mapping", type: "select", placeholder: "No auth", options: IMPORT_AUTH_OPTIONS },
  { id: "auth_header", label: "Auth header", type: "text", placeholder: "Authorization or X-API-Key" },
  { id: "token", label: "Token or API key", type: "password", placeholder: "Stored encrypted on OCM", secret: true },
  { id: "spec_text", label: "Uploaded spec", type: "textarea", placeholder: "Paste OpenAPI JSON or YAML" },
];

const GRAPHQL_IMPORT_FIELDS: CatalogField[] = [
  { id: "slug", label: "Connection slug", type: "text", placeholder: "records-graphql", required: true },
  { id: "display_name", label: "Display name", type: "text", placeholder: "Records GraphQL", required: true },
  { id: "endpoint", label: "GraphQL endpoint", type: "text", placeholder: "https://api.example.com/graphql", required: true },
  { id: "auth_type", label: "Auth mapping", type: "select", placeholder: "No auth", options: IMPORT_AUTH_OPTIONS },
  { id: "auth_header", label: "Auth header", type: "text", placeholder: "Authorization or X-API-Key" },
  { id: "token", label: "Token or API key", type: "password", placeholder: "Stored encrypted on OCM", secret: true },
  { id: "spec_text", label: "Schema or introspection JSON", type: "textarea", placeholder: "Paste GraphQL SDL or introspection JSON", required: true },
];

// Curated remote-MCP catalog entries have a fixed URL (no URL input). Bearer ones
// (e.g. GitHub PAT, Brevo API key) show only a token field; OAuth ones show none.
const CURATED_MCP_BEARER_FIELDS: CatalogField[] = [
  { id: "token", label: "Token or API key", type: "password", placeholder: "Paste your token / API key", secret: true },
];

const offReadWriteOptions: OAuthPermissionOption[] = [
  { value: "off", label: "Off", description: "Do not request access." },
  { value: "read", label: "Read", description: "Allow read-only access." },
  { value: "read_write", label: "Read + write", description: "Allow changes through workspace tools." },
];

export const CATALOG: CatalogEntry[] = [
  {
    id: "github",
    name: "GitHub",
    description: "Repository access for code, issues, and pull requests.",
    category: "Featured",
    scope: "workspace",
    Icon: Server,
    authKind: "bearer",
    transport: "http",
    kind: "github",
    endpoint: "https://api.github.com",
    tokenType: "token",
    developer: "GitHub",
    website: "github.com",
    privacy: "github.com/site/privacy",
    terms: "docs.github.com/site-policy",
    usageSuggestion: "Review repository status, summarize open issues, and inspect pull request context.",
    tools: [
      { name: "get_repo", description: "Get the configured repository summary.", access: "Read", mode: "Interactive" },
      { name: "list_issues", description: "List issues for the configured repository.", access: "Read", mode: "Interactive" },
    ],
    fields: [
      { id: "owner", label: "Repository owner", type: "text", placeholder: "octocat", required: true },
      { id: "repo", label: "Repository name", type: "text", placeholder: "Hello-World", required: true },
      { id: "token", label: "Access token", type: "password", placeholder: "Optional for public repositories", secret: true },
    ],
    toolManifest: [
      {
        name: "get_repo",
        description: "Get the configured GitHub repository summary",
        parameters: {
          type: "object",
          additionalProperties: false,
        },
        request: {
          method: "GET",
          path: "/repos/{owner}/{repo}",
          headers: {
            Accept: "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "openclaw-workspace-integrations",
          },
          path_params: {
            owner: { source: "config", name: "owner" },
            repo: { source: "config", name: "repo" },
          },
          auth: { type: "bearer", scheme: "Bearer", required: false },
          response: {
            fields: {
              full_name: "full_name",
              description: "description",
              private: "private",
              html_url: "html_url",
              default_branch: "default_branch",
              open_issues_count: "open_issues_count",
              stargazers_count: "stargazers_count",
              forks_count: "forks_count",
              updated_at: "updated_at",
            },
          },
        },
      },
      {
        name: "list_issues",
        description: "List issues for the configured GitHub repository",
        parameters: {
          type: "object",
          properties: {
            state: { type: "string", enum: ["open", "closed", "all"] },
            per_page: { type: "integer", minimum: 1, maximum: 30 },
          },
          additionalProperties: false,
        },
        request: {
          method: "GET",
          path: "/repos/{owner}/{repo}/issues",
          headers: {
            Accept: "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "openclaw-workspace-integrations",
          },
          path_params: {
            owner: { source: "config", name: "owner" },
            repo: { source: "config", name: "repo" },
          },
          query: {
            state: { source: "arg", name: "state", default: "open", enum: ["open", "closed", "all"] },
            per_page: { source: "arg", name: "per_page", default: 10, min: 1, max: 30 },
          },
          auth: { type: "bearer", scheme: "Bearer", required: false },
          response: {
            wrap: "issues",
            exclude_if_present: "pull_request",
            fields: {
              number: "number",
              title: "title",
              state: "state",
              html_url: "html_url",
              user: "user.login",
              created_at: "created_at",
              updated_at: "updated_at",
            },
          },
        },
      },
    ],
  },
  {
    id: "gmail",
    connectionSlug: GOOGLE_WORKSPACE_CONNECTION_SLUG,
    googleService: "gmail",
    name: "Gmail",
    description: "Read Gmail messages and send email through workspace-approved tools.",
    category: "Business & Operations",
    scope: "workspace",
    Icon: KeyRound,
    authKind: "oauth",
    transport: "http",
    developer: "Google",
    website: "workspace.google.com/products/gmail",
    privacy: "policies.google.com/privacy",
    terms: "policies.google.com/terms",
    usageSuggestion: "Search inbox messages, read message details, and send approved email.",
    tools: [
      { name: "gmail_list_messages", description: "List Gmail messages, optionally by search query.", access: "Read", mode: "Interactive" },
      { name: "gmail_get_message", description: "Read a single Gmail message.", access: "Read", mode: "Interactive" },
      { name: "gmail_send_message", description: "Send an email (requires Read + send).", access: "Write", mode: "Interactive" },
    ],
    oauthPermissions: [
      {
        id: "gmail",
        label: "Gmail",
        description: "Email profile, messages, and sending permissions.",
        defaultLevel: "read",
        options: [
          { value: "off", label: "Off", description: "No Gmail access." },
          { value: "read", label: "Read", description: "Read Gmail messages and settings." },
          { value: "read_write", label: "Read + send", description: "Read Gmail and send email." },
        ],
      },
    ],
  },
  {
    id: "google-drive",
    connectionSlug: GOOGLE_WORKSPACE_CONNECTION_SLUG,
    googleService: "drive",
    name: "Google Drive",
    description: "List Drive metadata and create approved files through workspace tools.",
    category: "Business & Operations",
    scope: "workspace",
    Icon: KeyRound,
    authKind: "oauth",
    transport: "http",
    developer: "Google",
    website: "workspace.google.com/products/drive",
    privacy: "policies.google.com/privacy",
    terms: "policies.google.com/terms",
    usageSuggestion: "Find visible Drive files and create files in approved folders.",
    tools: [
      { name: "drive_list_files", description: "List visible Google Drive file metadata.", access: "Read", mode: "Interactive" },
      { name: "drive_create_file", description: "Create a Drive file (requires Read + write).", access: "Write", mode: "Interactive" },
    ],
    oauthPermissions: [
      {
        id: "drive",
        label: "Google Drive",
        description: "Drive file discovery and file operations.",
        defaultLevel: "read",
        options: offReadWriteOptions,
      },
    ],
  },
  {
    id: "google-calendar",
    connectionSlug: GOOGLE_WORKSPACE_CONNECTION_SLUG,
    googleService: "calendar",
    name: "Google Calendar",
    description: "Read and create calendar events through workspace-approved tools.",
    category: "Business & Operations",
    scope: "workspace",
    Icon: KeyRound,
    authKind: "oauth",
    transport: "http",
    developer: "Google",
    website: "workspace.google.com/products/calendar",
    privacy: "policies.google.com/privacy",
    terms: "policies.google.com/terms",
    usageSuggestion: "List upcoming events and create approved calendar events.",
    tools: [
      { name: "calendar_list_events", description: "List upcoming Google Calendar events.", access: "Read", mode: "Interactive" },
      { name: "calendar_create_event", description: "Create a calendar event (requires Read + write).", access: "Write", mode: "Interactive" },
    ],
    oauthPermissions: [
      {
        id: "calendar",
        label: "Calendar",
        description: "Calendar event lookup and updates.",
        defaultLevel: "read",
        options: offReadWriteOptions,
      },
    ],
  },
  {
    id: "google-workspace",
    name: "Google Workspace",
    description: "OAuth connection for Gmail, Drive, Calendar, and Docs.",
    category: "Business & Operations",
    scope: "workspace",
    Icon: KeyRound,
    authKind: "oauth",
    transport: "http",
    developer: "Google",
    website: "workspace.google.com",
    privacy: "policies.google.com/privacy",
    terms: "policies.google.com/terms",
    usageSuggestion: "Read and send mail, find and create Drive files, and read or create calendar events and docs.",
    tools: [
      { name: "gmail_list_messages", description: "List Gmail messages, optionally by search query.", access: "Read", mode: "Interactive" },
      { name: "gmail_get_message", description: "Read a single Gmail message.", access: "Read", mode: "Interactive" },
      { name: "gmail_send_message", description: "Send an email (requires Gmail Read + send).", access: "Write", mode: "Interactive" },
      { name: "drive_list_files", description: "List visible Google Drive file metadata.", access: "Read", mode: "Interactive" },
      { name: "drive_create_file", description: "Create a Drive file (requires Drive Read + write).", access: "Write", mode: "Interactive" },
      { name: "calendar_list_events", description: "List upcoming Google Calendar events.", access: "Read", mode: "Interactive" },
      { name: "calendar_create_event", description: "Create a calendar event (requires Calendar Read + write).", access: "Write", mode: "Interactive" },
      { name: "docs_get_document", description: "Read a Google Doc (requires Docs Read).", access: "Read", mode: "Interactive" },
      { name: "docs_create_document", description: "Create a Google Doc (requires Docs Read + write).", access: "Write", mode: "Interactive" },
    ],
    oauthPermissions: [
      {
        id: "gmail",
        label: "Gmail",
        description: "Email profile, messages, and sending permissions.",
        defaultLevel: "read",
        options: [
          { value: "off", label: "Off", description: "No Gmail access." },
          { value: "read", label: "Read", description: "Read Gmail messages and settings." },
          { value: "read_write", label: "Read + send", description: "Read Gmail and send email." },
        ],
      },
      {
        id: "drive",
        label: "Google Drive",
        description: "Drive file discovery and file operations.",
        defaultLevel: "read",
        options: offReadWriteOptions,
      },
      {
        id: "calendar",
        label: "Calendar",
        description: "Calendar event lookup and updates.",
        defaultLevel: "read",
        options: offReadWriteOptions,
      },
      {
        id: "docs",
        label: "Docs",
        description: "Google Docs document access.",
        defaultLevel: "off",
        options: offReadWriteOptions,
      },
    ],
  },
  {
    id: "apollo",
    name: "Apollo.io",
    description: "Prospecting and account research through hosted MCP.",
    category: "Business & Operations",
    scope: "platform",
    Icon: Telescope,
    authKind: "oauth",
    transport: "mcp-remote",
    developer: "Apollo.io",
    website: "apollo.io",
    privacy: "apollo.io/privacy-policy",
    terms: "apollo.io/terms",
    usageSuggestion: "Research accounts and enrich lead context before drafting outreach.",
    tools: [
      { name: "search_people", description: "Search people records through Apollo.", access: "Read", mode: "Interactive" },
      { name: "search_organizations", description: "Search organization records through Apollo.", access: "Read", mode: "Interactive" },
    ],
  },
  {
    id: CUSTOM_MCP_CATALOG_ID,
    name: "Custom MCP server",
    description: "Connect a hosted MCP server by URL, review discovered tools, and add a bearer token when needed.",
    category: "Custom",
    scope: "workspace",
    Icon: Server,
    authKind: "bearer",
    transport: "mcp-remote",
    kind: "custom-mcp",
    developer: "Custom",
    website: "Remote MCP URL",
    privacy: "Provider policy",
    terms: "Provider terms",
    usageSuggestion: "Use a trusted remote MCP server after reviewing the tools it exposes.",
    tools: [],
    fields: CUSTOM_MCP_FIELDS,
  },
  {
    id: "openapi-import",
    name: "OpenAPI import",
    description: "Import an OpenAPI 3.x spec into workspace-reviewed tools.",
    category: "Custom",
    scope: "workspace",
    Icon: Server,
    authKind: "bearer",
    transport: "http",
    kind: "openapi-import",
    developer: "Custom",
    website: "OpenAPI spec",
    privacy: "Provider policy",
    terms: "Provider terms",
    usageSuggestion: "Preview generated operations, review read/write classification, then save the connection.",
    tools: [],
    fields: OPENAPI_IMPORT_FIELDS,
  },
  {
    id: "graphql-import",
    name: "GraphQL import",
    description: "Import a GraphQL schema into fixed query and mutation tools.",
    category: "Custom",
    scope: "workspace",
    Icon: Server,
    authKind: "bearer",
    transport: "http",
    kind: "graphql-import",
    developer: "Custom",
    website: "GraphQL endpoint",
    privacy: "Provider policy",
    terms: "Provider terms",
    usageSuggestion: "Preview generated query and mutation tools before enabling the connection.",
    tools: [],
    fields: GRAPHQL_IMPORT_FIELDS,
  },
];

function catalogIconForSlug(slug: string, authKind: AuthKind): CatalogIcon {
  switch (slug) {
    case "github":
      return Server;
    case "google-workspace":
      return KeyRound;
    case "google-cloud":
      return Telescope;
    case "notion":
      return Plug;
    case "brevo":
      return Wrench;
    case "cloudflare":
      return Server;
    case "apollo":
      // No brand mark available for Apollo.io; use a prospecting glyph.
      return Telescope;
    case CUSTOM_MCP_CATALOG_ID:
      return Server;
    case "openapi-import":
    case "graphql-import":
      return Server;
    default:
      return authKind === "bearer" ? KeyRound : Plug;
  }
}

function catalogToolsFromBackendItem(item: WorkspaceIntegrationCatalogItem, localEntry?: CatalogEntry): CatalogEntry["tools"] {
  const tools = Array.isArray(item.tools) ? item.tools : [];
  if (tools.length > 0) {
    return tools.map((tool) => ({
      name: tool.name,
      description: tool.description || "Workspace integration tool.",
      access: tool.access,
      mode: tool.mode,
      source: tool.source,
    }));
  }
  return localEntry?.tools ?? [];
}

function catalogToolManifestFromBackendItem(item: WorkspaceIntegrationCatalogItem, localEntry?: CatalogEntry): CatalogEntry["toolManifest"] {
  const tools = Array.isArray(item.tools) ? item.tools : [];
  if (tools.length > 0) {
    return tools.map((tool) => ({
      name: tool.name,
      description: tool.description || "Workspace integration tool.",
    }));
  }
  return localEntry?.toolManifest;
}

function catalogEntryFromBackendItem(item: WorkspaceIntegrationCatalogItem, localEntry?: CatalogEntry): CatalogEntry {
  return {
    id: item.slug,
    name: item.display_name,
    description: item.description,
    category: item.category,
    scope: localEntry?.scope ?? "workspace",
    connectionSlug: item.connection_slug ?? localEntry?.connectionSlug,
    oauthProviderSlug: localEntry?.oauthProviderSlug,
    googleService: item.google_service ?? localEntry?.googleService,
    Icon: localEntry?.Icon ?? catalogIconForSlug(item.slug, item.auth_kind),
    authKind: item.auth_kind,
    transport: item.transport,
    // A curated remote-MCP entry has a fixed backend-owned endpoint and manifest,
    // so it can be saved from the catalog without probing a user-entered server.
    kind: localEntry?.kind ?? item.slug,
    endpoint: item.remote_url || localEntry?.endpoint,
    tokenType: item.remote_url ? "bearer" : localEntry?.tokenType,
    toolManifest: item.remote_url ? catalogToolManifestFromBackendItem(item, localEntry) : localEntry?.toolManifest,
    developer: item.developer,
    website: item.website,
    privacy: item.privacy,
    terms: item.terms,
    usageSuggestion: localEntry?.usageSuggestion ?? `Use ${item.display_name} through workspace-approved tools.`,
    tools: catalogToolsFromBackendItem(item, localEntry),
    fields: item.remote_url
      ? (item.auth_kind === "bearer" ? CURATED_MCP_BEARER_FIELDS : undefined)
      : localEntry?.fields,
    oauthPermissions: localEntry?.oauthPermissions,
  };
}

function googleWorkspaceAccountLabel(integration: WorkspaceIntegration) {
  return integration.target || integration.display_name || integration.slug;
}

function googleWorkspaceAccountServiceEntryId(serviceEntryId: string, connectionSlug: string) {
  return `${serviceEntryId}--${connectionSlug}`;
}

function googleWorkspaceServiceEntriesForIntegration(entries: Map<string, CatalogEntry>, integration: WorkspaceIntegration) {
  if (integration.kind !== "google_workspace" || integration.slug === GOOGLE_WORKSPACE_CONNECTION_SLUG) {
    return [];
  }
  const accountLabel = googleWorkspaceAccountLabel(integration);
  return [...entries.values()]
    .filter((entry) => (
      entry.googleService
      && catalogEntryConnectionSlug(entry) === GOOGLE_WORKSPACE_CONNECTION_SLUG
      && googleServiceConfiguredForIntegration(entry, integration)
    ))
    .map((entry) => ({
      ...entry,
      id: googleWorkspaceAccountServiceEntryId(entry.id, integration.slug),
      name: `${entry.name} - ${accountLabel}`,
      category: "Added",
      connectionSlug: integration.slug,
      oauthProviderSlug: GOOGLE_WORKSPACE_CONNECTION_SLUG,
    }));
}

export function catalogEntriesFromBackend(
  catalogItems: WorkspaceIntegrationCatalogItem[] | null | undefined,
  integrations: WorkspaceIntegration[],
  localEntries: CatalogEntry[] = CATALOG,
) {
  const localById = new Map(localEntries.map((entry) => [entry.id, entry]));
  const entries = new Map<string, CatalogEntry>();
  if (catalogItems?.length) {
    for (const item of catalogItems) {
      entries.set(item.slug, catalogEntryFromBackendItem(item, localById.get(item.slug)));
    }
    for (const entry of localEntries) {
      if (!entries.has(entry.id)) {
        entries.set(entry.id, entry);
      }
    }
  } else {
    for (const entry of localEntries) {
      entries.set(entry.id, entry);
    }
  }
  for (const integration of integrations) {
    for (const entry of googleWorkspaceServiceEntriesForIntegration(entries, integration)) {
      entries.set(entry.id, entry);
    }
    if (!entries.has(integration.slug)) {
      entries.set(integration.slug, catalogEntryFromIntegration(integration));
    }
  }
  return [...entries.values()];
}

function catalogEntryConnectionSlug(entry: CatalogEntry | null | undefined) {
  return entry?.connectionSlug ?? entry?.id ?? "";
}

function catalogEntryOAuthProviderSlug(entry: CatalogEntry | null | undefined) {
  return entry?.oauthProviderSlug ?? catalogEntryConnectionSlug(entry);
}

function googleServiceConfiguredForIntegration(entry: CatalogEntry, integration: WorkspaceIntegration) {
  if (!entry.googleService) return true;
  const status = integration.service_status?.[entry.googleService]?.status;
  if (status) return status !== "off";
  const prefixes: Record<NonNullable<CatalogEntry["googleService"]>, string[]> = {
    gmail: ["gmail_"],
    drive: ["drive_"],
    calendar: ["calendar_"],
  };
  const tools = Array.isArray(integration.tools) ? integration.tools : [];
  return tools.some((tool) => prefixes[entry.googleService!].some((prefix) => tool.name.startsWith(prefix)));
}

function connectedCatalogEntries(catalogEntries: CatalogEntry[], integrations: WorkspaceIntegration[]) {
  return integrations.filter(isWorkspaceIntegrationConnected).flatMap((integration) => {
    const matches = catalogEntries.filter((candidate) => catalogEntryConnectionSlug(candidate) === integration.slug);
    if (integration.slug === GOOGLE_WORKSPACE_CONNECTION_SLUG || integration.kind === "google_workspace") {
      const serviceEntries = matches.filter((entry) => entry.googleService && googleServiceConfiguredForIntegration(entry, integration));
      if (serviceEntries.length > 0) {
        return serviceEntries.map((entry) => ({ integration, entry }));
      }
    }
    const entry = matches.find((candidate) => candidate.id !== integration.slug) ?? matches[0];
    return [{ integration, entry }];
  });
}

function catalogEntryConnectionDisabled(entry: CatalogEntry) {
  if (catalogEntryIsImport(entry)) return false;
  if (entry.id === CUSTOM_MCP_CATALOG_ID || entry.kind === "custom-mcp" || catalogEntryOAuthProviderSlug(entry) === GOOGLE_WORKSPACE_CONNECTION_SLUG) return false;
  if (entry.id === "apollo") return true;
  if (entry.authKind === "oauth") return true;
  return !(entry.endpoint && entry.toolManifest);
}

function catalogEntryIsCustomMCP(entry: CatalogEntry | null | undefined) {
  return entry?.id === CUSTOM_MCP_CATALOG_ID || entry?.kind === "custom-mcp";
}

function catalogEntryIsImport(entry: CatalogEntry | null | undefined) {
  return entry?.kind === "openapi-import" || entry?.kind === "graphql-import";
}

function statusClass(enabled: boolean) {
  return enabled
    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"
    : "border-border bg-[var(--color-surface)] text-text-tertiary";
}

function configStatusClass(configured: boolean) {
  return configured
    ? "border-amber-500/30 bg-amber-500/10 text-amber-500"
    : "border-border bg-[var(--color-surface)] text-text-tertiary";
}

function machineStatusClass(status: string) {
  switch (status) {
    case "running":
      return "text-emerald-500";
    case "error":
      return "text-red-500";
    case "provisioning":
    case "starting":
      return "text-amber-500";
    default:
      return "text-text-tertiary";
  }
}

function ConnectorStatus({ connected }: { connected: boolean }) {
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-1 text-[11px] ${statusClass(connected)}`}>
      {connected ? <CheckCircle2 className="h-3 w-3" /> : <Plug className="h-3 w-3" />}
      {connected ? "Added" : "Available"}
    </span>
  );
}

function IntegrationCatalogRow({
  entry,
  connected,
  canManage,
  disabled,
  href,
  onConnect,
}: {
  entry: CatalogEntry;
  connected: boolean;
  canManage: boolean;
  disabled?: boolean;
  href: string;
  onConnect: () => void;
}) {
  const Icon = entry.Icon;
  const label = disabled ? "Coming soon" : connected ? (canManage ? "Manage" : "View") : canManage ? "Add" : "View";
  // Custom-MCP entries (generic or curated remote-MCP like Notion) must reach the
  // detail page to probe the server before connecting/OAuth — they can't use the
  // direct consent->connect flow (which has no probe step).
  const useConnectFlow = canManage && !disabled && !connected && entry.authKind === "oauth" && !catalogEntryIsCustomMCP(entry);
  const actionClass = connected
    ? "border border-border text-text-secondary hover:bg-[var(--color-surface-hover)]"
    : "border border-border bg-card text-text-primary hover:bg-[var(--color-surface-hover)]";
  return (
    <div className="grid gap-3 border-t border-border py-4 first:border-t-0 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
      <Link to={href} className="group flex min-w-0 items-start gap-3">
        <div className="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-[var(--radius-sm)] border border-border bg-card">
          <Icon className="h-5 w-5 text-text-primary" />
        </div>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-[15px] font-semibold text-text-primary group-hover:text-brand-600">{entry.name}</h3>
            {connected && <ConnectorStatus connected />}
          </div>
          <p className="mt-1 max-w-xl text-sm text-text-secondary">{entry.description}</p>
        </div>
      </Link>
      {useConnectFlow ? (
        <button
          type="button"
          onClick={onConnect}
          className={`inline-flex min-w-[92px] items-center justify-center rounded-[var(--radius-sm)] px-3 py-2 text-sm font-medium transition-colors ${actionClass}`}
        >
          {label}
        </button>
      ) : (
        <Link
          to={href}
          aria-disabled={disabled}
          className={`inline-flex min-w-[92px] items-center justify-center rounded-[var(--radius-sm)] px-3 py-2 text-sm font-medium transition-colors ${
            disabled
              ? "pointer-events-none border border-border text-text-tertiary opacity-60"
              : actionClass
          }`}
        >
          {label}
        </Link>
      )}
    </div>
  );
}

function ScopePill({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex items-center rounded-full border border-border bg-[var(--color-surface)] px-2 py-1 text-[11px] text-text-secondary">
      {children}
    </span>
  );
}

function ApprovalBadge({ label = "Approved by your admin" }: { label?: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-1 text-[11px] text-emerald-500">
      <ShieldCheck className="h-3 w-3" />
      {label}
    </span>
  );
}

function googleServiceStatusItems(integration: WorkspaceIntegration | null): WorkspaceIntegrationServiceStatus[] {
  if (!integration || (integration.slug !== "google-workspace" && integration.kind !== "google_workspace")) {
    return [];
  }
  const statuses = integration.service_status ?? {};
  return ["gmail", "drive", "calendar"].flatMap((service) => {
    const status = statuses[service];
    return status ? [{ ...status, service }] : [];
  });
}

function googleServiceLabel(service: string) {
  switch (service) {
    case "gmail":
      return "Gmail";
    case "drive":
      return "Google Drive";
    case "calendar":
      return "Google Calendar";
    default:
      return service;
  }
}

function googleServiceStatusLabel(status: string) {
  switch (status) {
    case "connected":
      return "Ready";
    case "missing_scope":
      return "Missing scope";
    case "admin_policy_blocked":
      return "Admin blocked";
    case "token_revoked":
      return "Reconnect";
    case "rate_limited":
      return "Rate limited";
    case "upstream_unavailable":
      return "Unavailable";
    case "off":
      return "Off";
    default:
      return status.replace(/_/g, " ");
  }
}

function googleServiceStatusClass(status: string) {
  switch (status) {
    case "connected":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-500";
    case "missing_scope":
    case "admin_policy_blocked":
    case "token_revoked":
      return "border-red-500/30 bg-red-500/10 text-red-500";
    case "rate_limited":
    case "upstream_unavailable":
      return "border-amber-500/30 bg-amber-500/10 text-amber-500";
    default:
      return "border-border bg-[var(--color-surface)] text-text-secondary";
  }
}

function googleServiceActionLabel(action?: string) {
  switch (action) {
    case "grant_scope_or_reconnect":
      return "Reconnect and grant access.";
    case "ask_google_workspace_admin":
      return "Ask a Google Workspace admin to allow access.";
    case "reconnect_integration":
      return "Reconnect the Google account.";
    case "retry_later":
      return "Retry later.";
    case "connect_service":
      return "Enable this service to expose tools.";
    default:
      return "";
  }
}

function GoogleServiceStatusPanel({ statuses }: { statuses: WorkspaceIntegrationServiceStatus[] }) {
  if (statuses.length === 0) return null;
  return (
    <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3">
      <h3 className="text-sm font-semibold text-text-primary">Google services</h3>
      <div className="mt-3">
        {statuses.map((status) => {
          const action = googleServiceActionLabel(status.action);
          return (
            <div key={status.service} className="border-t border-border py-2 first:border-t-0 first:pt-0 last:pb-0">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="text-sm font-medium text-text-primary">{googleServiceLabel(status.service)}</div>
                <span className={`inline-flex items-center rounded-full border px-2 py-1 text-[11px] ${googleServiceStatusClass(status.status)}`}>
                  {googleServiceStatusLabel(status.status)}
                </span>
              </div>
              {(status.detail || action) && (
                <div className="mt-1 text-xs text-text-secondary">
                  {status.detail || action}
                  {status.detail && action ? ` ${action}` : ""}
                </div>
              )}
              {status.http_status && (
                <div className="mt-1 text-[11px] text-text-tertiary">HTTP {status.http_status}</div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function MachineRuntimeRow({
  machine,
  hasConfiguredIntegration,
}: {
  machine: WorkspaceIntegrationMachineConsumer;
  hasConfiguredIntegration: boolean;
}) {
  return (
    <tr className="border-t border-border">
      <td className="py-3 pr-4">
        <div className="flex items-center gap-2">
          <Server className="h-4 w-4 text-text-tertiary" />
          <span className="text-sm font-medium text-text-primary">{machine.name}</span>
        </div>
      </td>
      <td className={`py-3 pr-4 text-sm capitalize ${machineStatusClass(machine.status)}`}>{machine.status}</td>
      <td className="py-3 pr-4">
        <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-1 text-[11px] ${configStatusClass(machine.plugin_enabled)}`}>
          {machine.plugin_enabled ? <Wrench className="h-3 w-3" /> : <Plug className="h-3 w-3" />}
          {machine.plugin_enabled ? "Config enabled" : "Not configured"}
        </span>
      </td>
      <td className="py-3 text-right">
        {machine.plugin_enabled ? (
          <div className="text-xs text-text-tertiary">
            <div>Active on next agent run</div>
            {machine.installed_version && <div className="mt-0.5">Catalog {machine.installed_version}</div>}
          </div>
        ) : hasConfiguredIntegration ? (
          <span className="text-xs text-text-tertiary">Assigns on next config push</span>
        ) : (
          <span className="text-xs text-text-tertiary">Waiting for an app</span>
        )}
      </td>
    </tr>
  );
}

function integrationPath(workspaceId: string | undefined, catalogId?: string) {
  const base = workspaceId ? `/workspaces/${workspaceId}/integrations` : "/integrations";
  return catalogId ? `${base}/${catalogId}` : base;
}

function trustedOAuthRedirect(raw: string) {
  try {
    const parsed = new URL(raw, window.location.origin);
    if (parsed.origin === window.location.origin || parsed.protocol === "https:") return true;
    return parsed.protocol === "http:"
      && localOAuthHost(window.location.hostname)
      && localOAuthHost(parsed.hostname);
  } catch {
    return false;
  }
}

function localOAuthHost(hostname: string) {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1" || hostname === "[::1]";
}

let redirectToWorkspaceIntegrationOAuth = (url: string) => window.location.assign(url);

export function setWorkspaceIntegrationOAuthRedirectForTest(redirect?: (url: string) => void) {
  redirectToWorkspaceIntegrationOAuth = redirect ?? ((url: string) => window.location.assign(url));
}

function memberDisplayName(members: AccountMember[], userId?: number) {
  if (!userId) return "Unknown user";
  const member = members.find((candidate) => candidate.user_id === userId);
  return member?.name || member?.email || `User ${userId}`;
}

function formatConnectionTime(value?: string) {
  if (!value) return "Unknown time";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Unknown time";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function catalogFieldValue(values: Record<string, string>, fieldId: string) {
  return values[fieldId]?.trim() ?? "";
}

export function catalogConfig(entry: CatalogEntry, values: Record<string, string>) {
  const config: Record<string, unknown> = {};
  for (const field of entry.fields ?? []) {
    if (field.secret) continue;
    const value = catalogFieldValue(values, field.id);
    if (value) {
      config[field.configKey ?? field.id] = value;
    }
  }
  return config;
}

export function catalogSecret(entry: CatalogEntry, values: Record<string, string>) {
  const secretField = entry.fields?.find((field) => field.secret);
  return secretField ? catalogFieldValue(values, secretField.id) : "";
}

function catalogDisplayName(entry: CatalogEntry, values: Record<string, string>) {
  if (entry.id === "github") {
    const owner = catalogFieldValue(values, "owner");
    const repo = catalogFieldValue(values, "repo");
    if (owner && repo) return `${entry.name} ${owner}/${repo}`;
  }
  return entry.name;
}

function slugifyCustomMCP(value: string) {
  const slug = value
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, "")
    .replace(/^www\./, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48)
    .replace(/-+$/g, "");
  return slug || "custom-mcp";
}

export function deriveCustomMCPSlug({
  url,
  serverName,
  existingSlugs,
}: {
  url: string;
  serverName?: string;
  existingSlugs: string[];
}) {
  let hostname = "";
  try {
    hostname = new URL(url).hostname;
  } catch {
    hostname = "";
  }
  const base = slugifyCustomMCP(serverName || hostname || "custom-mcp");
  const used = new Set([...CATALOG.map((entry) => entry.id), ...existingSlugs]);
  if (!used.has(base)) return base;
  for (let index = 2; index < 1000; index += 1) {
    const candidate = `${base}-${index}`;
    if (!used.has(candidate)) return candidate;
  }
  return `${base}-${Date.now()}`;
}

const READ_VERB_SEGMENTS = new Set([
  "get", "list", "search", "find", "read", "fetch", "lookup", "query", "describe",
  "view", "show", "count", "retrieve", "check", "browse", "inspect",
]);
const WRITE_VERB_SEGMENTS = new Set([
  "create", "update", "delete", "remove", "send", "write", "patch", "post", "submit",
  "sync", "move", "duplicate", "insert", "upload", "publish", "archive", "revoke",
  "cancel", "edit", "add", "set", "put", "rename", "merge", "import",
]);

// Real MCP tools are namespaced (notion-search, kv_namespaces_list), so match a
// verb in any name segment. Write verbs win over read verbs.
function customMCPToolIsReadLike(tool: Pick<WorkspaceIntegrationProbeTool, "name" | "description">) {
  const segments = tool.name.toLowerCase().split(/[-_.\s/]+/).filter(Boolean);
  if (segments.some((segment) => WRITE_VERB_SEGMENTS.has(segment))) return false;
  return segments.some((segment) => READ_VERB_SEGMENTS.has(segment));
}

export function customMCPToolsFromProbe(tools: WorkspaceIntegrationProbeTool[]): CatalogEntry["tools"] {
  return tools.map((tool) => ({
    name: tool.name,
    description: tool.description || "Discovered from the remote MCP server.",
    access: customMCPToolIsReadLike(tool) ? "Read" : "Write",
    mode: "Interactive",
  }));
}

export function customMCPInitialPolicy(tools: CatalogEntry["tools"]) {
  const allowedTools: string[] = [];
  const deniedTools: string[] = [];
  for (const tool of tools) {
    if (tool.access === "Read" && allowedTools.length < MAX_CUSTOM_MCP_ENABLED_TOOLS) {
      allowedTools.push(tool.name);
    } else {
      deniedTools.push(tool.name);
    }
  }
  return { allowedTools, deniedTools };
}

export function customMCPManifestFromProbe(tools: WorkspaceIntegrationProbeTool[]) {
  return tools.map((tool) => ({
    name: tool.name,
    description: tool.description || "Discovered from the remote MCP server.",
    input_schema: tool.inputSchema ?? { type: "object", additionalProperties: true },
    mcp_remote: {
      name: tool.name,
      auth: { type: "bearer", required: false },
    },
  }));
}

function workspaceIntegrationImportRequestFromFields(entry: CatalogEntry, values: Record<string, string>): WorkspaceIntegrationImportRequest {
  const type = entry.kind === "graphql-import" ? "graphql" : "openapi";
  const authType = (catalogFieldValue(values, "auth_type") || "none") as WorkspaceIntegrationImportAuthType;
  const authHeader = catalogFieldValue(values, "auth_header");
  const request: WorkspaceIntegrationImportRequest = {
    type,
    slug: catalogFieldValue(values, "slug"),
    display_name: catalogFieldValue(values, "display_name"),
    spec_text: catalogFieldValue(values, "spec_text"),
    spec_url: type === "openapi" ? catalogFieldValue(values, "spec_url") : undefined,
    base_url: type === "openapi" ? catalogFieldValue(values, "base_url") : undefined,
    endpoint: type === "graphql" ? catalogFieldValue(values, "endpoint") : undefined,
    auth: {
      type: authType,
      ...(authHeader ? { header: authHeader } : {}),
    },
  };
  const token = catalogFieldValue(values, "token");
  if (token) {
    request.token = token;
  }
  return request;
}

function importPreviewTools(preview: WorkspaceIntegrationImportPreview | null): CatalogEntry["tools"] {
  const tools = Array.isArray(preview?.tools) ? preview.tools : [];
  return tools.map((tool) => ({
    name: tool.name,
    description: tool.description || "Imported tool.",
    access: tool.access,
    mode: tool.mode,
    source: tool.source,
  }));
}

function workspaceIntegrationImportAuthLabel(authKind: WorkspaceIntegrationImportAuthType) {
  switch (authKind) {
    case "none":
      return "No auth";
    case "bearer":
      return "Bearer token";
    case "api_key_header":
      return "API key header";
    case "oauth":
      return "OAuth access token";
  }
}

export function workspaceIntegrationSnapshotStatus(
  integration: Pick<WorkspaceIntegration, "snapshot">,
  now = new Date(),
): { state: "fresh" | "stale" | "missing"; label: string } {
  const raw = integration.snapshot?.probed_at;
  if (!raw) {
    return { state: "missing", label: "Probe needed" };
  }
  const probedAt = new Date(raw);
  if (Number.isNaN(probedAt.getTime())) {
    return { state: "missing", label: "Probe needed" };
  }
  if (now.getTime() - probedAt.getTime() > CUSTOM_MCP_SNAPSHOT_STALE_MS) {
    return { state: "stale", label: "Refresh recommended" };
  }
  return { state: "fresh", label: "Current" };
}

function catalogEntryFromIntegration(integration: WorkspaceIntegration): CatalogEntry {
  const integrationTools = Array.isArray(integration.tools) ? integration.tools : [];
  const tools = integrationTools.length
    ? integrationTools.map((tool) => ({
      name: tool.name,
      description: tool.description || "Workspace integration tool.",
      access: tool.access,
      mode: tool.mode,
    }))
    : Array.from({ length: integration.tool_count }, (_, index) => ({
      name: `tool_${index + 1}`,
      description: "Tool snapshot is not available in this response.",
      access: "Read" as const,
      mode: "Interactive" as const,
    }));
  return {
    id: integration.slug,
    name: integration.display_name,
    description: integration.target ? `Custom MCP server at ${integration.target}` : "Custom workspace integration.",
    category: "Added",
    scope: "workspace",
    Icon: Server,
    authKind: "bearer",
    transport: integration.transport === "mcp-remote" ? "mcp-remote" : "http",
    kind: integration.kind,
    fields: integration.kind === "custom-mcp" ? CUSTOM_MCP_FIELDS : undefined,
    developer: integration.kind === "custom-mcp" ? "Custom" : integration.display_name,
    website: integration.target || "Workspace integration",
    privacy: "Provider policy",
    terms: "Provider terms",
    usageSuggestion: integration.kind === "custom-mcp"
      ? "Use this custom MCP server through the workspace tools reviewed by an admin."
      : "Use this workspace integration through the enabled tools.",
    tools,
  };
}

export function requiredCatalogFieldsPresent(entry: CatalogEntry | null, values: Record<string, string>) {
  return !(entry?.fields ?? []).some((field) => field.required && !catalogFieldValue(values, field.id));
}

function permissionSelectionFromEntry(entry: CatalogEntry, integration?: WorkspaceIntegration | null): OAuthPermissionSelection {
  const selection: OAuthPermissionSelection = {};
  for (const permission of entry.oauthPermissions ?? []) {
    const raw = integration?.permission_levels?.[permission.id];
    const allowed = permission.options.some((option) => option.value === raw);
    selection[permission.id] = allowed ? (raw as PermissionLevel) : permission.defaultLevel;
  }
  return selection;
}

export function googleWorkspaceOAuthPayload(selection: OAuthPermissionSelection) {
  return { permissions: { ...selection } };
}

function oauthPayloadForEntry(entry: CatalogEntry, selection: OAuthPermissionSelection) {
  if (!entry.oauthPermissions?.length) return undefined;
  if (catalogEntryOAuthProviderSlug(entry) === GOOGLE_WORKSPACE_CONNECTION_SLUG) {
    const permissions: OAuthPermissionSelection = {};
    for (const id of GOOGLE_WORKSPACE_PERMISSION_IDS) {
      permissions[id] = "off";
    }
    return googleWorkspaceOAuthPayload({ ...permissions, ...selection });
  }
  return googleWorkspaceOAuthPayload(selection);
}

function isWorkspaceIntegrationConnected(integration?: WorkspaceIntegration | null) {
  return Boolean(integration?.connected_at || integration?.target || integration?.enabled);
}

function policyIncludes(values: string[] | undefined, toolName: string) {
  return (values ?? []).includes(toolName);
}

// effectivePolicy mirrors the gateway's resolution: deny always wins, and an
// empty allow-list means "allow all"; adding any allow entry switches to
// allow-list mode where unlisted tools are blocked.
export function effectivePolicy(allowed: string[], denied: string[], toolName: string): "Allowed" | "Blocked" {
  if (denied.includes(toolName)) return "Blocked";
  if (allowed.length > 0 && !allowed.includes(toolName)) return "Blocked";
  return "Allowed";
}

function updatePolicyTool(values: string[], toolName: string, enabled: boolean) {
  if (enabled) {
    return values.includes(toolName) ? values : [...values, toolName];
  }
  return values.filter((candidate) => candidate !== toolName);
}

function ModalOverlay({
  labelledBy,
  maxWidthClass,
  onClose,
  children,
}: {
  labelledBy: string;
  maxWidthClass: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    dialogRef.current?.focus();
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 px-4 py-6"
      onClick={onClose}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        className={`w-full ${maxWidthClass} rounded-[var(--radius-sm)] border border-border bg-card p-5 shadow-xl outline-none`}
      >
        {children}
      </div>
    </div>
  );
}

function ConnectConsentModal({
  entry,
  busy,
  advancedOpen,
  permissionSelection,
  onAdvancedToggle,
  onPermissionChange,
  onCancel,
  onContinue,
}: {
  entry: CatalogEntry;
  busy: boolean;
  advancedOpen: boolean;
  permissionSelection: OAuthPermissionSelection;
  onAdvancedToggle: () => void;
  onPermissionChange: (serviceId: string, level: PermissionLevel) => void;
  onCancel: () => void;
  onContinue: () => void;
}) {
  const isOAuth = entry.authKind === "oauth";
  return (
    <ModalOverlay labelledBy="connect-consent-title" maxWidthClass="max-w-lg" onClose={busy ? () => {} : onCancel}>
        <div className="text-center">
          <div className="mx-auto flex items-center justify-center gap-4">
            <div className="flex h-14 w-14 items-center justify-center rounded-[var(--radius-sm)] bg-text-primary text-white">
              <Plug className="h-7 w-7" />
            </div>
            <div className="flex gap-1">
              <span className="h-2 w-2 rounded-full bg-border" />
              <span className="h-2 w-2 rounded-full bg-border" />
              <span className="h-2 w-2 rounded-full bg-border" />
            </div>
            <div className="flex h-14 w-14 items-center justify-center rounded-[var(--radius-sm)] border border-border bg-card">
              <entry.Icon className="h-7 w-7 text-text-primary" />
            </div>
          </div>
          <h2 id="connect-consent-title" className="mt-5 text-2xl font-semibold text-text-primary">
            {isOAuth ? "Connect" : "Add"} {entry.name}
          </h2>
          <p className="mt-1 text-sm text-text-secondary">Developed by {entry.developer}</p>
          <div className="mt-4 flex justify-center">
            <ApprovalBadge label="Approved for this workspace" />
          </div>
        </div>

        <div className="mt-5 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-4">
          <div className="border-b border-border pb-3">
            <h3 className="text-sm font-semibold text-text-primary">Permissions always respected</h3>
            <p className="mt-1 text-sm text-text-secondary">
              OpenClaw is limited to the permissions you grant. You can disconnect this integration anytime.
            </p>
          </div>
          <div className="border-b border-border py-3">
            <h3 className="text-sm font-semibold text-text-primary">You're in control</h3>
            <p className="mt-1 text-sm text-text-secondary">
              Machines in this workspace can use {entry.name} only through the enabled workspace tools.
            </p>
          </div>
          <div className="pt-3">
            <h3 className="text-sm font-semibold text-text-primary">Integrations may introduce risk</h3>
            <p className="mt-1 text-sm text-text-secondary">
              Connected services can expose sensitive workspace data. Review access before continuing.
            </p>
          </div>
        </div>

        {entry.oauthPermissions && (
          <div className="mt-5 rounded-[var(--radius-sm)] border border-border bg-card p-4">
            <h3 className="text-sm font-semibold text-text-primary">Choose access</h3>
            <div className="mt-3 space-y-3">
              {entry.oauthPermissions.map((permission) => (
                <div key={permission.id} className="grid gap-2 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
                  <div>
                    <div className="text-sm font-medium text-text-primary">{permission.label}</div>
                    <div className="mt-0.5 text-xs text-text-tertiary">{permission.description}</div>
                  </div>
                  <div className="inline-flex rounded-[var(--radius-sm)] border border-border bg-card p-1">
                    {permission.options.map((option) => (
                      <button
                        key={option.value}
                        type="button"
                        onClick={() => onPermissionChange(permission.id, option.value)}
                        disabled={busy}
                        title={option.description}
                        className={`rounded-[var(--radius-sm)] px-2.5 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
                          permissionSelection[permission.id] === option.value
                            ? "bg-text-primary text-white"
                            : "text-text-secondary hover:bg-[var(--color-surface-hover)]"
                        }`}
                      >
                        {option.label}
                      </button>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {advancedOpen && (
          <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3 text-sm text-text-secondary">
            <InfoRow label="Transport" value={entry.transport} />
            <div className="mt-2">
              <InfoRow label="Auth" value={entry.authKind} />
            </div>
            {catalogEntryIsCustomMCP(entry) && (
              <div className="mt-2">
                <InfoRow label="Resources/prompts" value="Not connected in v1" />
              </div>
            )}
            <div className="mt-3 space-y-2 border-t border-border pt-3">
              {entry.tools.map((tool) => (
                <div key={tool.name} className="flex items-start justify-between gap-3 text-xs">
                  <span className="font-medium text-text-primary">{tool.name}</span>
                  <ScopePill>{tool.access}</ScopePill>
                </div>
              ))}
            </div>
          </div>
        )}

        <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-end">
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="inline-flex items-center justify-center rounded-[var(--radius-sm)] border border-border px-3 py-2 text-sm font-medium text-text-secondary hover:bg-[var(--color-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onAdvancedToggle}
            disabled={busy}
            className="inline-flex items-center justify-center rounded-[var(--radius-sm)] border border-border px-3 py-2 text-sm font-medium text-text-secondary hover:bg-[var(--color-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            Advanced settings
          </button>
          <button
            type="button"
            onClick={onContinue}
            disabled={busy}
            className="inline-flex items-center justify-center gap-2 rounded-[var(--radius-sm)] bg-text-primary px-4 py-2.5 text-sm font-medium text-white transition-colors hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {isOAuth ? `Continue to ${entry.developer}` : `Add ${entry.name}`}
          </button>
        </div>
    </ModalOverlay>
  );
}

export function WorkspaceIntegrations() {
  const { workspaceId, catalogId } = useParams<{ workspaceId?: string; catalogId?: string }>();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { account, user } = useAuth();
  const [data, setData] = useState<WorkspaceIntegrationsResponse | null>(null);
  const [health, setHealth] = useState<WorkspaceIntegrationHealthResponse | null>(null);
  const [catalogItems, setCatalogItems] = useState<WorkspaceIntegrationCatalogItem[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [members, setMembers] = useState<AccountMember[]>([]);
  const [memberRole, setMemberRole] = useState<AccountMember["role"] | null>(null);
  const [savingCatalogId, setSavingCatalogId] = useState<string | null>(null);
  const [testingCatalogId, setTestingCatalogId] = useState<string | null>(null);
  const [revokingCatalogId, setRevokingCatalogId] = useState<string | null>(null);
  const [policySaving, setPolicySaving] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({});
  const [policyDraft, setPolicyDraft] = useState<{ allowedTools: string[]; deniedTools: string[] }>({ allowedTools: [], deniedTools: [] });
  const [integrationTestResult, setIntegrationTestResult] = useState<Record<string, unknown> | null>(null);
  const [consentEntry, setConsentEntry] = useState<CatalogEntry | null>(null);
  const [advancedConsentOpen, setAdvancedConsentOpen] = useState(false);
  const [permissionSelection, setPermissionSelection] = useState<OAuthPermissionSelection>({});
  const [customProbe, setCustomProbe] = useState<WorkspaceIntegrationProbeResponse | null>(null);
  const [customOAuthScopes, setCustomOAuthScopes] = useState<string[]>([]);
  const [customOAuthClientId, setCustomOAuthClientId] = useState("");
  const [probingCustomMCP, setProbingCustomMCP] = useState(false);
  const [importPreview, setImportPreview] = useState<WorkspaceIntegrationImportPreview | null>(null);
  const [previewingImport, setPreviewingImport] = useState(false);
  const mountedRef = useRef(false);
  const loadRequestSeq = useRef(0);
  const integrationTestRequestSeq = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      loadRequestSeq.current += 1;
      integrationTestRequestSeq.current += 1;
    };
  }, []);

  const load = useCallback(async () => {
    if (!account) return;
    const requestSeq = loadRequestSeq.current + 1;
    loadRequestSeq.current = requestSeq;
    const isCurrent = () => mountedRef.current && loadRequestSeq.current === requestSeq;
    setLoading(true);
    setError(null);
    setNotice(null);
    try {
      const healthSince = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
      const [next, catalog, nextHealth] = await Promise.all([
        listWorkspaceIntegrations(account.id, workspaceId),
        listWorkspaceIntegrationCatalog(account.id, workspaceId).catch(() => null),
        workspaceId ? listWorkspaceIntegrationHealth(account.id, workspaceId, healthSince).catch(() => null) : Promise.resolve(null),
      ]);
      if (isCurrent()) {
        setData(next);
        setCatalogItems(catalog?.integrations ?? null);
        setHealth(nextHealth);
      }
    } catch (err: unknown) {
      if (!isCurrent()) return;
      const message = err instanceof Error ? err.message : "Failed to load workspace integrations.";
      setError(message);
    } finally {
      if (isCurrent()) {
        setLoading(false);
      }
    }
  }, [account, workspaceId]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    if (!account || !user) return;
    let cancelled = false;
    setMembers([]);
    setMemberRole(null);
    listMembers(account.id)
      .then((members) => {
        if (cancelled) return;
        setMembers(members);
        const current = members.find((member) => member.user_id === user.id);
        setMemberRole(current?.role ?? null);
      })
      .catch(() => {
        if (!cancelled) {
          setMembers([]);
          setMemberRole(null);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [account, user]);

  useEffect(() => {
    if ((searchParams.get("google-workspace") ?? searchParams.get("google")) !== "error") return;
    setError(searchParams.get("message") || "Google Workspace connection failed.");
  }, [searchParams]);

  const integrations = Array.isArray(data?.integrations) ? data.integrations : EMPTY_WORKSPACE_INTEGRATIONS;
  const machines = Array.isArray(data?.machines) ? data.machines : EMPTY_WORKSPACE_MACHINES;
  const workspaceName = data?.workspace.name ?? "Workspace";
  const normalizedCatalogItems = Array.isArray(catalogItems) ? catalogItems : null;
  const baseCatalogEntries = useMemo(() => catalogEntriesFromBackend(normalizedCatalogItems, [], CATALOG), [normalizedCatalogItems]);
  const catalogEntries = useMemo(() => catalogEntriesFromBackend(normalizedCatalogItems, integrations, CATALOG), [normalizedCatalogItems, integrations]);
  const selectedEntry = useMemo(() => catalogEntries.find((entry) => entry.id === catalogId) ?? null, [catalogEntries, catalogId]);
  const integrationBySlug = useMemo(() => new Map(integrations.map((integration) => [integration.slug, integration])), [integrations]);
  const selectedIntegrationSlug = catalogEntryConnectionSlug(selectedEntry);
  const selectedIntegration = selectedEntry ? integrationBySlug.get(selectedIntegrationSlug) ?? null : null;
  const selectedIntegrationConnected = isWorkspaceIntegrationConnected(selectedIntegration);
  const configuredCount = integrations.filter(isWorkspaceIntegrationConnected).length;
  const hasConfiguredIntegration = configuredCount > 0;
  const canManage = memberRole === "owner" || memberRole === "admin";
  const configuredMachineCount = machines.filter((machine) => machine.plugin_enabled).length;
  const healthTools = Array.isArray(health?.tools) ? health.tools : [];
  const unhealthyToolCount = healthTools.filter((tool) => tool.error_calls > 0 || tool.success_rate < 0.99).length;

  const addedEntries = useMemo(() => connectedCatalogEntries(catalogEntries, integrations), [catalogEntries, integrations]);

  const filteredCatalog = useMemo(() => {
    const query = searchQuery.trim().toLowerCase();
    return baseCatalogEntries.filter((entry) => {
      if (entry.id === GOOGLE_WORKSPACE_CONNECTION_SLUG) return false;
      if (!query) return true;
      return [
        entry.name,
        entry.description,
        entry.category,
        entry.developer,
        ...entry.tools.flatMap((tool) => [tool.name, tool.description]),
        ...(entry.oauthPermissions ?? []).flatMap((permission) => [permission.label, permission.description]),
      ].some((value) => value.toLowerCase().includes(query));
    });
  }, [baseCatalogEntries, searchQuery]);

  const catalogByCategory = useMemo(() => {
    const groups = new Map<string, CatalogEntry[]>();
    for (const entry of filteredCatalog) {
      const group = groups.get(entry.category) ?? [];
      group.push(entry);
      groups.set(entry.category, group);
    }
    return [...groups.entries()];
  }, [filteredCatalog]);

  useEffect(() => {
    if (!selectedEntry) return;
    setCustomProbe(null);
    setImportPreview(null);
    if (!selectedEntry.fields?.length) {
      setFieldValues({});
      return;
    }
    const current = integrationBySlug.get(selectedEntry.id);
    const [owner = "", repo = ""] = current?.target?.split("/", 2) ?? [];
    setFieldValues((previous) => {
      const next: Record<string, string> = {};
      for (const field of selectedEntry.fields ?? []) {
        if (field.secret) {
          next[field.id] = "";
          continue;
        }
        const fallback = field.id === "owner"
          ? owner
          : field.id === "repo"
            ? repo
            : field.id === "url"
              ? current?.target ?? ""
              : field.id === "auth_type"
                ? "none"
                : "";
        next[field.id] = previous[field.id] || fallback;
      }
      return next;
    });
  }, [integrationBySlug, selectedEntry]);

  useEffect(() => {
    if (catalogEntryIsImport(selectedEntry) && importPreview) {
      setPolicyDraft({
        allowedTools: importPreview.allowed_tools ?? [],
        deniedTools: importPreview.denied_tools ?? [],
      });
      return;
    }
    if (!selectedIntegration) {
      setPolicyDraft({ allowedTools: [], deniedTools: [] });
      return;
    }
    setPolicyDraft({
      allowedTools: selectedIntegration.allowed_tools ?? [],
      deniedTools: selectedIntegration.denied_tools ?? [],
    });
  }, [importPreview, selectedEntry, selectedIntegration]);

  const handlePreviewImport = useCallback(async () => {
    if (!account || !canManage || !selectedEntry || !catalogEntryIsImport(selectedEntry)) return;
    setPreviewingImport(true);
    setError(null);
    setNotice(null);
    setImportPreview(null);
    try {
      const request = workspaceIntegrationImportRequestFromFields(selectedEntry, fieldValues);
      delete request.token;
      const response = await previewWorkspaceIntegrationImport(account.id, request, workspaceId);
      if (!mountedRef.current) return;
      setImportPreview(response);
      setPolicyDraft({
        allowedTools: response.allowed_tools ?? [],
        deniedTools: response.denied_tools ?? [],
      });
      setNotice(`Generated ${response.tools.length} ${response.tools.length === 1 ? "tool" : "tools"} from ${response.display_name}.`);
    } catch (err: unknown) {
      if (!mountedRef.current) return;
      const message = err instanceof Error ? err.message : "Failed to preview imported integration.";
      setError(message);
    } finally {
      if (mountedRef.current) {
        setPreviewingImport(false);
      }
    }
  }, [account, canManage, fieldValues, selectedEntry, workspaceId]);

  const handleSaveImport = useCallback(async (entry: CatalogEntry) => {
    if (!account || !canManage || !catalogEntryIsImport(entry)) return;
    if (!importPreview) {
      throw new Error("Preview the imported integration before saving it.");
    }
    const request = workspaceIntegrationImportRequestFromFields(entry, fieldValues);
    const saved = await createWorkspaceIntegrationImport(account.id, request, workspaceId);
    await updateWorkspaceIntegrationPolicy(account.id, saved.slug, {
      allowed_tools: policyDraft.allowedTools,
      denied_tools: policyDraft.deniedTools,
    }, workspaceId);
    await load();
    if (mountedRef.current) {
      setNotice(`${saved.display_name} connected.`);
      setImportPreview(null);
      navigate(integrationPath(workspaceId, saved.slug), { replace: true });
    }
  }, [account, canManage, fieldValues, importPreview, load, navigate, policyDraft.allowedTools, policyDraft.deniedTools, workspaceId]);

  const executeConnect = useCallback(async (entry: CatalogEntry) => {
    if (!account || !canManage) return;
    setSavingCatalogId(entry.id);
    setError(null);
    setIntegrationTestResult(null);
    try {
      if (catalogEntryIsImport(entry)) {
        await handleSaveImport(entry);
      } else if (catalogEntryIsCustomMCP(entry)) {
        const endpoint = catalogFieldValue(fieldValues, "url") || entry.endpoint || "";
        if (!customProbe || customProbe.url !== endpoint) {
          throw new Error("Probe the remote MCP server before adding it.");
        }
        const probedTools = customMCPToolsFromProbe(customProbe.tools);
        const policy = customMCPInitialPolicy(probedTools);
        const slug = entry.id === CUSTOM_MCP_CATALOG_ID
          ? deriveCustomMCPSlug({
            url: endpoint,
            serverName: customProbe.server.name,
            existingSlugs: [...catalogEntries.map((entry) => entry.id), ...integrations.map((integration) => integration.slug)],
          })
          : entry.id;
        const token = catalogSecret(entry, fieldValues);
        const displayName = customProbe.server.name || (entry.id === CUSTOM_MCP_CATALOG_ID ? endpoint : entry.name);
        await createWorkspaceIntegration(account.id, slug, {
          display_name: displayName,
          kind: "custom-mcp",
          transport: "mcp-remote",
          endpoint,
          tool_manifest: customMCPManifestFromProbe(customProbe.tools),
          config: {
            display_target: endpoint,
            server_name: customProbe.server.name,
            server_version: customProbe.server.version,
            protocol_version: customProbe.protocol_version,
            probed_at: customProbe.probed_at,
          },
          ...(token ? { token, token_type: entry.tokenType ?? "bearer" } : {}),
        }, workspaceId);
        await updateWorkspaceIntegrationPolicy(account.id, slug, {
          allowed_tools: policy.allowedTools,
          denied_tools: policy.deniedTools,
        }, workspaceId);
        await load();
        if (mountedRef.current) {
          setNotice(`${displayName} ${entry.id === CUSTOM_MCP_CATALOG_ID ? "connected" : "updated"}.`);
          setCustomProbe(null);
          navigate(integrationPath(workspaceId, slug), { replace: true });
        }
      } else if (entry.authKind === "bearer") {
        if (!entry.endpoint || !entry.toolManifest) {
          throw new Error(`${entry.name} is missing catalog connection data.`);
        }
        const token = catalogSecret(entry, fieldValues);
        await createWorkspaceIntegration(account.id, entry.id, {
          display_name: catalogDisplayName(entry, fieldValues),
          kind: entry.kind ?? entry.id,
          transport: entry.transport,
          endpoint: entry.endpoint,
          tool_manifest: entry.toolManifest,
          config: catalogConfig(entry, fieldValues),
          ...(token ? { token, token_type: entry.tokenType ?? "bearer" } : {}),
        }, workspaceId);
        await load();
        if (mountedRef.current) {
          setNotice(`${entry.name} connected.`);
          navigate(integrationPath(workspaceId, entry.id), { replace: true });
        }
      } else if (catalogEntryOAuthProviderSlug(entry) === GOOGLE_WORKSPACE_CONNECTION_SLUG) {
        const response = await startWorkspaceIntegrationOAuth(account.id, GOOGLE_WORKSPACE_CONNECTION_SLUG, workspaceId, oauthPayloadForEntry(entry, permissionSelection));
        if (!trustedOAuthRedirect(response.url)) {
          throw new Error("Google OAuth returned an untrusted redirect URL.");
        }
        redirectToWorkspaceIntegrationOAuth(response.url);
      } else {
        throw new Error(`${entry.name} is not available in this workspace yet.`);
      }
    } catch (err: unknown) {
      if (!mountedRef.current) return;
      const message = err instanceof Error ? err.message : `Failed to connect ${entry.name}.`;
      setError(message);
    } finally {
      if (mountedRef.current) {
        setSavingCatalogId(null);
      }
    }
  }, [account, canManage, catalogEntries, customOAuthClientId, customProbe, fieldValues, handleSaveImport, integrations, load, navigate, permissionSelection, workspaceId]);

  const handleFieldValueChange = useCallback((fieldId: string, value: string) => {
    setFieldValues((previous) => ({ ...previous, [fieldId]: value }));
    if (catalogEntryIsCustomMCP(selectedEntry) && fieldId === "url") {
      setCustomProbe(null);
    }
    if (catalogEntryIsImport(selectedEntry)) {
      setImportPreview(null);
    }
  }, [selectedEntry]);

  const handleProbeCustomMCP = useCallback(async () => {
    if (!account || !canManage) return;
    // Generic custom-MCP reads the user-entered URL; curated entries use their
    // fixed catalog endpoint.
    const endpoint = catalogFieldValue(fieldValues, "url") || selectedEntry?.endpoint || "";
    if (!endpoint) {
      setError("Remote MCP URL is required.");
      return;
    }
    setProbingCustomMCP(true);
    setError(null);
    setCustomProbe(null);
    try {
      const token = selectedEntry ? catalogSecret(selectedEntry, fieldValues) : "";
      const response = await probeWorkspaceIntegration(account.id, {
        url: endpoint,
        ...(token ? { token } : {}),
      }, workspaceId);
      if (!mountedRef.current) return;
      setCustomProbe(response);
      setCustomOAuthScopes([]);
      setCustomOAuthClientId("");
      const tools = customMCPToolsFromProbe(response.tools);
      const policy = customMCPInitialPolicy(tools);
      setPolicyDraft(policy);
      if (response.auth_required && response.oauth?.available) {
        setNotice(`OAuth metadata detected for ${response.server.name || endpoint}. Bearer/API-key remains available while generic OAuth connection is finalized.`);
      } else {
        setNotice(`Discovered ${response.tools.length} ${response.tools.length === 1 ? "tool" : "tools"} from ${response.server.name || endpoint}.`);
      }
    } catch (err: unknown) {
      if (!mountedRef.current) return;
      const message = err instanceof Error ? err.message : "Failed to probe remote MCP server.";
      setError(message);
    } finally {
      if (mountedRef.current) {
        setProbingCustomMCP(false);
      }
    }
  }, [account, canManage, fieldValues, selectedEntry, workspaceId]);

  const handleCustomMCPOAuthContinue = useCallback(async () => {
    if (!account || !canManage || !selectedEntry || !customProbe?.oauth?.available) return;
    const endpoint = catalogFieldValue(fieldValues, "url") || selectedEntry.endpoint || "";
    if (!endpoint) {
      setError("Remote MCP URL is required.");
      return;
    }
    // Curated entries keep their catalog slug; only the generic custom-MCP entry
    // derives a slug from the URL/server name.
    const slug = selectedEntry.id === CUSTOM_MCP_CATALOG_ID
      ? deriveCustomMCPSlug({
        url: endpoint,
        serverName: customProbe.server.name,
        existingSlugs: [...catalogEntries.map((entry) => entry.id), ...integrations.map((integration) => integration.slug)],
      })
      : selectedEntry.id;
    setSavingCatalogId(selectedEntry.id);
    setError(null);
    try {
      const response = await startWorkspaceIntegrationOAuth(account.id, slug, workspaceId, {
        url: endpoint,
        display_name: customProbe.server.name || endpoint,
        scopes: customOAuthScopes,
        ...(customProbe.oauth.dynamic_client_registration ? {} : { client_id: customOAuthClientId.trim() }),
      });
      if (!trustedOAuthRedirect(response.url)) {
        throw new Error("Remote MCP OAuth returned an untrusted redirect URL.");
      }
      redirectToWorkspaceIntegrationOAuth(response.url);
    } catch (err: unknown) {
      if (!mountedRef.current) return;
      const message = err instanceof Error ? err.message : "Failed to start remote MCP OAuth.";
      setError(message);
    } finally {
      if (mountedRef.current) {
        setSavingCatalogId(null);
      }
    }
  }, [account, canManage, catalogEntries, customOAuthClientId, customOAuthScopes, customProbe, fieldValues, integrations, selectedEntry, workspaceId]);

  const handleConnect = useCallback((entry: CatalogEntry) => {
    if (!account || !canManage) return;
    setError(null);
    setIntegrationTestResult(null);
    setAdvancedConsentOpen(false);
    setPermissionSelection(permissionSelectionFromEntry(entry, integrationBySlug.get(catalogEntryConnectionSlug(entry))));
    setConsentEntry(entry);
  }, [account, canManage, integrationBySlug]);

  const handleConsentContinue = useCallback((entry: CatalogEntry) => {
    setConsentEntry(null);
    setAdvancedConsentOpen(false);
    void executeConnect(entry);
  }, [executeConnect]);

  const renderConnectModals = () => (
    <>
      {consentEntry && (
        <ConnectConsentModal
          entry={consentEntry}
          busy={savingCatalogId === consentEntry.id}
          advancedOpen={advancedConsentOpen}
          permissionSelection={permissionSelection}
          onAdvancedToggle={() => setAdvancedConsentOpen((value) => !value)}
          onPermissionChange={(serviceId, level) => setPermissionSelection((previous) => ({ ...previous, [serviceId]: level }))}
          onCancel={() => {
            setConsentEntry(null);
            setAdvancedConsentOpen(false);
          }}
          onContinue={() => handleConsentContinue(consentEntry)}
        />
      )}
    </>
  );

  const handleTestIntegration = useCallback(async (entry: CatalogEntry) => {
    if (!account || !canManage) return;
    const requestSeq = integrationTestRequestSeq.current + 1;
    integrationTestRequestSeq.current = requestSeq;
    const isCurrent = () => mountedRef.current && integrationTestRequestSeq.current === requestSeq;
    setTestingCatalogId(entry.id);
    setError(null);
    setIntegrationTestResult(null);
    try {
      const response = await testWorkspaceIntegration(account.id, catalogEntryConnectionSlug(entry), workspaceId);
      if (isCurrent()) {
        setIntegrationTestResult(response.result);
      }
    } catch (err: unknown) {
      if (!isCurrent()) return;
      const message = err instanceof Error ? err.message : `Failed to test ${entry.name} integration.`;
      setError(message);
    } finally {
      if (isCurrent()) {
        setTestingCatalogId(null);
      }
    }
  }, [account, canManage, workspaceId]);

  const handleRevoke = useCallback(async (entry: CatalogEntry) => {
    if (!account || !canManage) return;
    setRevokingCatalogId(entry.id);
    setError(null);
    setIntegrationTestResult(null);
    try {
      await revokeWorkspaceIntegration(account.id, catalogEntryConnectionSlug(entry), workspaceId);
      await load();
      if (mountedRef.current) {
        setNotice(`${entry.name} revoked.`);
        navigate(integrationPath(workspaceId), { replace: true });
      }
    } catch (err: unknown) {
      if (!mountedRef.current) return;
      const message = err instanceof Error ? err.message : `Failed to revoke ${entry.name}.`;
      setError(message);
    } finally {
      if (mountedRef.current) {
        setRevokingCatalogId(null);
      }
    }
  }, [account, canManage, load, navigate, workspaceId]);

  const handlePolicyToggle = useCallback((mode: "allow" | "deny", toolName: string, enabled: boolean) => {
    setPolicyDraft((previous) => {
      if (mode === "deny") {
        return {
          // Deny always wins, so clear any Allow entry for a tool being denied
          // to keep the draft consistent with how the gateway resolves it.
          allowedTools: enabled ? previous.allowedTools.filter((candidate) => candidate !== toolName) : previous.allowedTools,
          deniedTools: updatePolicyTool(previous.deniedTools, toolName, enabled),
        };
      }
      return {
        allowedTools: updatePolicyTool(previous.allowedTools, toolName, enabled),
        deniedTools: previous.deniedTools,
      };
    });
  }, []);

  const handleSavePolicy = useCallback(async () => {
    if (!account || !canManage || !selectedEntry || !selectedIntegration) return;
    setPolicySaving(true);
    setError(null);
    try {
      await updateWorkspaceIntegrationPolicy(account.id, selectedIntegrationSlug, {
        allowed_tools: policyDraft.allowedTools,
        denied_tools: policyDraft.deniedTools,
      }, workspaceId);
      await load();
      if (mountedRef.current) {
        setNotice("Policy saved.");
      }
    } catch (err: unknown) {
      if (!mountedRef.current) return;
      const message = err instanceof Error ? err.message : `Failed to update ${selectedEntry.name} policy.`;
      setError(message);
    } finally {
      if (mountedRef.current) {
        setPolicySaving(false);
      }
    }
  }, [account, canManage, load, policyDraft.allowedTools, policyDraft.deniedTools, selectedEntry, selectedIntegration, selectedIntegrationSlug, workspaceId]);

  if (!account) {
    return <p className="text-[13px] text-text-secondary">Loading account...</p>;
  }

  const renderHeader = (title: string, subtitle?: ReactNode) => (
    <div className="flex flex-col gap-3">
      <div>
        <div className="mb-2 flex items-center gap-2 text-[11px] uppercase tracking-wider text-text-tertiary">
          <span>{account.name}</span>
          {workspaceId && (
            <>
              <span>/</span>
              <Link to={`/workspaces/${workspaceId}`} className="hover:text-text-primary">
                {workspaceName}
              </Link>
            </>
          )}
        </div>
        <h1 className="mt-1 text-2xl font-bold text-text-primary md:text-3xl">{title}</h1>
        {subtitle}
      </div>
    </div>
  );

  const renderError = () => error && (
    <div role="alert" className="flex items-center gap-2 rounded-[var(--radius-sm)] border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-500">
      <AlertCircle className="h-4 w-4 flex-shrink-0" />
      <span>{error}</span>
    </div>
  );

  const renderNotice = () => !error && notice && (
    <div role="status" className="flex items-center gap-2 rounded-[var(--radius-sm)] border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-500">
      <CheckCircle2 className="h-4 w-4 flex-shrink-0" />
      <span>{notice}</span>
    </div>
  );

  if (catalogId) {
    if (!selectedEntry) {
      return (
        <div className="space-y-6">
          {renderHeader("Integration not found")}
          <Link to={integrationPath(workspaceId)} className="inline-flex items-center gap-2 text-sm text-text-secondary hover:text-text-primary">
            <ArrowLeft className="h-4 w-4" />
            Back to catalog
          </Link>
        </div>
      );
    }

    const isCustomMCPEntry = catalogEntryIsCustomMCP(selectedEntry);
    const isImportEntry = catalogEntryIsImport(selectedEntry);
    const previewTools = importPreviewTools(importPreview);
    const detailEntry: CatalogEntry = isCustomMCPEntry && customProbe
      ? {
        ...selectedEntry,
        name: customProbe.server.name || selectedEntry.name,
        developer: customProbe.server.name || selectedEntry.developer,
        description: `Remote MCP server with ${customProbe.tools.length} discovered ${customProbe.tools.length === 1 ? "tool" : "tools"}.`,
        usageSuggestion: "Review the discovered tools, then connect this server for the workspace.",
        tools: customMCPToolsFromProbe(customProbe.tools),
      }
      : isImportEntry && importPreview
        ? {
          ...selectedEntry,
          name: importPreview.display_name || selectedEntry.name,
          description: `Imported ${importPreview.type === "graphql" ? "GraphQL schema" : "OpenAPI spec"} with ${importPreview.tools.length} generated ${importPreview.tools.length === 1 ? "tool" : "tools"}.`,
          usageSuggestion: "Review generated tools, access classification, and policy before saving this workspace connection.",
          tools: previewTools,
        }
      : selectedEntry;
    const Icon = detailEntry.Icon;
    const isConnected = selectedIntegrationConnected;
    const isBusy = savingCatalogId === selectedEntry.id;
    const isRevoking = revokingCatalogId === selectedEntry.id;
    const connectionDisabled = catalogEntryConnectionDisabled(selectedEntry);
    const detailAuthLabel = isImportEntry && importPreview
      ? workspaceIntegrationImportAuthLabel(importPreview.auth_kind)
      : detailEntry.authKind;
    // OAuth servers return 0 tools before auth, so allow submit once a probe found
    // either tools (bearer) or usable OAuth metadata.
    const canSubmitConnection = !connectionDisabled
      && requiredCatalogFieldsPresent(selectedEntry, fieldValues)
      && (!isCustomMCPEntry || Boolean(customProbe?.tools.length) || Boolean(customProbe?.oauth?.available))
      && (!isImportEntry || Boolean(importPreview?.tools.length));
    const snapshotIntegration = selectedIntegration?.kind === "custom-mcp" ? selectedIntegration : null;
    const snapshotStatus = snapshotIntegration ? workspaceIntegrationSnapshotStatus(snapshotIntegration) : null;
    const detailEndpoint = selectedIntegration?.target
      || importPreview?.endpoint
      || detailEntry.endpoint
      || (isCustomMCPEntry ? catalogFieldValue(fieldValues, "url") : "")
      || (isImportEntry ? (catalogFieldValue(fieldValues, "endpoint") || catalogFieldValue(fieldValues, "base_url") || catalogFieldValue(fieldValues, "spec_url")) : "");
    // Once connected, show the REAL discovered tools from the integration manifest
    // (not the catalog's illustrative placeholders) so the policy table lets the
    // admin enable/disable every actual tool by its real name.
    const detailTools = (selectedIntegration?.tools && selectedIntegration.tools.length > 0)
      ? selectedIntegration.tools
      : isImportEntry && importPreview
        ? previewTools
      : detailEntry.tools;
    const approvalLabel = selectedIntegration?.approved_at && selectedIntegration.approved_by_user_id
      ? `Approved by ${memberDisplayName(members, selectedIntegration.approved_by_user_id)}`
      : "Approved by your admin";
    const googleServiceStatuses = googleServiceStatusItems(selectedIntegration);

    return (
      <div className="space-y-6">
        {renderConnectModals()}
        {renderHeader(detailEntry.name, (
          <p className="mt-2 max-w-2xl text-sm text-text-secondary">{detailEntry.description}</p>
        ))}
        {renderError()}
        {renderNotice()}

        <Link to={integrationPath(workspaceId)} className="inline-flex items-center gap-2 text-sm text-text-secondary hover:text-text-primary">
          <ArrowLeft className="h-4 w-4" />
          Back to catalog
        </Link>

        <section className="rounded-[var(--radius-sm)] border border-border bg-card p-4 shadow-card">
          <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
            <div className="flex items-start gap-3">
              <div className="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-[var(--radius-sm)] bg-[var(--color-surface)]">
                <Icon className="h-5 w-5 text-text-primary" />
              </div>
              <div>
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="text-lg font-semibold text-text-primary">{detailEntry.name}</h2>
                  <ConnectorStatus connected={isConnected} />
                  <ApprovalBadge label={approvalLabel} />
                </div>
                <p className="mt-2 text-sm text-text-secondary">{detailEntry.usageSuggestion}</p>
              </div>
            </div>
            <Link
              to="/chat"
              className="inline-flex items-center justify-center rounded-[var(--radius-sm)] border border-border px-3 py-2 text-sm font-medium text-text-secondary hover:bg-[var(--color-surface-hover)]"
            >
              Try in chat
            </Link>
          </div>
        </section>

        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(280px,360px)]">
          <section className="rounded-[var(--radius-sm)] border border-border bg-card p-4 shadow-card">
            <div className="flex items-center justify-between gap-3">
              <h2 className="text-sm font-semibold uppercase tracking-wider text-text-tertiary">Connection</h2>
              <span className="text-xs text-text-tertiary">{detailAuthLabel}</span>
            </div>

            {selectedIntegration?.target && (
              <div className="mt-4 rounded-[var(--radius-sm)] border border-emerald-500/20 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-500">
                Connected as {selectedIntegration.target}
              </div>
            )}

            {selectedIntegration && (
              <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] px-3 py-2 text-xs text-text-secondary">
                <div>
                  Connected by {memberDisplayName(members, selectedIntegration.connected_by_user_id)}
                </div>
                <div className="mt-0.5">
                  {formatConnectionTime(selectedIntegration.connected_at)}
                </div>
              </div>
            )}

            <GoogleServiceStatusPanel statuses={googleServiceStatuses} />

            {snapshotIntegration && snapshotStatus && (
              <div className={`mt-4 rounded-[var(--radius-sm)] border px-3 py-2 text-xs ${
                snapshotStatus.state === "stale"
                  ? "border-amber-500/30 bg-amber-500/10 text-amber-500"
                  : snapshotStatus.state === "fresh"
                    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-500"
                    : "border-border bg-[var(--color-surface)] text-text-secondary"
              }`}>
                <div className="font-medium">Tool snapshot: {snapshotStatus.label}</div>
                {snapshotIntegration.snapshot?.probed_at && (
                  <div className="mt-0.5">Last probed {formatConnectionTime(snapshotIntegration.snapshot.probed_at)}</div>
                )}
                {snapshotIntegration.snapshot?.server_name && (
                  <div className="mt-0.5">
                    {snapshotIntegration.snapshot.server_name}
                    {snapshotIntegration.snapshot.server_version ? ` ${snapshotIntegration.snapshot.server_version}` : ""}
                  </div>
                )}
              </div>
            )}

            {detailEntry.fields && (
              <div className="mt-4 grid gap-3 md:grid-cols-2">
                {detailEntry.fields.map((field) => (
                  <label key={field.id} className={field.id === "token" || field.type === "textarea" ? "block md:col-span-2" : "block"}>
                    <span className="mb-1 block text-[11px] uppercase tracking-wider text-text-tertiary">{field.label}</span>
                    {field.type === "textarea" ? (
                      <textarea
                        value={fieldValues[field.id] ?? ""}
                        onChange={(event) => handleFieldValueChange(field.id, event.target.value)}
                        placeholder={field.placeholder}
                        rows={8}
                        className="w-full rounded-[var(--radius-sm)] border border-border bg-input px-3 py-2 text-sm text-text-primary outline-none focus:border-brand-500"
                      />
                    ) : field.type === "select" ? (
                      <select
                        value={fieldValues[field.id] ?? ""}
                        onChange={(event) => handleFieldValueChange(field.id, event.target.value)}
                        className="w-full rounded-[var(--radius-sm)] border border-border bg-input px-3 py-2 text-sm text-text-primary outline-none focus:border-brand-500"
                      >
                        <option value="">{field.placeholder}</option>
                        {(field.options ?? []).map((option) => (
                          <option key={option.value} value={option.value}>{option.label}</option>
                        ))}
                      </select>
                    ) : (
                      <input
                        type={field.type}
                        value={fieldValues[field.id] ?? ""}
                        onChange={(event) => handleFieldValueChange(field.id, event.target.value)}
                        placeholder={field.id === "token" && isConnected ? "Leave blank to keep existing token" : field.placeholder}
                        className="w-full rounded-[var(--radius-sm)] border border-border bg-input px-3 py-2 text-sm text-text-primary outline-none focus:border-brand-500"
                      />
                    )}
                  </label>
                ))}
              </div>
            )}

            {isCustomMCPEntry && (
              <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <div>
                    <h3 className="text-sm font-semibold text-text-primary">{isConnected ? "Refresh snapshot" : "Probe first"}</h3>
                    <p className="mt-1 text-xs text-text-secondary">
                      {isConnected
                        ? "Re-probe before changing the enabled tool set or refreshing this server."
                        : "OCM checks the server before saving it. Nothing is connected until you continue."}
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={handleProbeCustomMCP}
                    disabled={probingCustomMCP || !(catalogFieldValue(fieldValues, "url") || selectedEntry.endpoint)}
                    className="inline-flex items-center justify-center gap-2 rounded-[var(--radius-sm)] border border-border px-3 py-2 text-sm font-medium text-text-secondary transition-colors hover:bg-[var(--color-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {probingCustomMCP ? <Loader2 className="h-4 w-4 animate-spin" /> : <Search className="h-4 w-4" />}
                    {isConnected ? "Re-probe server" : "Probe server"}
                  </button>
                </div>
                {customProbe && (
                  <div className="mt-3 grid gap-2 border-t border-border pt-3 text-xs text-text-secondary sm:grid-cols-3">
                    <InfoRow label="Server" value={customProbe.server.name || "Remote MCP"} />
                    <InfoRow label="Protocol" value={customProbe.protocol_version || "Unknown"} />
                    <InfoRow label="Discovered" value={`${customProbe.tools.length} tools`} />
                  </div>
                )}
                {customProbe?.oauth?.available && (
                  <div className="mt-3 rounded-[var(--radius-sm)] border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-500">
                    <div className="font-medium">OAuth metadata detected</div>
                    <div className="mt-0.5">
                      {customProbe.oauth.issuer || customProbe.oauth.authorization_server || "Authorization server advertised"}
                      {customProbe.oauth.dynamic_client_registration ? " · dynamic registration available" : ""}
                    </div>
                    {customProbe.oauth.scopes_supported?.length ? (
                      <div className="mt-3 space-y-2 text-amber-600">
                        <div className="font-medium">OAuth scopes</div>
                        <div className="grid gap-1 sm:grid-cols-2">
                          {customProbe.oauth.scopes_supported.map((scope) => (
                            <label key={scope} className="flex items-center gap-2 rounded-[var(--radius-sm)] border border-amber-500/20 bg-card/60 px-2 py-1">
                              <input
                                type="checkbox"
                                checked={customOAuthScopes.includes(scope)}
                                onChange={(event) => {
                                  setCustomOAuthScopes((previous) => (
                                    event.target.checked
                                      ? [...previous, scope]
                                      : previous.filter((candidate) => candidate !== scope)
                                  ));
                                }}
                              />
                              <span className="break-all">{scope}</span>
                            </label>
                          ))}
                        </div>
                      </div>
                    ) : null}
                    {!customProbe.oauth.dynamic_client_registration && (
                      <label className="mt-3 block text-amber-600">
                        <span className="font-medium">OAuth client ID</span>
                        <input
                          type="text"
                          value={customOAuthClientId}
                          onChange={(event) => setCustomOAuthClientId(event.target.value)}
                          placeholder="Pre-registered public client id"
                          className="mt-1 w-full rounded-[var(--radius-sm)] border border-amber-500/30 bg-card px-2 py-1.5 text-xs text-text-primary outline-none transition-colors placeholder:text-text-tertiary focus:border-amber-500"
                        />
                      </label>
                    )}
                    <div className="mt-3 flex flex-wrap items-center gap-2">
                      <button
                        type="button"
                        onClick={handleCustomMCPOAuthContinue}
                        disabled={savingCatalogId === selectedEntry.id || (!customProbe.oauth.dynamic_client_registration && !customOAuthClientId.trim())}
                        className="inline-flex items-center gap-2 rounded-[var(--radius-sm)] border border-amber-500/40 bg-card px-3 py-2 text-xs font-medium text-amber-600 transition-colors hover:bg-amber-500/10 disabled:cursor-not-allowed disabled:opacity-50"
                      >
                        {savingCatalogId === selectedEntry.id ? <Loader2 className="h-3 w-3 animate-spin" /> : <Plug className="h-3 w-3" />}
                        Continue with OAuth
                      </button>
                      <span className="text-amber-600/80">Bearer/API-key remains available.</span>
                    </div>
                  </div>
                )}
              </div>
            )}

            {isImportEntry && (
              <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <div>
                    <h3 className="text-sm font-semibold text-text-primary">{importPreview ? "Preview ready" : "Preview import"}</h3>
                    <p className="mt-1 text-xs text-text-secondary">
                      OCM generates fixed tools from the spec before anything is saved.
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={handlePreviewImport}
                    disabled={previewingImport || !requiredCatalogFieldsPresent(selectedEntry, fieldValues)}
                    className="inline-flex items-center justify-center gap-2 rounded-[var(--radius-sm)] border border-border px-3 py-2 text-sm font-medium text-text-secondary transition-colors hover:bg-[var(--color-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {previewingImport ? <Loader2 className="h-4 w-4 animate-spin" /> : <Search className="h-4 w-4" />}
                    Preview tools
                  </button>
                </div>
                {importPreview && (
                  <div className="mt-3 grid gap-2 border-t border-border pt-3 text-xs text-text-secondary sm:grid-cols-4">
                    <InfoRow label="Import" value={importPreview.type === "graphql" ? "GraphQL" : "OpenAPI"} />
                    <InfoRow label="Auth" value={workspaceIntegrationImportAuthLabel(importPreview.auth_kind)} />
                    <InfoRow label="Endpoint" value={importPreview.endpoint} />
                    <InfoRow label="Generated" value={`${importPreview.tools.length} tools`} />
                  </div>
                )}
                {importPreview?.denied_tools?.length ? (
                  <div className="mt-3 rounded-[var(--radius-sm)] border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600">
                    {importPreview.denied_tools.length} generated write {importPreview.denied_tools.length === 1 ? "tool is" : "tools are"} blocked by default.
                  </div>
                ) : null}
              </div>
            )}

            {detailEntry.authKind === "oauth" && !selectedIntegration?.target && (
              <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] px-3 py-2 text-xs text-text-secondary">
                Uses provider OAuth with offline refresh for workspace-scoped tools.
              </div>
            )}

            <div className="mt-4 flex flex-wrap items-center gap-2">
              {canManage && (
                <button
                  type="button"
                  onClick={() => {
                    if (isImportEntry) {
                      void executeConnect(detailEntry);
                      return;
                    }
                    // Probed an OAuth server (with dynamic registration) and no
                    // bearer token entered → hand off to the provider OAuth flow
                    // instead of a bearer save.
                    if (isCustomMCPEntry && customProbe?.oauth?.available && (customProbe.oauth.dynamic_client_registration || customOAuthClientId.trim()) && !catalogSecret(detailEntry, fieldValues)) {
                      void handleCustomMCPOAuthContinue();
                      return;
                    }
                    handleConnect(detailEntry);
                  }}
                  disabled={isBusy || !canSubmitConnection}
                  className="inline-flex items-center gap-2 rounded-[var(--radius-sm)] bg-brand-600 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {isBusy ? <Loader2 className="h-4 w-4 animate-spin" /> : detailEntry.authKind === "oauth" ? <Plug className="h-4 w-4" /> : <KeyRound className="h-4 w-4" />}
                  {isImportEntry ? "Save import" : isConnected ? `Reconnect ${detailEntry.name}` : `Connect ${detailEntry.name}`}
                </button>
              )}
              {canManage && isConnected && (
                <button
                  type="button"
                  onClick={() => handleTestIntegration(detailEntry)}
                  disabled={testingCatalogId === selectedEntry.id}
                  className="inline-flex items-center gap-2 rounded-[var(--radius-sm)] border border-border px-3 py-2 text-sm font-medium text-text-secondary transition-colors hover:bg-[var(--color-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {testingCatalogId === selectedEntry.id ? <Loader2 className="h-4 w-4 animate-spin" /> : <Wrench className="h-4 w-4" />}
                  Test connection
                </button>
              )}
              {canManage && isConnected && (
                <button
                  type="button"
                  onClick={() => handleRevoke(detailEntry)}
                  disabled={isRevoking}
                  className="inline-flex items-center gap-2 rounded-[var(--radius-sm)] border border-red-500/30 px-3 py-2 text-sm font-medium text-red-500 transition-colors hover:bg-red-500/10 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {isRevoking ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plug className="h-4 w-4" />}
                  Revoke
                </button>
              )}
            </div>

            {integrationTestResult && (
              <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] px-3 py-2 text-sm text-text-primary">
                Last test: {String(integrationTestResult.full_name || integrationTestResult.emailAddress || `${detailEntry.name} reachable`)}
              </div>
            )}
          </section>

          <section className="rounded-[var(--radius-sm)] border border-border bg-card p-4 shadow-card">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-text-tertiary">Information</h2>
            <dl className="mt-4 space-y-3 text-sm">
              <InfoRow label="Capabilities" value={`${detailTools.length} tools`} />
              <InfoRow label="Developer" value={detailEntry.developer} />
              <InfoRow label="Category" value={detailEntry.category} />
              <InfoRow label="Website" value={detailEntry.website} />
              <InfoRow label="Privacy" value={detailEntry.privacy} />
              <InfoRow label="Terms" value={detailEntry.terms} />
            </dl>
            <details className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] px-3 py-2 text-sm text-text-secondary">
              <summary className="cursor-pointer text-sm font-medium text-text-primary">Advanced</summary>
              <dl className="mt-3 space-y-3 border-t border-border pt-3">
                <InfoRow label="Transport" value={detailEntry.transport} />
                <InfoRow label="Auth" value={detailAuthLabel} />
                <InfoRow label="Endpoint" value={detailEndpoint || "Provider managed"} />
                {isCustomMCPEntry && (
                  <InfoRow label="Resources/prompts" value="Not connected in v1" />
                )}
                {selectedIntegration?.scopes?.length ? (
                  <div>
                    <dt className="text-xs uppercase tracking-wider text-text-tertiary">Raw scopes</dt>
                    <dd className="mt-1 break-words text-xs text-text-secondary">{selectedIntegration.scopes.join(", ")}</dd>
                  </div>
                ) : null}
              </dl>
            </details>
          </section>
        </div>

        <section className="rounded-[var(--radius-sm)] border border-border bg-card p-4 shadow-card">
          <h2 className="text-sm font-semibold uppercase tracking-wider text-text-tertiary">Tools</h2>
          <div className="mt-4 grid gap-3 md:grid-cols-2">
            {detailEntry.tools.length === 0 && (
              <div className="rounded-[var(--radius-sm)] border border-dashed border-border bg-[var(--color-surface)] p-4 text-sm text-text-tertiary">
                {isImportEntry ? "Preview the import to review generated tools before saving." : "Probe the server to review tools before connecting."}
              </div>
            )}
            {detailTools.map((tool) => (
              <div key={tool.name} className="rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3">
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-medium text-text-primary">{tool.name}</h3>
                    <p className="mt-1 text-xs text-text-secondary">{tool.description}</p>
                  </div>
                  <div className="flex flex-shrink-0 flex-wrap justify-end gap-1">
                    {tool.source && <ScopePill>{tool.source}</ScopePill>}
                    <ScopePill>{tool.access}</ScopePill>
                    <ScopePill>{tool.mode}</ScopePill>
                  </div>
                </div>
              </div>
            ))}
          </div>
          {canManage && (selectedIntegration || (isImportEntry && importPreview)) && (
            <div className="mt-4 rounded-[var(--radius-sm)] border border-border bg-[var(--color-surface)] p-3">
              <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                <h3 className="text-sm font-semibold text-text-primary">Policy</h3>
                {selectedIntegration && (
                  <button
                    type="button"
                    onClick={handleSavePolicy}
                    disabled={policySaving}
                    className="inline-flex items-center justify-center gap-2 rounded-[var(--radius-sm)] bg-brand-600 px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {policySaving && <Loader2 className="h-4 w-4 animate-spin" />}
                    Save policy
                  </button>
                )}
              </div>
              <p className="mb-3 text-xs text-text-tertiary">
                Deny always wins. With no Allow entries every tool is allowed; adding any Allow entry restricts the
                integration to just the allowed tools.
              </p>
              <div className="overflow-x-auto">
                <table className="w-full min-w-[520px] text-left">
                  <thead>
                    <tr className="text-[11px] uppercase tracking-wider text-text-tertiary">
                      <th className="pb-2 pr-4 font-medium">Tool</th>
                      <th className="pb-2 pr-4 font-medium">Allow</th>
                      <th className="pb-2 pr-4 font-medium">Deny</th>
                      <th className="pb-2 font-medium">Effective</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detailTools.map((tool) => {
                      const denied = policyIncludes(policyDraft.deniedTools, tool.name);
                      const effective = effectivePolicy(policyDraft.allowedTools, policyDraft.deniedTools, tool.name);
                      return (
                        <tr key={tool.name} className="border-t border-border">
                          <td className="py-2 pr-4 text-sm text-text-primary">{tool.name}</td>
                          <td className="py-2 pr-4">
                            <label className={`inline-flex items-center gap-2 text-sm ${denied ? "text-text-tertiary" : "text-text-secondary"}`}>
                              <input
                                type="checkbox"
                                checked={policyIncludes(policyDraft.allowedTools, tool.name)}
                                disabled={denied}
                                onChange={(event) => handlePolicyToggle("allow", tool.name, event.currentTarget.checked)}
                                className="h-4 w-4 rounded border-border bg-input disabled:opacity-50"
                              />
                              Allow
                            </label>
                          </td>
                          <td className="py-2 pr-4">
                            <label className="inline-flex items-center gap-2 text-sm text-text-secondary">
                              <input
                                type="checkbox"
                                checked={denied}
                                onChange={(event) => handlePolicyToggle("deny", tool.name, event.currentTarget.checked)}
                                className="h-4 w-4 rounded border-border bg-input"
                              />
                              Deny
                            </label>
                          </td>
                          <td className="py-2">
                            <span className={`text-xs font-medium ${effective === "Blocked" ? "text-red-500" : "text-emerald-500"}`}>
                              {effective}
                            </span>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </section>

        <MachineRuntimeSection
          loading={loading}
          machines={machines}
          hasConfiguredIntegration={hasConfiguredIntegration}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {renderConnectModals()}
      {renderHeader("Workspace Integrations", (
        <p className="mt-2 max-w-2xl text-sm text-text-secondary">
          Connect apps once. Machines in {workspaceName} inherit them.
        </p>
      ))}
      {renderError()}
      {renderNotice()}

      {workspaceId && (
        <section className="rounded-[var(--radius-sm)] border border-border bg-card p-4 shadow-card">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <h2 className="text-sm font-semibold uppercase tracking-wider text-text-tertiary">Integration health</h2>
              <p className="mt-1 text-sm text-text-secondary">
                Last 24 hours across workspace tools.
              </p>
            </div>
            <div className="flex flex-wrap gap-2 text-xs">
              <ScopePill>{healthTools.length} active tools</ScopePill>
              <ScopePill>{unhealthyToolCount} need attention</ScopePill>
            </div>
          </div>

          {healthTools.length === 0 ? (
            <div className="mt-4 rounded-[var(--radius-sm)] border border-dashed border-border px-4 py-6 text-sm text-text-tertiary">
              No integration tool calls recorded in this window.
            </div>
          ) : (
            <div className="mt-4 overflow-x-auto">
              <table className="w-full min-w-[680px] text-left">
                <thead>
                  <tr className="text-[11px] uppercase tracking-wider text-text-tertiary">
                    <th className="pb-2 pr-4 font-medium">Tool</th>
                    <th className="pb-2 pr-4 font-medium">Calls</th>
                    <th className="pb-2 pr-4 font-medium">Success</th>
                    <th className="pb-2 pr-4 font-medium">P95</th>
                    <th className="pb-2 pr-4 font-medium">Retries</th>
                    <th className="pb-2 font-medium">Top failure</th>
                  </tr>
                </thead>
                <tbody>
                  {healthTools.slice(0, 8).map((tool) => {
                    const topFailure = tool.top_failure_classes?.[0];
                    return (
                      <tr key={tool.tool_id} className="border-t border-border">
                        <td className="py-2 pr-4">
                          <div className="text-sm font-medium text-text-primary">{tool.tool_id}</div>
                          <div className="text-xs text-text-tertiary">{tool.transport} · {tool.access}</div>
                        </td>
                        <td className="py-2 pr-4 text-sm text-text-secondary">{tool.total_calls}</td>
                        <td className="py-2 pr-4 text-sm text-text-secondary">{formatPercent(tool.success_rate)}</td>
                        <td className="py-2 pr-4 text-sm text-text-secondary">{formatLatency(tool.p95_latency_ms)}</td>
                        <td className="py-2 pr-4 text-sm text-text-secondary">{tool.avg_retry_count.toFixed(2)}</td>
                        <td className="py-2 text-sm text-text-secondary">
                          {topFailure ? `${topFailure.class} (${topFailure.count})` : "None"}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      <section className="space-y-4">
        <div className="relative max-w-xl">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-tertiary" />
            <input
              type="search"
              value={searchQuery}
              onChange={(event) => setSearchQuery(event.target.value)}
              placeholder="Search integrations"
              className="w-full rounded-[var(--radius-sm)] border border-border bg-input py-2 pl-9 pr-3 text-sm text-text-primary outline-none focus:border-brand-500"
            />
        </div>

        {addedEntries.length > 0 && (
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <h2 className="text-sm font-semibold text-text-primary">Added</h2>
              <span className="text-xs text-text-tertiary">{addedEntries.length} connected</span>
            </div>
            <div className="divide-y divide-border rounded-[var(--radius-sm)] border border-border bg-card px-4 shadow-card">
              {addedEntries.map(({ integration, entry }) => {
                if (!entry) {
                  return (
                    <Link
                      key={integration.id}
                      to={integrationPath(workspaceId, integration.slug)}
                      className="flex items-center justify-between gap-3 py-4 text-sm text-text-primary hover:text-brand-600"
                    >
                      <span>{integration.display_name}</span>
                      <span className="text-xs text-text-tertiary">Manage</span>
                    </Link>
                  );
                }
                return (
                  <IntegrationCatalogRow
                    key={`${integration.id}:${entry.id}`}
                    entry={entry}
                    connected
                    canManage={canManage}
                    href={integrationPath(workspaceId, entry.id)}
                    onConnect={() => handleConnect(entry)}
                  />
                );
              })}
            </div>
          </div>
        )}

        {catalogByCategory.length === 0 ? (
          <div className="rounded-[var(--radius-sm)] border border-dashed border-border px-4 py-8 text-center text-sm text-text-tertiary">
            No integrations match the current filters.
          </div>
        ) : (
          catalogByCategory.map(([category, entries]) => (
            <div key={category} className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <h2 className="text-sm font-semibold text-text-primary">{category}</h2>
                <span className="text-xs text-text-tertiary">{entries.length} {entries.length === 1 ? "app" : "apps"}</span>
              </div>
              <div className="rounded-[var(--radius-sm)] border border-border bg-card px-4 shadow-card">
                {entries.map((entry) => {
                  const integration = integrationBySlug.get(catalogEntryConnectionSlug(entry));
                  const connected = isWorkspaceIntegrationConnected(integration);
                  const disabled = !connected && catalogEntryConnectionDisabled(entry);
                  return (
                    <IntegrationCatalogRow
                      key={entry.id}
                      entry={entry}
                      connected={connected}
                      canManage={canManage}
                      disabled={disabled}
                      href={integrationPath(workspaceId, entry.id)}
                      onConnect={() => handleConnect(entry)}
                    />
                  );
                })}
              </div>
            </div>
          ))
        )}
      </section>

      <details className="group">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 rounded-[var(--radius-sm)] border border-border bg-card px-4 py-3 text-sm text-text-secondary shadow-card hover:bg-[var(--color-surface-hover)]">
          <span>Workspace status</span>
          <span className="text-xs text-text-tertiary">
            {configuredMachineCount}/{machines.length} machines configured
          </span>
        </summary>
        <div className="mt-3">
          <MachineRuntimeSection
            loading={loading}
            machines={machines}
            hasConfiguredIntegration={hasConfiguredIntegration}
          />
        </div>
      </details>
    </div>
  );
}

function InfoRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <dt className="text-text-tertiary">{label}</dt>
      <dd className="text-right font-medium text-text-primary">{value}</dd>
    </div>
  );
}

function formatPercent(value: number) {
  if (!Number.isFinite(value)) return "0%";
  return `${Math.round(value * 100)}%`;
}

function formatLatency(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0ms";
  if (value >= 1000) return `${(value / 1000).toFixed(1)}s`;
  return `${Math.round(value)}ms`;
}

function MachineRuntimeSection({
  loading,
  machines,
  hasConfiguredIntegration,
}: {
  loading: boolean;
  machines: WorkspaceIntegrationMachineConsumer[];
  hasConfiguredIntegration: boolean;
}) {
  return (
    <section className="rounded-[var(--radius-sm)] border border-border bg-card p-4 shadow-card">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="text-sm font-semibold uppercase tracking-wider text-text-tertiary">Machine assignment</h2>
        <Plug className="h-4 w-4 text-text-tertiary" />
      </div>
      {loading ? (
        <div className="flex items-center gap-2 py-4 text-sm text-text-secondary">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading...
        </div>
      ) : machines.length === 0 ? (
        <p className="py-4 text-sm text-text-tertiary">No machines in this workspace.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[640px] text-left">
            <thead>
              <tr className="text-[11px] uppercase tracking-wider text-text-tertiary">
                <th className="pb-2 pr-4 font-medium">Machine</th>
                <th className="pb-2 pr-4 font-medium">Status</th>
                <th className="pb-2 pr-4 font-medium">Config</th>
                <th className="pb-2 font-medium text-right">Assignment</th>
              </tr>
            </thead>
            <tbody>
              {machines.map((machine) => (
                <MachineRuntimeRow
                  key={machine.machine_id}
                  machine={machine}
                  hasConfiguredIntegration={hasConfiguredIntegration}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
