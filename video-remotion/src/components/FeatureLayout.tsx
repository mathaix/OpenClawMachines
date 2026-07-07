import React from "react";
import { AbsoluteFill } from "remotion";
import { font, theme } from "../theme";
import { Kicker, useReveal } from "./ui";

export const FeatureLayout: React.FC<{
  index: string;
  kicker: string;
  title: string;
  lines: string[];
  accent: string;
  visual: React.ReactNode;
}> = ({ index, kicker, title, lines, accent, visual }) => {
  const k = useReveal(4);
  const t = useReveal(10);
  const l0 = useReveal(18);
  const l1 = useReveal(24);

  return (
    <AbsoluteFill
      style={{
        flexDirection: "row",
        alignItems: "center",
        padding: "0 130px",
        gap: 80,
      }}
    >
      <div style={{ flex: 1, maxWidth: 720 }}>
        <div style={{ ...k, display: "flex", alignItems: "center", gap: 18 }}>
          <span
            style={{
              fontFamily: font,
              fontSize: 22,
              fontWeight: 800,
              color: theme.bgDeep,
              background: accent,
              borderRadius: 10,
              padding: "4px 12px",
            }}
          >
            {index}
          </span>
          <Kicker color={accent}>{kicker}</Kicker>
        </div>

        <div
          style={{
            ...t,
            fontFamily: font,
            fontSize: 74,
            fontWeight: 800,
            color: theme.text,
            lineHeight: 1.05,
            letterSpacing: -1.5,
            marginTop: 22,
          }}
        >
          {title}
        </div>

        <div style={{ marginTop: 34, display: "flex", flexDirection: "column", gap: 18 }}>
          {[l0, l1].map((style, i) =>
            lines[i] ? (
              <div
                key={i}
                style={{
                  ...style,
                  fontFamily: font,
                  fontSize: 32,
                  lineHeight: 1.4,
                  color: theme.muted,
                  display: "flex",
                  gap: 16,
                }}
              >
                <span style={{ color: accent, fontWeight: 800 }}>—</span>
                <span>{lines[i]}</span>
              </div>
            ) : null,
          )}
        </div>
      </div>

      <div
        style={{
          flex: 1,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          minHeight: 620,
        }}
      >
        {visual}
      </div>
    </AbsoluteFill>
  );
};
