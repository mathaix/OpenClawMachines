import { dataPlaneUrl, dataPlaneWsUrl } from "./api";
import type { Machine } from "./types";

type MachineInterfaceMachine = Pick<Machine, "id" | "slug"> &
  Partial<Pick<Machine, "kind" | "status" | "gateway_token">>;

function openClawGatewayUrl(
  accountSlug: string | undefined,
  accountId: number | undefined,
  machine: MachineInterfaceMachine,
): string {
  const httpUrl = dataPlaneUrl(accountSlug, machine.slug, "gateway/", accountId, machine.id);
  const wsUrl = dataPlaneWsUrl(accountSlug, machine.slug, "gateway/", accountId, machine.id);
  const params = new URLSearchParams({ gatewayUrl: wsUrl });
  if (machine.gateway_token) {
    params.set("token", machine.gateway_token);
  }
  return `${httpUrl}?${params.toString()}`;
}

export function machineInterfaceUrl(
  accountSlug: string | undefined,
  accountId: number | undefined,
  machine: MachineInterfaceMachine,
): string {
  if (machine.kind === "hermes") {
    return dataPlaneUrl(accountSlug, machine.slug, "dashboard/", accountId, machine.id);
  }

  return openClawGatewayUrl(accountSlug, accountId, machine);
}

export function machineWebchatUrl(
  accountSlug: string | undefined,
  accountId: number | undefined,
  machine: MachineInterfaceMachine,
): string {
  if (machine.kind === "hermes") {
    return dataPlaneUrl(accountSlug, machine.slug, "dashboard/chat", accountId, machine.id);
  }

  return openClawGatewayUrl(accountSlug, accountId, machine);
}

export function canOpenWebchat(machine: MachineInterfaceMachine): boolean {
  if (machine.status !== "running") return false;
  if (machine.kind === "hermes") return true;
  return !!machine.gateway_token;
}
