import { describe, it, expect } from "vitest";
import { selectDefaultRegion } from "./useRegions";
import type { RegionOption } from "./types";

function region(partial: Partial<RegionOption> & Pick<RegionOption, "code">): RegionOption {
  return {
    code: partial.code,
    name: partial.name ?? partial.code,
    available: partial.available ?? true,
  };
}

describe("selectDefaultRegion", () => {
  it("returns empty string for empty input", () => {
    expect(selectDefaultRegion([])).toBe("");
  });

  it("prefers us-west when present", () => {
    const regions = [
      region({ code: "eu-central" }),
      region({ code: "us-west" }),
      region({ code: "us-central" }),
    ];
    expect(selectDefaultRegion(regions)).toBe("us-west");
  });

  it("falls back to first available region when us-west is absent", () => {
    const regions = [
      region({ code: "eu-central" }),
      region({ code: "us-central" }),
      region({ code: "asia-east" }),
    ];
    expect(selectDefaultRegion(regions)).toBe("eu-central");
  });

  it("prefers available regions over unavailable ones", () => {
    const regions = [
      region({ code: "us-west", available: false }),
      region({ code: "us-east", available: true }),
    ];
    expect(selectDefaultRegion(regions)).toBe("us-east");
  });
});
