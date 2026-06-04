import { describe, it, expect, vi, beforeEach } from "vitest";
import { changeModel } from "./model";

vi.mock("../lib/api", () => ({
  setMachineModel: vi.fn(),
  pushMachineConfig: vi.fn(),
}));

import { setMachineModel, pushMachineConfig } from "../lib/api";

describe("changeModel", () => {
  beforeEach(() => vi.clearAllMocks());

  it("sets model and pushes config on success", async () => {
    vi.mocked(setMachineModel).mockResolvedValue({ status: "ok" });
    vi.mocked(pushMachineConfig).mockResolvedValue({ status: "ok" });
    const result = await changeModel(1, "m-1", "anthropic/claude-sonnet-4-6");
    expect(result.success).toBe(true);
    expect(setMachineModel).toHaveBeenCalledWith(1, "m-1", "anthropic/claude-sonnet-4-6");
    expect(pushMachineConfig).toHaveBeenCalledWith(1, "m-1");
  });

  it("returns error if setMachineModel fails", async () => {
    vi.mocked(setMachineModel).mockRejectedValue(new Error("invalid model"));
    const result = await changeModel(1, "m-1", "bad/model");
    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("invalid model");
    expect(pushMachineConfig).not.toHaveBeenCalled();
  });

  it("returns error if pushMachineConfig fails", async () => {
    vi.mocked(setMachineModel).mockResolvedValue({ status: "ok" });
    vi.mocked(pushMachineConfig).mockRejectedValue(new Error("push failed"));
    const result = await changeModel(1, "m-1", "anthropic/claude-sonnet-4-6");
    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("push failed");
  });
});
