import React from "react";
import { AbsoluteFill } from "remotion";
import { mono, theme } from "../theme";
import { useReveal } from "./ui";

type Line = { t: string; c?: string; comment?: boolean };

// Real output captured from the running gateway: ocm.search_tools -> ocm.call_tool
// against the mock-echo integration.
const LINES: Line[] = [
  { t: "# the agent finds a tool by intent", comment: true },
  { t: "$ ocm.search_tools  \"create a GitHub issue\"", c: theme.text },
  { t: "  → mock-echo.echo   access: read   policy: allow", c: theme.green },
  { t: "", },
  { t: "# then calls it by address", comment: true },
  { t: "$ ocm.call_tool  mock-echo.echo  {\"message\":\"create an issue\"}", c: theme.text },
  { t: "  {", c: theme.muted },
  { t: "    \"result\": { \"echo\": { \"message\": \"create an issue\" } },", c: theme.brandSoft },
  { t: "    \"status\": \"ok\"", c: theme.green },
  { t: "  }", c: theme.muted },
];

const PER_LINE = 12; // frames per line reveal

export const Terminal: React.FC<{ frame: number }> = ({ frame }) => {
  const shown = Math.floor(frame / PER_LINE);
  const cursorOn = frame % 20 < 11;
  const header = useReveal(2);

  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
      <div
        style={{
          ...header,
          width: 1180,
          borderRadius: 16,
          overflow: "hidden",
          border: `1px solid ${theme.panelBorder}`,
          boxShadow: "0 40px 120px rgba(0,0,0,0.6)",
          background: "#0C111A",
        }}
      >
        <div style={{ height: 50, background: "#1b2431", display: "flex", alignItems: "center", gap: 10, padding: "0 18px" }}>
          {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
            <span key={c} style={{ width: 13, height: 13, borderRadius: 999, background: c }} />
          ))}
          <span style={{ marginLeft: 14, fontFamily: mono, fontSize: 16, color: theme.muted }}>
            agent → ocm · native MCP gateway
          </span>
        </div>
        <div style={{ padding: "34px 40px", fontFamily: mono, fontSize: 27, lineHeight: 1.62, minHeight: 430 }}>
          {LINES.map((l, i) => {
            if (i > shown) return <div key={i} style={{ height: l.t === "" ? 20 : 44 }} />;
            const isCurrent = i === shown;
            return (
              <div
                key={i}
                style={{
                  color: l.comment ? theme.faint : l.c || theme.text,
                  fontStyle: l.comment ? "italic" : "normal",
                  whiteSpace: "pre",
                  height: l.t === "" ? 20 : 44,
                }}
              >
                {l.t}
                {isCurrent && cursorOn ? <span style={{ color: theme.brand }}>▋</span> : null}
              </div>
            );
          })}
        </div>
      </div>
    </AbsoluteFill>
  );
};
