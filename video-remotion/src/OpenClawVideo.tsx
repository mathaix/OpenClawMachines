import React from "react";
import { AbsoluteFill, Series } from "remotion";
import { theme } from "./theme";
import { Background } from "./components/Background";
import { SceneFade } from "./components/ui";
import { FeatureLayout } from "./components/FeatureLayout";
import { Intro } from "./scenes/Intro";
import { Outro } from "./scenes/Outro";
import {
  BrowserVMVisual,
  ChatTerminalVisual,
  ControlPlaneVisual,
  DataPlaneVisual,
  MCPVisual,
  MicroVMVisual,
} from "./components/visuals";

const INTRO = 105;
const FEAT = 120;
const OUTRO = 150;

const FEATURES = [
  {
    index: "01",
    kicker: "Isolation",
    title: "One microVM per agent",
    lines: [
      "Every agent runs in its own Firecracker microVM — its own kernel, hardware-isolated by KVM.",
      "Run many untrusted agents safely, side by side.",
    ],
    accent: theme.brand,
    visual: <MicroVMVisual />,
  },
  {
    index: "02",
    kicker: "The brain",
    title: "A control plane you own",
    lines: [
      "Accounts, machines, hosts, and config — placement and full lifecycle orchestration.",
      "Enroll your own Linux boxes; worker agents boot and stop VMs on demand.",
    ],
    accent: theme.brand,
    visual: <ControlPlaneVisual />,
  },
  {
    index: "03",
    kicker: "Work with it",
    title: "Web chat + live terminal",
    lines: [
      "Talk to the agent, watch its tool activity, and drop into a real shell inside the VM.",
      "Sessions persist across reconnects.",
    ],
    accent: theme.brandSoft,
    visual: <ChatTerminalVisual />,
  },
  {
    index: "04",
    kicker: "Browsing",
    title: "Browser VMs, live",
    lines: [
      "A separate microVM runs headful Chromium with a live view.",
      "The agent drives it over CDP while you watch.",
    ],
    accent: theme.blue,
    visual: <BrowserVMVisual />,
  },
  {
    index: "05",
    kicker: "Data plane",
    title: "Routed at the edge",
    lines: [
      "Every VM gets its own subdomain and a Cloudflare Tunnel that terminates inside the VM.",
      "Auth is enforced at the edge and again in-VM.",
    ],
    accent: theme.blue,
    visual: <DataPlaneVisual />,
  },
  {
    index: "06",
    kicker: "Tools",
    title: "Native MCP integrations",
    lines: [
      "Connect GitHub, Google, or any OpenAPI / GraphQL / MCP endpoint once per workspace.",
      "The agent discovers and calls them through one built-in MCP server.",
    ],
    accent: theme.violet,
    visual: <MCPVisual />,
  },
];

export const OpenClawVideo: React.FC = () => {
  return (
    <AbsoluteFill style={{ backgroundColor: theme.bgDeep }}>
      <Series>
        <Series.Sequence durationInFrames={INTRO}>
          <Background glow={theme.brand} />
          <SceneFade durationInFrames={INTRO}>
            <Intro />
          </SceneFade>
        </Series.Sequence>

        {FEATURES.map((f) => (
          <Series.Sequence key={f.index} durationInFrames={FEAT}>
            <Background glow={f.accent} />
            <SceneFade durationInFrames={FEAT}>
              <FeatureLayout {...f} />
            </SceneFade>
          </Series.Sequence>
        ))}

        <Series.Sequence durationInFrames={OUTRO}>
          <Background glow={theme.brand} />
          <SceneFade durationInFrames={OUTRO}>
            <Outro />
          </SceneFade>
        </Series.Sequence>
      </Series>
    </AbsoluteFill>
  );
};

export const TOTAL_FRAMES = INTRO + FEATURES.length * FEAT + OUTRO;
