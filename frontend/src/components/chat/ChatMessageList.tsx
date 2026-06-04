import { useEffect, useRef } from "react";
import { ChatMessage } from "./ChatMessage";
import type { GatewayMessage } from "./useGateway";

interface ChatMessageListProps {
  messages: GatewayMessage[];
  streaming: boolean;
}

export function ChatMessageList({ messages, streaming }: ChatMessageListProps) {
  const bottomRef = useRef<HTMLDivElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const autoScrollRef = useRef(true);

  // Track whether user has scrolled up
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const handleScroll = () => {
      const { scrollTop, scrollHeight, clientHeight } = container;
      autoScrollRef.current = scrollHeight - scrollTop - clientHeight < 100;
    };

    container.addEventListener("scroll", handleScroll);
    return () => container.removeEventListener("scroll", handleScroll);
  }, []);

  // Auto-scroll on new messages
  useEffect(() => {
    if (autoScrollRef.current && bottomRef.current?.scrollIntoView) {
      bottomRef.current.scrollIntoView({ behavior: "smooth" });
    }
  }, [messages.length, streaming]);

  if (messages.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center text-zinc-500">
        <p className="text-sm">Send a message to start chatting</p>
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      className="flex-1 overflow-y-auto py-4"
    >
      {messages.map((msg, i) => (
        <ChatMessage key={msg.messageId ?? `msg-${i}`} message={msg} />
      ))}
      <div ref={bottomRef} />
    </div>
  );
}
