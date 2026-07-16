import React from "react";
import { AbsoluteFill, interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";
import { font, theme } from "../theme";

export const ChapterCard: React.FC<{ num: string; title: string; accent: string }> = ({ num, title, accent }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const s = spring({ frame, fps, config: { damping: 200 } });
  const numScale = interpolate(s, [0, 1], [0.6, 1]);
  const t = spring({ frame: frame - 8, fps, config: { damping: 200 } });

  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "row", gap: 34 }}>
      <div
        style={{
          fontFamily: font,
          fontSize: 150,
          fontWeight: 800,
          color: accent,
          opacity: s,
          transform: `scale(${numScale})`,
          lineHeight: 1,
          textShadow: `0 0 60px ${accent}55`,
        }}
      >
        {num}
      </div>
      <div
        style={{
          fontFamily: font,
          fontSize: 72,
          fontWeight: 800,
          color: theme.text,
          letterSpacing: -1.5,
          opacity: t,
          transform: `translateX(${interpolate(t, [0, 1], [30, 0])}px)`,
          maxWidth: 700,
          lineHeight: 1.05,
        }}
      >
        {title}
      </div>
    </AbsoluteFill>
  );
};
