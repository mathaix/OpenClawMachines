import { describe, expect, it } from "vitest";
import { canOpenWebchat, machineInterfaceUrl, machineWebchatUrl } from "./machineInterface";

describe("machineInterfaceUrl", () => {
  it("routes Hermes machines to the native dashboard", () => {
    const url = machineInterfaceUrl("team", 1, {
      id: "machine-1",
      slug: "hermes-one",
      kind: "hermes",
      gateway_token: "gw-token",
    });

    expect(url).toBe("/api/accounts/1/machines/machine-1/dashboard/");
  });

  it("routes Hermes webchat to the native chat route", () => {
    const url = machineWebchatUrl("team", 1, {
      id: "machine-1",
      slug: "hermes-one",
      kind: "hermes",
    });

    expect(url).toBe("/api/accounts/1/machines/machine-1/dashboard/chat");
  });

  it("routes OpenClaw machines to the gateway with websocket and auth params", () => {
    const url = machineInterfaceUrl("team", 1, {
      id: "machine-1",
      slug: "openclaw-one",
      kind: "openclaw",
      gateway_token: "gw-token",
    });

    expect(url).toContain("/api/accounts/1/machines/machine-1/gateway/?");
    expect(url).toContain("gatewayUrl=");
    expect(url).toContain(encodeURIComponent("/api/accounts/1/machines/machine-1/gateway/"));
    expect(url).toContain("token=gw-token");
  });

  it("allows running Hermes webchat without a gateway token", () => {
    expect(canOpenWebchat({ id: "machine-1", slug: "hermes-one", kind: "hermes", status: "running" })).toBe(true);
  });

  it("requires an OpenClaw gateway token for webchat", () => {
    expect(canOpenWebchat({ id: "machine-1", slug: "openclaw-one", kind: "openclaw", status: "running" })).toBe(false);
    expect(canOpenWebchat({ id: "machine-1", slug: "openclaw-one", kind: "openclaw", status: "running", gateway_token: "gw" })).toBe(true);
  });
});
