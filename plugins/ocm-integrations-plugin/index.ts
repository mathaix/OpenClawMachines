import type { OpenClawPluginApi } from "openclaw/plugin-sdk";
import { execFileSync } from "node:child_process";

interface OCMIntegrationConfig {
  enabled: boolean;
  apiUrl: string;
  toolsUrl: string;
  machineToken: string;
}

interface GatewayTool {
  name: string;
  description?: string;
  parameters?: Record<string, unknown>;
  integration?: string;
}

function nestedConfig(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const raw = value as Record<string, unknown>;
  if (raw.config && typeof raw.config === "object" && !Array.isArray(raw.config)) {
    return { ...raw, ...(raw.config as Record<string, unknown>) };
  }
  return raw;
}

function redactSensitive(value: string, secrets: string[]): string {
  let redacted = value;
  for (const secret of secrets) {
    if (secret) {
      redacted = redacted.split(secret).join("[REDACTED]");
    }
  }
  return redacted.replace(/Authorization:\s*Bearer\s+[^\s"',)]+/gi, "Authorization: Bearer [REDACTED]");
}

function surfacedErrorMessage(err: unknown, config: OCMIntegrationConfig): string {
  const message = err instanceof Error ? err.message : String(err);
  return redactSensitive(message, [config.machineToken]);
}

function parseConfig(value: unknown): OCMIntegrationConfig {
  const raw = nestedConfig(value);
  const apiUrl = typeof raw.apiUrl === "string" ? raw.apiUrl.trim().replace(/\/+$/, "") : "";
  const toolsUrl =
    typeof raw.toolsUrl === "string" && raw.toolsUrl.trim()
      ? raw.toolsUrl.trim()
      : apiUrl
        ? `${apiUrl}/tools`
        : "";
  return {
    enabled: typeof raw.enabled === "boolean" ? raw.enabled : true,
    apiUrl,
    toolsUrl,
    machineToken: typeof raw.machineToken === "string" ? raw.machineToken.trim() : "",
  };
}

function escapeCurlConfigValue(value: string): string {
  return value.replace(/[\r\n]/g, "").replace(/\\/g, "\\\\").replace(/"/g, "\\\"");
}

function curlAuthConfig(machineToken: string): { args: string[]; input?: string } {
  if (!machineToken) return { args: [] };
  return {
    args: ["-K", "-"],
    input: `header = "Authorization: Bearer ${escapeCurlConfigValue(machineToken)}"\n`,
  };
}

function execGatewayCurlSync(config: OCMIntegrationConfig, args: string[], timeout: number): string {
  const auth = curlAuthConfig(config.machineToken);
  return execFileSync("curl", [
    ...args,
    ...auth.args,
  ], { encoding: "utf-8", timeout, input: auth.input });
}

function fetchToolsSync(config: OCMIntegrationConfig): GatewayTool[] {
  const raw = execGatewayCurlSync(config, [
    config.toolsUrl,
    "-s",
    "-H",
    "Accept: application/json",
  ], 15_000);

  const parsed = JSON.parse(raw);
  return (parsed.tools ?? []) as GatewayTool[];
}

function callToolSync(config: OCMIntegrationConfig, toolName: string, args: Record<string, unknown>): string {
  const url = `${config.apiUrl}/tools/${encodeURIComponent(toolName)}/call`;
  const raw = execGatewayCurlSync(config, [
    url,
    "-s",
    "-X",
    "POST",
    "-H",
    "Content-Type: application/json",
    "-H",
    "Accept: application/json",
    "-d",
    JSON.stringify({ arguments: args }),
  ], 30_000);

  const parsed = JSON.parse(raw);
  if (parsed.error) throw new Error(parsed.error);
  return typeof parsed.result === "string" ? parsed.result : JSON.stringify(parsed.result ?? parsed);
}

const ocmIntegrationsPlugin = {
  id: "ocm-integrations",
  name: "OCM Integrations",
  description: "Legacy REST fallback for workspace-scoped integrations exposed by OCM.",
  configSchema: {
    parse: parseConfig,
  },

  register(api: OpenClawPluginApi) {
    const config = parseConfig(api.pluginConfig);
    if (!config.enabled) {
      api.logger.debug?.("[ocm-integrations] Plugin disabled");
      return;
    }
    if (!config.apiUrl || !config.toolsUrl) {
      api.logger.warn("[ocm-integrations] No backend API URL configured.");
      return;
    }

    let ready = false;
    let toolCount = 0;
    let connectError = "";

    api.on("before_prompt_build", () => ({
      prependSystemContext: ready && toolCount > 0
        ? `<ocm-integrations>
OCM workspace integrations expose external services connected to this workspace through a legacy REST fallback.
Use ocm.search_tools with method "semantic" to find candidate tools from the user's intent, ocm.describe_tool to load one schema, and ocm.call_tool to execute the selected tool through OCM. Prefer tool_address from search results; use tool_id only when it is unambiguous. If policy_state is "require_approval", report that approval is needed instead of calling the tool.
Do not invent or prefer provider-specific tool names; use the ocm search, describe, and call flow.
Do not reveal backend URLs, tokens, or integration credentials.
</ocm-integrations>`
        : ready
          ? `<ocm-integrations>
OCM workspace integrations loaded zero tools.${connectError ? ` Error: ${connectError}` : ""}
Do not pretend external workspace integration tools are available.
</ocm-integrations>`
          : `<ocm-integrations>
OCM workspace integrations are loading. Ask the user to retry shortly if they need an external integration.
</ocm-integrations>`,
    }));

    try {
      const tools = fetchToolsSync(config);
      for (const tool of tools) {
        api.registerTool({
          name: tool.name,
          label: tool.name,
          description: tool.description ?? "",
          parameters: (tool.parameters ?? { type: "object", properties: {} }) as Record<string, unknown>,

          async execute(_toolCallId: string, params: Record<string, unknown>) {
            try {
              const text = callToolSync(config, tool.name, params);
              return {
                content: [{ type: "text" as const, text }],
                details: null,
              };
            } catch (err) {
              const msg = surfacedErrorMessage(err, config);
              return {
                content: [{ type: "text" as const, text: `Error calling ${tool.name}: ${msg}` }],
                details: null,
              };
            }
          },
        });
      }
      toolCount = tools.length;
      ready = true;
      api.logger.info(`[ocm-integrations] Ready - ${toolCount} tools registered`);
    } catch (err) {
      connectError = surfacedErrorMessage(err, config);
      ready = true;
      api.logger.error(`[ocm-integrations] Failed to fetch tools: ${connectError}`);
    }
  },
};

export default ocmIntegrationsPlugin;
