import React from "react";
import {
  AbsoluteFill,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { font, theme } from "../theme";

// Spring-based reveal: returns opacity + translateY, staggered by `delay` frames.
export const useReveal = (delay = 0, distance = 26) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const s = spring({
    frame: frame - delay,
    fps,
    config: { damping: 200, mass: 0.7 },
  });
  return {
    opacity: interpolate(s, [0, 1], [0, 1]),
    transform: `translateY(${interpolate(s, [0, 1], [distance, 0])}px)`,
  };
};

// Fade a whole scene in at the start and out near its end.
export const SceneFade: React.FC<{
  durationInFrames: number;
  children: React.ReactNode;
}> = ({ durationInFrames, children }) => {
  const frame = useCurrentFrame();
  const opacity = interpolate(
    frame,
    [0, 12, durationInFrames - 12, durationInFrames],
    [0, 1, 1, 0],
    { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
  );
  return <AbsoluteFill style={{ opacity }}>{children}</AbsoluteFill>;
};

export const Kicker: React.FC<{ children: React.ReactNode; color?: string }> = ({
  children,
  color = theme.brand,
}) => (
  <div
    style={{
      fontFamily: font,
      fontSize: 24,
      fontWeight: 700,
      letterSpacing: 4,
      textTransform: "uppercase",
      color,
    }}
  >
    {children}
  </div>
);

export const Chip: React.FC<{
  children: React.ReactNode;
  color?: string;
  style?: React.CSSProperties;
}> = ({ children, color = theme.muted, style }) => (
  <div
    style={{
      fontFamily: font,
      fontSize: 26,
      fontWeight: 600,
      color: theme.text,
      background: theme.panel,
      border: `1px solid ${theme.panelBorder}`,
      borderRadius: 14,
      padding: "12px 20px",
      display: "flex",
      alignItems: "center",
      gap: 12,
      boxShadow: "0 8px 30px rgba(0,0,0,0.35)",
      ...style,
    }}
  >
    <span
      style={{
        width: 10,
        height: 10,
        borderRadius: 999,
        background: color,
        boxShadow: `0 0 12px ${color}`,
      }}
    />
    {children}
  </div>
);
