import React from "react";
import {
  Img,
  interpolate,
  spring,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { mono, theme } from "../theme";

// A floating browser window that displays a real product screenshot with a
// slow Ken-Burns push. `focus` optionally biases the zoom toward a region of
// the shot (0..1 in x/y).
export const BrowserFrame: React.FC<{
  src: string;
  url: string;
  focus?: [number, number];
  zoom?: number;
  width?: number;
}> = ({ src, url, focus = [0.5, 0.35], zoom = 1.1, width = 1360 }) => {
  const frame = useCurrentFrame();
  const { fps, durationInFrames } = useVideoConfig();

  const enter = spring({ frame, fps, config: { damping: 200, mass: 0.8 } });
  const opacity = interpolate(enter, [0, 1], [0, 1]);
  const rise = interpolate(enter, [0, 1], [40, 0]);

  const prog = interpolate(frame, [0, durationInFrames], [0, 1], {
    extrapolateRight: "clamp",
  });
  const scale = interpolate(prog, [0, 1], [1.0, zoom]);
  const ox = interpolate(prog, [0, 1], [0, (focus[0] - 0.5) * -60]);
  const oy = interpolate(prog, [0, 1], [0, (focus[1] - 0.5) * -60]);

  const barH = 52;
  const imgW = width;
  const imgH = Math.round((width * 2160) / 3840);

  return (
    <div
      style={{
        width: imgW,
        borderRadius: 16,
        overflow: "hidden",
        border: `1px solid ${theme.panelBorder}`,
        boxShadow: "0 40px 120px rgba(0,0,0,0.6)",
        opacity,
        transform: `translateY(${rise}px)`,
        background: theme.bgDeep,
      }}
    >
      <div
        style={{
          height: barH,
          background: "#1b2431",
          display: "flex",
          alignItems: "center",
          gap: 10,
          padding: "0 18px",
          borderBottom: `1px solid ${theme.panelBorder}`,
        }}
      >
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
          <span key={c} style={{ width: 13, height: 13, borderRadius: 999, background: c }} />
        ))}
        <div
          style={{
            marginLeft: 14,
            flex: 1,
            height: 30,
            borderRadius: 8,
            background: theme.bgDeep,
            border: `1px solid ${theme.panelBorder}`,
            display: "flex",
            alignItems: "center",
            padding: "0 14px",
            fontFamily: mono,
            fontSize: 15,
            color: theme.muted,
          }}
        >
          {url}
        </div>
      </div>
      <div style={{ width: imgW, height: imgH, overflow: "hidden" }}>
        <Img
          src={staticFile(src)}
          style={{
            width: imgW,
            height: imgH,
            objectFit: "cover",
            transform: `scale(${scale}) translate(${ox}px, ${oy}px)`,
            transformOrigin: `${focus[0] * 100}% ${focus[1] * 100}%`,
          }}
        />
      </div>
    </div>
  );
};
