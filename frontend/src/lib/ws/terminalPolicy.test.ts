import { describe, it, expect } from "vitest";
import { resolveTerminalAction } from "./terminalPolicy";
import type { WsFrame } from "./protocol";

const frame = (type: string, payload: string): WsFrame => ({
  type,
  payload,
  raw: new MessageEvent("message", { data: type + payload }),
});

describe("resolveTerminalAction", () => {
  it("returns set_session for 's' frames", () => {
    const action = resolveTerminalAction(frame("s", "abc-123"), false);
    expect(action).toEqual({ kind: "set_session", sessionId: "abc-123" });
  });

  it("returns write with isReplay=true for 'r' frames when no content yet", () => {
    const action = resolveTerminalAction(frame("r", "replay data"), false);
    expect(action).toEqual({ kind: "write", data: "replay data", isReplay: true });
  });

  it("returns ignore for 'r' frames when content already exists", () => {
    const action = resolveTerminalAction(frame("r", "replay data"), true);
    expect(action).toEqual({ kind: "ignore" });
  });

  it("returns write with isReplay=false for '0' frames", () => {
    const action = resolveTerminalAction(frame("0", "hello"), false);
    expect(action).toEqual({ kind: "write", data: "hello", isReplay: false });
  });

  it("returns ignore for unknown frame types", () => {
    const action = resolveTerminalAction(frame("x", "whatever"), false);
    expect(action).toEqual({ kind: "ignore" });
  });
});
