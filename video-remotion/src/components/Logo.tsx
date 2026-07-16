import React from "react";
import { theme } from "../theme";

// OpenClaw Machines mark — the exact single compound path from the repo
// (docs/branding/candidates/r5-claw-grip.svg): an asymmetric machine claw
// gripping a microVM.
const CLAW_PATH = `
  M 22 3 L 12 3 L 3 12 L 3 36 L 12 45 L 36 45 L 45 36 L 45 29
  L 38 29 L 38 33.2 L 33.2 38 L 31 38 L 27 34.5 L 23 38 L 14.8 38
  L 10 33.2 L 10 14.8 L 14.8 10 L 22 10 Z
  M 26 3 L 37 3 L 45 11 L 45 25 L 38 25 L 38 23.5 L 34.5 21.5 L 34.5 18.5
  L 38 16.5 L 38 13.8 L 34.2 10 L 26 10 Z
  M 21.5 17.5 H 26.5 A 4 4 0 0 1 30.5 21.5 V 26.5 A 4 4 0 0 1 26.5 30.5
  H 21.5 A 4 4 0 0 1 17.5 26.5 V 21.5 A 4 4 0 0 1 21.5 17.5 Z
`;

export const Logo: React.FC<{
  size?: number;
  color?: string;
  gripScale?: number; // 1 = closed grip, >1 = claw opening
}> = ({ size = 120, color = theme.brand, gripScale = 1 }) => {
  return (
    <svg width={size} height={size} viewBox="0 0 48 48">
      <g style={{ transformOrigin: "24px 24px", transform: `scale(${gripScale})` }}>
        <path fill={color} d={CLAW_PATH} />
      </g>
    </svg>
  );
};
