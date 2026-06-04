import type { WsFrame } from "./protocol";

export type TerminalAction =
  | { kind: "set_session"; sessionId: string }
  | { kind: "write"; data: string; isReplay: boolean }
  | { kind: "ignore" };

export function resolveTerminalAction(
  frame: WsFrame,
  hasContent: boolean,
): TerminalAction {
  switch (frame.type) {
    case "s":
      return { kind: "set_session", sessionId: frame.payload as string };
    case "r":
      return hasContent
        ? { kind: "ignore" }
        : { kind: "write", data: frame.payload as string, isReplay: true };
    case "0":
      return { kind: "write", data: frame.payload as string, isReplay: false };
    default:
      return { kind: "ignore" };
  }
}
