import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "../../test/test-utils";
import { ChatInput } from "./ChatInput";

describe("ChatInput", () => {
  it("sends message on Enter", () => {
    const onSend = vi.fn();
    render(<ChatInput onSend={onSend} onAbort={vi.fn()} disabled={false} streaming={false} />);

    const input = screen.getByPlaceholderText(/message/i);
    fireEvent.change(input, { target: { value: "Hello" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onSend).toHaveBeenCalledWith("Hello");
  });

  it("does not send empty message", () => {
    const onSend = vi.fn();
    render(<ChatInput onSend={onSend} onAbort={vi.fn()} disabled={false} streaming={false} />);

    const input = screen.getByPlaceholderText(/message/i);
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onSend).not.toHaveBeenCalled();
  });

  it("allows Shift+Enter for newline", () => {
    const onSend = vi.fn();
    render(<ChatInput onSend={onSend} onAbort={vi.fn()} disabled={false} streaming={false} />);

    const input = screen.getByPlaceholderText(/message/i);
    fireEvent.change(input, { target: { value: "Hello" } });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });

    expect(onSend).not.toHaveBeenCalled();
  });

  it("shows abort button when streaming", () => {
    render(<ChatInput onSend={vi.fn()} onAbort={vi.fn()} disabled={false} streaming={true} />);
    expect(screen.getByRole("button", { name: /stop/i })).toBeInTheDocument();
  });

  it("disables input when disabled", () => {
    render(<ChatInput onSend={vi.fn()} onAbort={vi.fn()} disabled={true} streaming={false} />);
    expect(screen.getByPlaceholderText(/message/i)).toBeDisabled();
  });
});
