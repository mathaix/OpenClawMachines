import React from "react";
import {
  AbsoluteFill,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { font, mono, theme } from "../theme";
import { Logo } from "../components/Logo";

export const Intro: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();

  const logoIn = spring({ frame, fps, config: { damping: 200, mass: 0.9 } });
  const logoScale = interpolate(logoIn, [0, 1], [0.4, 1]);
  // Claw "grips" — opens slightly then closes.
  const grip = 1 + 0.12 * Math.sin(Math.min(frame, 40) / 40 * Math.PI);

  const titleIn = spring({ frame: frame - 18, fps, config: { damping: 200 } });
  const tagIn = spring({ frame: frame - 34, fps, config: { damping: 200 } });

  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center" }}>
      <div
        style={{
          transform: `scale(${logoScale})`,
          opacity: logoIn,
          filter: `drop-shadow(0 0 40px ${theme.brand}66)`,
        }}
      >
        <Logo size={190} gripScale={grip} />
      </div>

      <div
        style={{
          fontFamily: font,
          fontSize: 92,
          fontWeight: 800,
          letterSpacing: -2,
          color: theme.text,
          marginTop: 28,
          opacity: titleIn,
          transform: `translateY(${interpolate(titleIn, [0, 1], [24, 0])}px)`,
        }}
      >
        OpenClaw <span style={{ color: theme.brand }}>Machines</span>
      </div>

      <div
        style={{
          fontFamily: mono,
          fontSize: 32,
          color: theme.muted,
          marginTop: 18,
          opacity: tagIn,
          letterSpacing: 0.5,
        }}
      >
        a self-hosted mini-cloud for AI agents
      </div>
    </AbsoluteFill>
  );
};
