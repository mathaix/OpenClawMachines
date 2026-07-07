import React from "react";
import { AbsoluteFill, Series, useVideoConfig } from "remotion";
import { theme } from "./theme";
import { Background } from "./components/Background";
import { SceneFade } from "./components/ui";
import { DemoScene } from "./components/DemoScene";
import { ConsoleToolCall } from "./scenes/ConsoleToolCall";
import { ChapterCard } from "./scenes/ChapterCard";
import { HostProvisioning } from "./scenes/HostProvisioning";
import { Intro } from "./scenes/Intro";
import { Outro } from "./scenes/Outro";

const A_ORANGE = theme.brand;
const A_VIOLET = theme.violet;

const D = {
  intro: 90,
  chapter: 48,
  hostProvisioning: 115,
  dashboard: 80,
  newMachine: 105,
  provisioned: 90,
  runningVm: 115,
  workspace: 78,
  integrations: 130,
  toolCall: 175,
  outro: 120,
};

// Background + fade wrapper that reads its own (sequence) duration.
const Scene: React.FC<{ glow: string; children: React.ReactNode }> = ({ glow, children }) => {
  const { durationInFrames } = useVideoConfig();
  return (
    <AbsoluteFill>
      <Background glow={glow} />
      <SceneFade durationInFrames={durationInFrames}>{children}</SceneFade>
    </AbsoluteFill>
  );
};

export const DemoVideo: React.FC = () => {
  return (
    <AbsoluteFill style={{ backgroundColor: theme.bgDeep }}>
      <Series>
        <Series.Sequence durationInFrames={D.intro}>
          <Scene glow={A_ORANGE}>
            <Intro />
          </Scene>
        </Series.Sequence>

        <Series.Sequence durationInFrames={D.chapter}>
          <Scene glow={A_ORANGE}>
            <ChapterCard num="1" title="Spin up an instance" accent={A_ORANGE} />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.hostProvisioning}>
          <Scene glow={A_ORANGE}>
            <HostProvisioning chapter="1" accent={A_ORANGE} />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.dashboard}>
          <Scene glow={A_ORANGE}>
            <DemoScene
              chapter="1"
              step="Dashboard"
              title="Your fleet of agents"
              note="Every machine is one AI agent in its own Firecracker microVM."
              accent={A_ORANGE}
              src="shots/01-dashboard.png"
              url="localhost / dashboard"
              focus={[0.5, 0.25]}
              zoom={1.08}
            />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.newMachine}>
          <Scene glow={A_ORANGE}>
            <DemoScene
              chapter="1"
              step="New machine"
              title="Name it, size it, create"
              note="Pick OpenClaw, a size, and a runtime version — the control plane places it on the ready host."
              accent={A_ORANGE}
              src="shots/02-new-machine.png"
              url="localhost / dashboard"
              focus={[0.5, 0.45]}
              zoom={1.12}
            />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.provisioned}>
          <Scene glow={A_ORANGE}>
            <DemoScene
              chapter="1"
              step="Provisioned"
              title="demo-agent is live"
              note="The selected host stages the rootfs, starts Firecracker, and wires the gateway for this machine."
              accent={A_ORANGE}
              src="shots/03-created.png"
              url="localhost / dashboard"
              focus={[0.5, 0.22]}
              zoom={1.1}
            />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.runningVm}>
          <Scene glow={theme.green}>
            <DemoScene
              chapter="1"
              step="Running VM"
              title="Open the VM after boot"
              note="After the spin-up finishes, you can enter the live terminal inside the running Firecracker microVM."
              accent={theme.green}
              src="shots/console-shell.png"
              url="workspace / docs-e2e / terminal"
              focus={[0.5, 0.2]}
              zoom={1.1}
            />
          </Scene>
        </Series.Sequence>

        <Series.Sequence durationInFrames={D.chapter}>
          <Scene glow={A_VIOLET}>
            <ChapterCard num="2" title="Connect MCP tools" accent={A_VIOLET} />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.workspace}>
          <Scene glow={A_VIOLET}>
            <DemoScene
              chapter="2"
              step="Workspaces"
              title="Tools live in a workspace"
              note="Connect an app once; every machine in the workspace inherits it."
              accent={A_VIOLET}
              src="shots/05-workspace.png"
              url="localhost / workspaces / default"
              focus={[0.5, 0.3]}
              zoom={1.08}
            />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.integrations}>
          <Scene glow={A_VIOLET}>
            <DemoScene
              chapter="2"
              step="Integrations"
              title="One catalog of MCP tools"
              note="GitHub, Google, or any OpenAPI / GraphQL / remote-MCP endpoint — added with a click."
              accent={A_VIOLET}
              src="shots/06-integrations.png"
              url="localhost / workspaces / default / integrations"
              focus={[0.5, 0.5]}
              zoom={1.14}
            />
          </Scene>
        </Series.Sequence>

        <Series.Sequence durationInFrames={D.chapter}>
          <Scene glow={A_VIOLET}>
            <ChapterCard num="3" title="The running agent uses them" accent={A_VIOLET} />
          </Scene>
        </Series.Sequence>
        <Series.Sequence durationInFrames={D.toolCall}>
          <Scene glow={A_VIOLET}>
            <ConsoleToolCall />
          </Scene>
        </Series.Sequence>

        <Series.Sequence durationInFrames={D.outro}>
          <Scene glow={A_ORANGE}>
            <Outro />
          </Scene>
        </Series.Sequence>
      </Series>
    </AbsoluteFill>
  );
};

export const DEMO_FRAMES =
  D.intro +
  D.chapter +
  D.hostProvisioning +
  D.dashboard +
  D.newMachine +
  D.provisioned +
  D.runningVm +
  D.chapter +
  D.workspace +
  D.integrations +
  D.chapter +
  D.toolCall +
  D.outro;
