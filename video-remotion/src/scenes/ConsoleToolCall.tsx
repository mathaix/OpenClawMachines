import React from "react";
import { AbsoluteFill } from "remotion";
import { font, mono, theme } from "../theme";
import { useReveal } from "../components/ui";
import { BrowserFrame } from "../components/BrowserFrame";

// The real returned payload captured live from the OpenClaw console.
const RESULT_LINES: [string, string][] = [
  ['{ "result": { "echo": {', theme.muted],
  ['    "message": "hello from the live OpenClaw agent"', theme.green],
  ["  },", theme.muted],
  ['    "integration_slug": "mock-echo" },', theme.brandSoft],
  ['  "tool_id": "mock-echo.echo" }', theme.muted],
];

export const ConsoleToolCall: React.FC<{ chapter?: string }> = ({ chapter = "4" }) => {
  const c = useReveal(4);
  const t = useReveal(10);
  const n = useReveal(18);
  const r = useReveal(40);

  return (
    <AbsoluteFill style={{ flexDirection: "row", alignItems: "center", padding: "0 90px", gap: 48 }}>
      <div style={{ width: 470, flexShrink: 0 }}>
        <div style={{ ...c, display: "flex", alignItems: "center", gap: 14 }}>
          <span style={{ fontFamily: font, fontSize: 20, fontWeight: 800, color: theme.bgDeep, background: theme.violet, borderRadius: 999, width: 40, height: 40, display: "flex", alignItems: "center", justifyContent: "center" }}>{chapter}</span>
          <span style={{ fontFamily: font, fontSize: 22, fontWeight: 700, letterSpacing: 3, textTransform: "uppercase", color: theme.violet }}>Tool call</span>
        </div>
        <div style={{ ...t, fontFamily: font, fontSize: 54, fontWeight: 800, color: theme.text, lineHeight: 1.08, letterSpacing: -1, marginTop: 22 }}>
          The agent calls the tool
        </div>
        <div style={{ ...n, fontFamily: font, fontSize: 27, lineHeight: 1.5, color: theme.muted, marginTop: 24 }}>
          A real Gemini agent runs <span style={{ color: theme.text }}>ocm.search_tools</span> then{" "}
          <span style={{ color: theme.text }}>ocm.call_tool</span> — the gateway executes it and returns live.
        </div>

        {/* real returned payload */}
        <div style={{ ...r, marginTop: 30, background: "#0C111A", border: `1px solid ${theme.panelBorder}`, borderRadius: 14, padding: "18px 20px", boxShadow: "0 20px 50px rgba(0,0,0,0.4)" }}>
          <div style={{ fontFamily: mono, fontSize: 15, color: theme.green, marginBottom: 10, letterSpacing: 1 }}>✓ ocm.call_tool returned</div>
          {RESULT_LINES.map(([txt, col], i) => (
            <div key={i} style={{ fontFamily: mono, fontSize: 17, lineHeight: 1.5, color: col, whiteSpace: "pre" }}>{txt}</div>
          ))}
        </div>
      </div>

      <div style={{ flex: 1, display: "flex", justifyContent: "center", alignItems: "center" }}>
        <BrowserFrame src="shots/console-chat.png" url="workspace / docs-e2e / chat" focus={[0.55, 0.55]} zoom={1.12} width={1130} />
      </div>
    </AbsoluteFill>
  );
};
