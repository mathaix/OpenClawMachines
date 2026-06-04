import { describe, it, expect } from "vitest";
import { render, screen } from "../../test/test-utils";
import { ChatMessageList } from "./ChatMessageList";
import type { GatewayMessage } from "./useGateway";

describe("ChatMessageList", () => {
  it("shows empty state when no messages", () => {
    render(<ChatMessageList messages={[]} streaming={false} />);
    expect(screen.getByText(/send a message/i)).toBeInTheDocument();
  });

  it("renders messages", () => {
    const messages: GatewayMessage[] = [
      { type: "text", role: "user", content: "Hello", messageId: "m1" },
      { type: "text", role: "assistant", content: "Hi there!", messageId: "m2" },
    ];
    render(<ChatMessageList messages={messages} streaming={false} />);
    expect(screen.getByText("Hello")).toBeInTheDocument();
    expect(screen.getByText(/Hi there/)).toBeInTheDocument();
  });

  it("renders tool call and thinking messages", () => {
    const messages: GatewayMessage[] = [
      { type: "tool_call", role: "assistant", content: "Searching...", toolName: "web_search", messageId: "m1" },
      { type: "thinking", role: "assistant", content: "Analyzing...", thinkingMs: 2000, messageId: "m2" },
    ];
    render(<ChatMessageList messages={messages} streaming={false} />);
    expect(screen.getByText("web_search")).toBeInTheDocument();
    expect(screen.getByText(/2\.0s/)).toBeInTheDocument();
  });
});
