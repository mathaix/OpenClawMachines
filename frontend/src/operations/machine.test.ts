import { describe, it, expect, vi, beforeEach } from "vitest";
import { transitionMachine } from "./machine";

vi.mock("../lib/api", () => ({
  startMachine: vi.fn(),
  stopMachine: vi.fn(),
  deleteMachine: vi.fn(),
}));

import { startMachine, stopMachine, deleteMachine } from "../lib/api";

describe("transitionMachine", () => {
  beforeEach(() => vi.clearAllMocks());

  it("starts a machine successfully", async () => {
    vi.mocked(startMachine).mockResolvedValue({ status: "provisioning", host_id: 1, vm_ip: "10.0.0.1" });
    const result = await transitionMachine(1, "m-1", "start");
    expect(result.success).toBe(true);
    expect(result.error).toBeUndefined();
    expect(startMachine).toHaveBeenCalledWith(1, "m-1");
  });

  it("stops a machine successfully", async () => {
    vi.mocked(stopMachine).mockResolvedValue({ status: "stopped" });
    const result = await transitionMachine(1, "m-1", "stop");
    expect(result.success).toBe(true);
    expect(stopMachine).toHaveBeenCalledWith(1, "m-1");
  });

  it("deletes a machine successfully", async () => {
    vi.mocked(deleteMachine).mockResolvedValue(undefined as never);
    const result = await transitionMachine(1, "m-1", "delete");
    expect(result.success).toBe(true);
    expect(deleteMachine).toHaveBeenCalledWith(1, "m-1");
  });

  it("returns error on failure", async () => {
    vi.mocked(startMachine).mockRejectedValue(new Error("host full"));
    const result = await transitionMachine(1, "m-1", "start");
    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("host full");
  });
});
