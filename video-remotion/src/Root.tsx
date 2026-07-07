import React from "react";
import { Composition } from "remotion";
import { OpenClawVideo, TOTAL_FRAMES } from "./OpenClawVideo";
import { DemoVideo, DEMO_FRAMES } from "./DemoVideo";

export const RemotionRoot: React.FC = () => {
  return (
    <>
      <Composition
        id="OpenClawDemo"
        component={DemoVideo}
        durationInFrames={DEMO_FRAMES}
        fps={30}
        width={1920}
        height={1080}
      />
      <Composition
        id="OpenClawMachines"
        component={OpenClawVideo}
        durationInFrames={TOTAL_FRAMES}
        fps={30}
        width={1920}
        height={1080}
      />
    </>
  );
};
