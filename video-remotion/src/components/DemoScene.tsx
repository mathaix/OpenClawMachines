import React from "react";
import { AbsoluteFill } from "remotion";
import { font, theme } from "../theme";
import { useReveal } from "./ui";
import { BrowserFrame } from "./BrowserFrame";

export const DemoScene: React.FC<{
  chapter: string;
  step: string;
  title: string;
  note: string;
  accent: string;
  src: string;
  url: string;
  focus?: [number, number];
  zoom?: number;
}> = ({ chapter, step, title, note, accent, src, url, focus, zoom }) => {
  const c = useReveal(4);
  const t = useReveal(10);
  const n = useReveal(18);

  return (
    <AbsoluteFill style={{ flexDirection: "row", alignItems: "center", padding: "0 90px", gap: 56 }}>
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
          <span style={{ fontFamily: font, fontSize: 22, fontWeight: 700, letterSpacing: 3, textTransform: "uppercase", color: accent }}>
            {step}
          </span>
        </div>
        <div style={{ ...t, fontFamily: font, fontSize: 56, fontWeight: 800, color: theme.text, lineHeight: 1.08, letterSpacing: -1, marginTop: 22 }}>
          {title}
        </div>
        <div style={{ ...n, fontFamily: font, fontSize: 27, lineHeight: 1.5, color: theme.muted, marginTop: 26 }}>
          {note}
        </div>
      </div>

      <div style={{ flex: 1, display: "flex", justifyContent: "center", alignItems: "center" }}>
        <BrowserFrame src={src} url={url} focus={focus} zoom={zoom} width={1180} />
      </div>
    </AbsoluteFill>
  );
};
