import React from "react";
import { AbsoluteFill, interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";
import { font, mono, theme } from "../theme";
import { Logo } from "../components/Logo";
import { useReveal } from "../components/ui";

export const Outro: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const logoIn = spring({ frame, fps, config: { damping: 200 } });
  const cta = useReveal(24);
  const repo = useReveal(34);

  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", gap: 8 }}>
      <div style={{ opacity: logoIn, transform: `scale(${interpolate(logoIn, [0, 1], [0.6, 1])})`, filter: `drop-shadow(0 0 30px ${theme.brand}55)` }}>
        <Logo size={120} />
      </div>
      <div style={{ fontFamily: font, fontSize: 72, fontWeight: 800, color: theme.text, marginTop: 20, opacity: logoIn, letterSpacing: -1.5 }}>
        Run agents on <span style={{ color: theme.brand }}>your</span> hardware
      </div>
      <div style={{ ...cta, fontFamily: font, fontSize: 32, color: theme.muted, marginTop: 20 }}>
        Firecracker microVMs · Cloudflare data plane · native MCP tools
      </div>
      <div
        style={{
          ...repo,
          fontFamily: mono,
          fontSize: 28,
          color: theme.brandSoft,
          marginTop: 40,
          border: `1px solid ${theme.brand}55`,
          borderRadius: 12,
          padding: "14px 28px",
          background: `${theme.brand}11`,
        }}
      >
        github.com/mathaix/OpenClawMachines
      </div>
    </AbsoluteFill>
  );
};
