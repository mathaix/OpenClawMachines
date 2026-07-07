import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { theme } from "../theme";

// Dark gradient backdrop with a slow-drifting dot grid and a soft brand glow.
export const Background: React.FC<{ glow?: string }> = ({
  glow = theme.brand,
}) => {
  const frame = useCurrentFrame();
  const drift = (frame * 0.15) % 40;

  return (
    <AbsoluteFill
      style={{
        background: `radial-gradient(120% 90% at 50% 0%, ${theme.bg} 0%, ${theme.bgDeep} 70%)`,
      }}
    >
      <AbsoluteFill
        style={{
          backgroundImage: `radial-gradient(${theme.panelBorder} 1px, transparent 1px)`,
          backgroundSize: "40px 40px",
          backgroundPosition: `${drift}px ${drift}px`,
          opacity: 0.35,
          maskImage:
            "radial-gradient(80% 70% at 50% 45%, black 40%, transparent 100%)",
          WebkitMaskImage:
            "radial-gradient(80% 70% at 50% 45%, black 40%, transparent 100%)",
        }}
      />
      <AbsoluteFill
        style={{
          background: `radial-gradient(45% 40% at 50% 42%, ${glow}22 0%, transparent 70%)`,
        }}
      />
    </AbsoluteFill>
  );
};
