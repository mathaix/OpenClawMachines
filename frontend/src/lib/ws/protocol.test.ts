import { describe, it, expect } from "vitest";
import { parseFrame } from "./protocol";

describe("parseFrame", () => {
  it("parses string frames into type + payload", () => {
    const event = new MessageEvent("message", { data: "shello-session" });
    const frame = parseFrame(event);
    expect(frame.type).toBe("s");
    expect(frame.payload).toBe("hello-session");
  });

  it("parses single-char frames as type with empty payload", () => {
    const event = new MessageEvent("message", { data: "s" });
    const frame = parseFrame(event);
    expect(frame.type).toBe("s");
    expect(frame.payload).toBe("");
  });

  it("returns ignore type for empty string", () => {
    const event = new MessageEvent("message", { data: "" });
    const frame = parseFrame(event);
    expect(frame.type).toBe("");
    expect(frame.payload).toBe("");
  });

  it("returns binary type for ArrayBuffer data", () => {
    const buf = new ArrayBuffer(4);
    const event = new MessageEvent("message", { data: buf });
    const frame = parseFrame(event);
    expect(frame.type).toBe("binary");
    expect(frame.payload).toBe(buf);
  });
});
