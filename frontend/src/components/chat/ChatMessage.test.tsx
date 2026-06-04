import { describe, it, expect } from "vitest";
import { render, screen } from "../../test/test-utils";
import { ChatMessage } from "./ChatMessage";
import type { GatewayMessage } from "./useGateway";

describe("ChatMessage", () => {
  it("renders user message right-aligned", () => {
    const msg: GatewayMessage = {
      type: "text",
      role: "user",
      content: "Hello bot",
    };
    render(<ChatMessage message={msg} />);
    expect(screen.getByText("Hello bot")).toBeInTheDocument();
    // User messages should be on the right
    const bubble = screen.getByText("Hello bot").closest("[data-role]");
    expect(bubble?.getAttribute("data-role")).toBe("user");
  });

  it("renders assistant message left-aligned", () => {
    const msg: GatewayMessage = {
      type: "text",
      role: "assistant",
      content: "Hi there!",
    };
    render(<ChatMessage message={msg} />);
    expect(screen.getByText(/Hi there/)).toBeInTheDocument();
    const bubble = screen.getByText(/Hi there/).closest("[data-role]");
    expect(bubble?.getAttribute("data-role")).toBe("assistant");
  });

  it("renders tool call with tool name badge", () => {
    const msg: GatewayMessage = {
      type: "tool_call",
      role: "assistant",
      content: "Searching for weather data...",
      toolName: "web_search",
    };
    render(<ChatMessage message={msg} />);
    expect(screen.getByText("web_search")).toBeInTheDocument();
  });

  it("renders thinking block with elapsed time", () => {
    const msg: GatewayMessage = {
      type: "thinking",
      role: "assistant",
      content: "Let me analyze this...",
      thinkingMs: 3500,
    };
    render(<ChatMessage message={msg} />);
    expect(screen.getByText(/3\.5s/)).toBeInTheDocument();
  });

  it("renders markdown content", () => {
    const msg: GatewayMessage = {
      type: "text",
      role: "assistant",
      content: "Here is **bold** text",
    };
    render(<ChatMessage message={msg} />);
    const bold = screen.getByText("bold");
    expect(bold.tagName).toBe("STRONG");
  });
});
