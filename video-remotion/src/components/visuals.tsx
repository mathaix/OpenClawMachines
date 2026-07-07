import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { font, mono, theme } from "../theme";

const panel: React.CSSProperties = {
  background: theme.panel,
  border: `1px solid ${theme.panelBorder}`,
  borderRadius: 18,
  boxShadow: "0 20px 60px rgba(0,0,0,0.45)",
};

// ---- 1. microVM chip ----
export const MicroVMVisual: React.FC = () => {
  const frame = useCurrentFrame();
  const pulse = 1 + 0.03 * Math.sin(frame / 12);
  const ring = (interpolate(frame % 90, [0, 90], [0, 1]) * 100).toFixed(0);
  return (
    <div style={{ position: "relative", width: 460, height: 460 }}>
      <div
        style={{
          position: "absolute",
          inset: 60,
          borderRadius: 26,
          border: `2px solid ${theme.brand}55`,
          transform: `scale(${1 + Number(ring) / 400})`,
          opacity: 1 - Number(ring) / 100,
        }}
      />
      <div
        style={{
          ...panel,
          position: "absolute",
          inset: 90,
          transform: `scale(${pulse})`,
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          justifyContent: "center",
          gap: 14,
          borderColor: `${theme.brand}66`,
        }}
      >
        {/* chip pins */}
        {[0, 1, 2, 3].map((i) => (
          <React.Fragment key={i}>
            <span style={{ position: "absolute", left: -10, top: 50 + i * 60, width: 10, height: 20, background: theme.brand, borderRadius: 3 }} />
            <span style={{ position: "absolute", right: -10, top: 50 + i * 60, width: 10, height: 20, background: theme.brand, borderRadius: 3 }} />
          </React.Fragment>
        ))}
        <div style={{ fontFamily: mono, fontSize: 22, color: theme.brandSoft, letterSpacing: 2 }}>microVM</div>
        <div style={{ fontFamily: font, fontSize: 30, fontWeight: 800, color: theme.text }}>OpenClaw agent</div>
        <div style={{ fontFamily: mono, fontSize: 18, color: theme.muted }}>own kernel · KVM</div>
      </div>
    </div>
  );
};

// ---- 2. control plane fan-out ----
const Box: React.FC<{ x: number; y: number; w: number; h: number; label: string; sub?: string; color?: string; children?: React.ReactNode }> = ({ x, y, w, h, label, sub, color = theme.panelBorder, children }) => (
  <div style={{ position: "absolute", left: x, top: y, width: w, height: h, ...panel, borderColor: color, display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", gap: 6 }}>
    <div style={{ fontFamily: font, fontSize: 22, fontWeight: 700, color: theme.text }}>{label}</div>
    {sub ? <div style={{ fontFamily: mono, fontSize: 15, color: theme.muted }}>{sub}</div> : null}
    {children}
  </div>
);

export const ControlPlaneVisual: React.FC = () => {
  const frame = useCurrentFrame();
  const dash = -(frame * 3) % 24;
  const line = (d: string, color: string) => (
    <path d={d} stroke={color} strokeWidth={2.5} fill="none" strokeDasharray="8 8" strokeDashoffset={dash} opacity={0.8} />
  );
  return (
    <div style={{ position: "relative", width: 520, height: 520 }}>
      <svg width={520} height={520} style={{ position: "absolute", inset: 0 }}>
        {line("M260 120 L110 300", theme.brand)}
        {line("M260 120 L260 300", theme.brand)}
        {line("M260 120 L410 300", theme.brand)}
      </svg>
      <Box x={150} y={50} w={220} h={90} label="Control plane" sub="placement · lifecycle" color={`${theme.brand}88`} />
      {[40, 200, 360].map((x, i) => (
        <Box key={i} x={x} y={300} w={120} h={150} label={`Host ${i + 1}`} sub="ocm-agent">
          <div style={{ display: "flex", gap: 6, marginTop: 6 }}>
            {[0, 1].map((j) => (
              <span key={j} style={{ width: 22, height: 22, borderRadius: 6, background: `${theme.green}`, opacity: 0.85, boxShadow: `0 0 10px ${theme.green}` }} />
            ))}
          </div>
        </Box>
      ))}
    </div>
  );
};

// ---- 3. chat + terminal ----
export const ChatTerminalVisual: React.FC = () => {
  const frame = useCurrentFrame();
  const lines = ["$ run the test suite", "→ agent: executing…", "PASS  129 tests  1.0s"];
  const shown = Math.min(lines.length, Math.floor(frame / 24));
  const cursor = frame % 20 < 10;
  return (
    <div style={{ ...panel, width: 520, height: 360, overflow: "hidden" }}>
      <div style={{ height: 46, background: theme.bgDeep, display: "flex", alignItems: "center", gap: 8, padding: "0 18px", borderBottom: `1px solid ${theme.panelBorder}` }}>
        {[theme.accent, theme.brandSoft, theme.green].map((c) => (
          <span key={c} style={{ width: 13, height: 13, borderRadius: 999, background: c }} />
        ))}
        <span style={{ fontFamily: mono, fontSize: 16, color: theme.muted, marginLeft: 10 }}>web chat + live terminal</span>
      </div>
      <div style={{ padding: 26, fontFamily: mono, fontSize: 24, lineHeight: 1.7 }}>
        {lines.slice(0, shown + 1).map((l, i) => (
          <div key={i} style={{ color: i === 0 ? theme.text : i === 2 ? theme.green : theme.brandSoft }}>
            {i <= shown ? l : ""}
            {i === shown && cursor ? <span style={{ color: theme.brand }}>▋</span> : null}
          </div>
        ))}
      </div>
    </div>
  );
};

// ---- 4. browser VM ----
export const BrowserVMVisual: React.FC = () => {
  const frame = useCurrentFrame();
  const scan = interpolate(frame % 120, [0, 120], [0, 100]);
  return (
    <div style={{ ...panel, width: 540, height: 380, overflow: "hidden", borderColor: `${theme.blue}55` }}>
      <div style={{ height: 50, background: theme.bgDeep, display: "flex", alignItems: "center", gap: 12, padding: "0 18px" }}>
        <span style={{ display: "flex", gap: 7 }}>
          {[theme.accent, theme.brandSoft, theme.green].map((c) => (<span key={c} style={{ width: 12, height: 12, borderRadius: 999, background: c }} />))}
        </span>
        <div style={{ flex: 1, height: 26, borderRadius: 8, background: theme.panel, border: `1px solid ${theme.panelBorder}`, fontFamily: mono, fontSize: 15, color: theme.muted, display: "flex", alignItems: "center", padding: "0 12px" }}>headful Chromium · live view</div>
      </div>
      <div style={{ position: "relative", height: 330, background: `linear-gradient(160deg, ${theme.panel}, ${theme.bgDeep})` }}>
        <div style={{ position: "absolute", top: `${scan}%`, left: 0, right: 0, height: 2, background: `${theme.blue}`, boxShadow: `0 0 16px ${theme.blue}`, opacity: 0.7 }} />
        <div style={{ position: "absolute", left: 40, top: 60, width: 180, height: 24, borderRadius: 6, background: `${theme.blue}33` }} />
        <div style={{ position: "absolute", left: 40, top: 100, width: 300, height: 16, borderRadius: 6, background: `${theme.panelBorder}` }} />
        <div style={{ position: "absolute", left: 40, top: 128, width: 240, height: 16, borderRadius: 6, background: `${theme.panelBorder}` }} />
        <div style={{ position: "absolute", right: 24, bottom: 20, fontFamily: mono, fontSize: 16, color: theme.blue, border: `1px solid ${theme.blue}55`, borderRadius: 8, padding: "6px 12px" }}>driven over CDP</div>
      </div>
    </div>
  );
};

// ---- 5. data plane flow ----
export const DataPlaneVisual: React.FC = () => {
  const frame = useCurrentFrame();
  const t = (frame % 90) / 90;
  const nodes = ["You", "Cloudflare edge", "per-VM tunnel", "microVM"];
  const colors = [theme.muted, theme.blue, theme.blue, theme.brand];
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 26, width: 480 }}>
      {nodes.map((n, i) => (
        <div key={i} style={{ display: "flex", flexDirection: "column", gap: 0 }}>
          <div style={{ ...panel, padding: "18px 24px", display: "flex", alignItems: "center", gap: 14, borderColor: `${colors[i]}66` }}>
            <span style={{ width: 12, height: 12, borderRadius: 999, background: colors[i], boxShadow: `0 0 12px ${colors[i]}` }} />
            <span style={{ fontFamily: font, fontSize: 27, fontWeight: 700, color: theme.text }}>{n}</span>
            {i === 1 ? <span style={{ marginLeft: "auto", fontFamily: mono, fontSize: 16, color: theme.muted }}>edge auth</span> : null}
          </div>
          {i < nodes.length - 1 ? (
            <div style={{ height: 26, marginLeft: 30, position: "relative", width: 2, background: theme.panelBorder }}>
              <span style={{ position: "absolute", left: -4, top: `${t * 100}%`, width: 10, height: 10, borderRadius: 999, background: theme.blue, boxShadow: `0 0 12px ${theme.blue}` }} />
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
};

// ---- 6. native MCP facade ----
export const MCPVisual: React.FC = () => {
  const frame = useCurrentFrame();
  const dash = -(frame * 3) % 20;
  const tools = ["GitHub", "Google", "OpenAPI", "GraphQL"];
  const positions = [[60, 60], [420, 60], [60, 360], [420, 360]];
  return (
    <div style={{ position: "relative", width: 560, height: 500 }}>
      <svg width={560} height={500} style={{ position: "absolute", inset: 0 }}>
        {positions.map(([x, y], i) => (
          <path key={i} d={`M280 250 L${x + 55} ${y + 25}`} stroke={theme.violet} strokeWidth={2.5} fill="none" strokeDasharray="7 7" strokeDashoffset={dash} opacity={0.75} />
        ))}
      </svg>
      {positions.map(([x, y], i) => (
        <div key={i} style={{ position: "absolute", left: x, top: y, width: 130, height: 50, ...panel, display: "flex", alignItems: "center", justifyContent: "center", fontFamily: font, fontSize: 22, fontWeight: 700, color: theme.text }}>{tools[i]}</div>
      ))}
      <div style={{ position: "absolute", left: 175, top: 175, width: 210, height: 150, ...panel, borderColor: `${theme.violet}88`, display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", gap: 8, boxShadow: `0 0 40px ${theme.violet}44` }}>
        <div style={{ fontFamily: mono, fontSize: 20, color: theme.violet, letterSpacing: 1 }}>ocm · MCP</div>
        <div style={{ fontFamily: mono, fontSize: 17, color: theme.muted }}>search_tools</div>
        <div style={{ fontFamily: mono, fontSize: 17, color: theme.muted }}>call_tool</div>
      </div>
    </div>
  );
};
