import React from "react";
import {
  AbsoluteFill,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { font, mono, theme } from "../theme";
import { useReveal } from "../components/ui";

const card: React.CSSProperties = {
  background: theme.panel,
  border: `1px solid ${theme.panelBorder}`,
  borderRadius: 16,
  boxShadow: "0 28px 90px rgba(0,0,0,0.48)",
};

const StatusPill: React.FC<{ label: string; color: string; active: boolean }> = ({
  label,
  color,
  active,
}) => (
  <div
    style={{
      display: "flex",
      alignItems: "center",
      gap: 10,
      height: 42,
      padding: "0 14px",
      borderRadius: 12,
      background: active ? `${color}18` : "#0C111A",
      border: `1px solid ${active ? `${color}77` : theme.panelBorder}`,
      opacity: active ? 1 : 0.45,
    }}
  >
    <span
      style={{
        width: 9,
        height: 9,
        borderRadius: 999,
        background: color,
        boxShadow: active ? `0 0 12px ${color}` : "none",
      }}
    />
    <span style={{ fontFamily: mono, fontSize: 15, color: active ? theme.text : theme.muted }}>
      {label}
    </span>
  </div>
);

const HostProvisioningVisual: React.FC<{ accent: string }> = ({ accent }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const ready = spring({ frame: frame - 42, fps, config: { damping: 200 } });
  const packet = interpolate(frame % 75, [0, 75], [0, 1]);
  const scan = interpolate(frame % 100, [0, 100], [0, 100]);

  const steps = [
    { label: "installer started", at: 14, color: accent },
    { label: "host enrolled", at: 34, color: theme.blue },
    { label: "ocm-agent heartbeat", at: 52, color: theme.green },
    { label: "KVM capacity ready", at: 70, color: theme.green },
  ];

  return (
    <div style={{ position: "relative", width: 1120, height: 660 }}>
      <svg width={1120} height={660} style={{ position: "absolute", inset: 0 }}>
        <path
          d="M330 300 C480 210 585 210 730 300"
          stroke={`${accent}88`}
          strokeWidth={3}
          fill="none"
          strokeDasharray="10 10"
          strokeDashoffset={-(frame * 2.2)}
        />
        <path
          d="M730 362 C580 460 470 460 330 362"
          stroke={`${theme.blue}88`}
          strokeWidth={3}
          fill="none"
          strokeDasharray="10 10"
          strokeDashoffset={frame * 2.2}
        />
        <circle
          cx={interpolate(packet, [0, 1], [330, 730])}
          cy={interpolate(packet, [0, 1], [300, 300]) - Math.sin(packet * Math.PI) * 74}
          r={8}
          fill={accent}
          opacity={0.9}
        />
        <circle
          cx={interpolate(packet, [0, 1], [730, 330])}
          cy={interpolate(packet, [0, 1], [362, 362]) + Math.sin(packet * Math.PI) * 86}
          r={8}
          fill={theme.blue}
          opacity={0.9}
        />
      </svg>

      <div
        style={{
          ...card,
          position: "absolute",
          left: 0,
          top: 150,
          width: 330,
          height: 360,
          padding: 26,
          borderColor: `${accent}66`,
        }}
      >
        <div style={{ fontFamily: mono, fontSize: 18, color: accent, letterSpacing: 1.5 }}>
          control-plane
        </div>
        <div
          style={{
            fontFamily: font,
            fontSize: 35,
            fontWeight: 800,
            color: theme.text,
            marginTop: 12,
          }}
        >
          OpenClaw control plane
        </div>
        <div style={{ fontFamily: mono, fontSize: 17, color: theme.muted, marginTop: 18 }}>
          account: demo
        </div>
        <div style={{ fontFamily: mono, fontSize: 17, color: theme.muted, marginTop: 8 }}>
          region: local-kvm
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, marginTop: 34 }}>
          <StatusPill label="hosts" color={accent} active />
          <StatusPill label="images" color={theme.blue} active />
          <StatusPill label="routes" color={theme.blue} active={frame > 42} />
          <StatusPill label="VMs" color={theme.green} active={frame > 64} />
        </div>
      </div>

      <div
        style={{
          ...card,
          position: "absolute",
          right: 0,
          top: 70,
          width: 390,
          height: 540,
          padding: 26,
          borderColor: `${theme.green}66`,
          transform: `scale(${interpolate(ready, [0, 1], [0.97, 1])})`,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 14 }}>
          <span
            style={{
              width: 52,
              height: 52,
              borderRadius: 14,
              background: "#0C111A",
              border: `1px solid ${theme.panelBorder}`,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              color: theme.green,
              fontFamily: mono,
              fontSize: 26,
              fontWeight: 800,
            }}
          >
            $_
          </span>
          <div>
            <div style={{ fontFamily: mono, fontSize: 17, color: theme.green }}>
              ubuntu-host-01
            </div>
            <div style={{ fontFamily: font, fontSize: 28, fontWeight: 800, color: theme.text }}>
              Provisioned host
            </div>
          </div>
        </div>

        <div
          style={{
            position: "relative",
            overflow: "hidden",
            background: "#090D14",
            border: `1px solid ${theme.panelBorder}`,
            borderRadius: 14,
            padding: "18px 18px 20px",
            marginTop: 24,
            height: 170,
          }}
        >
          <div
            style={{
              position: "absolute",
              left: 0,
              right: 0,
              top: `${scan}%`,
              height: 2,
              background: theme.green,
              boxShadow: `0 0 18px ${theme.green}`,
              opacity: 0.5,
            }}
          />
          {[
            "Admin generates an enrollment token",
            "$ curl .../api/agent/install | sudo bash -s -- <token>",
            "ocm-agent online · Firecracker ready",
          ].map((line, i) => (
            <div
              key={line}
              style={{
                fontFamily: mono,
                fontSize: i === 1 ? 15 : 17,
                lineHeight: 1.55,
                color: i === 2 ? theme.green : i === 1 ? theme.blue : theme.muted,
                opacity: frame > 10 + i * 18 ? 1 : 0.2,
              }}
            >
              {line}
            </div>
          ))}
        </div>

        <div style={{ display: "flex", flexDirection: "column", gap: 10, marginTop: 18 }}>
          {steps.map((step) => (
            <StatusPill
              key={step.label}
              label={step.label}
              color={step.color}
              active={frame >= step.at}
            />
          ))}
        </div>
      </div>

      <div
        style={{
          ...card,
          position: "absolute",
          left: 416,
          top: 252,
          width: 260,
          height: 156,
          padding: 22,
          borderColor: `${theme.blue}66`,
          display: "flex",
          flexDirection: "column",
          justifyContent: "center",
          alignItems: "center",
          gap: 10,
        }}
      >
        <div style={{ fontFamily: mono, fontSize: 17, color: theme.blue, letterSpacing: 1 }}>
          placement
        </div>
        <div style={{ fontFamily: font, fontSize: 30, fontWeight: 800, color: theme.text }}>
          host selected
        </div>
        <div style={{ fontFamily: mono, fontSize: 15, color: theme.muted }}>
          8 vCPU · 32 GB · KVM
        </div>
      </div>
    </div>
  );
};

export const HostProvisioning: React.FC<{
  chapter: string;
  accent: string;
}> = ({ chapter, accent }) => {
  const c = useReveal(4);
  const t = useReveal(10);
  const n = useReveal(18);

  return (
    <AbsoluteFill style={{ flexDirection: "row", alignItems: "center", padding: "0 90px", gap: 44 }}>
      <div style={{ width: 480, flexShrink: 0 }}>
        <div style={{ ...c, display: "flex", alignItems: "center", gap: 14 }}>
          <span
            style={{
              fontFamily: font,
              fontSize: 20,
              fontWeight: 800,
              color: theme.bgDeep,
              background: accent,
              borderRadius: 999,
              width: 40,
              height: 40,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
            }}
          >
            {chapter}
          </span>
          <span
            style={{
              fontFamily: font,
              fontSize: 22,
              fontWeight: 700,
              letterSpacing: 3,
              textTransform: "uppercase",
              color: accent,
            }}
          >
            Host setup
          </span>
        </div>
        <div
          style={{
            ...t,
            fontFamily: font,
            fontSize: 56,
            fontWeight: 800,
            color: theme.text,
            lineHeight: 1.08,
            letterSpacing: -1,
            marginTop: 22,
          }}
        >
          Onboard your KVM host
        </div>
        <div
          style={{
            ...n,
            fontFamily: font,
            fontSize: 27,
            lineHeight: 1.5,
            color: theme.muted,
            marginTop: 26,
          }}
        >
          Generate an enrollment token, install the worker on a Linux box, and
          wait for its heartbeat before placing machines there.
        </div>
      </div>

      <div style={{ flex: 1, display: "flex", justifyContent: "center", alignItems: "center" }}>
        <HostProvisioningVisual accent={accent} />
      </div>
    </AbsoluteFill>
  );
};
