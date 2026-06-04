import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { GatewayMessage } from "./useGateway";

interface ChatMessageProps {
  message: GatewayMessage;
}

export function ChatMessage({ message }: ChatMessageProps) {
  const isUser = message.role === "user";

  if (message.type === "thinking") {
    return (
      <div data-role="assistant" className="flex justify-start px-4 py-1">
        <div className="max-w-[80%] rounded-lg bg-zinc-800/50 px-3 py-2 text-xs text-zinc-400 italic">
          <span className="mr-2">Thinking...</span>
          {message.thinkingMs && (
            <span className="text-zinc-500">
              {(message.thinkingMs / 1000).toFixed(1)}s
            </span>
          )}
          {message.content && (
            <div className="mt-1 text-zinc-500 line-clamp-2">{message.content}</div>
          )}
        </div>
      </div>
    );
  }

  if (message.type === "tool_call") {
    return (
      <div data-role="assistant" className="flex justify-start px-4 py-1">
        <div className="max-w-[80%] rounded-lg bg-zinc-800/50 px-3 py-2">
          <span className="inline-block rounded-full bg-violet-600/20 px-2 py-0.5 text-xs font-medium text-violet-300">
            {message.toolName}
          </span>
          {message.content && (
            <div className="mt-1 text-xs text-zinc-400">{message.content}</div>
          )}
        </div>
      </div>
    );
  }

  if (message.type === "tool_result") {
    return (
      <div data-role="assistant" className="flex justify-start px-4 py-1">
        <div className="max-w-[80%] rounded-lg bg-zinc-800/30 px-3 py-2">
          <pre className="overflow-x-auto text-xs text-zinc-400">
            {message.content}
          </pre>
        </div>
      </div>
    );
  }

  // Default: text message
  return (
    <div
      data-role={message.role}
      className={`flex px-4 py-1.5 ${isUser ? "justify-end" : "justify-start"}`}
    >
      <div
        className={`max-w-[80%] rounded-2xl px-4 py-2 text-sm ${
          isUser
            ? "bg-blue-600 text-white"
            : "bg-zinc-800 text-zinc-200"
        }`}
      >
        {isUser ? (
          <p className="whitespace-pre-wrap">{message.content}</p>
        ) : (
          <div className="prose prose-sm prose-invert max-w-none">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>
              {message.content}
            </ReactMarkdown>
          </div>
        )}
      </div>
    </div>
  );
}
