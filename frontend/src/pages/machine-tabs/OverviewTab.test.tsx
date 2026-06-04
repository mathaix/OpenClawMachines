import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@/test/test-utils";
import { OverviewTab } from "./OverviewTab";
import type { Machine } from "../../lib/types";
import { listOpenClawReleases, listRootfsReleases } from "../../lib/api";

vi.mock("../../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api")>();
  return {
    ...actual,
    authMe: vi.fn(() => Promise.reject(new Error("Unauthorized"))),
    listAccounts: vi.fn(() => Promise.resolve([])),
    listPendingInvitations: vi.fn(() => Promise.resolve([])),
    getMachineUsage: vi.fn(() => Promise.resolve({ records: [] })),
    listMachineCapabilities: vi.fn(() => Promise.resolve([])),
    listOpenClawReleases: vi.fn(() => Promise.resolve([
      { version: "openclaw-2026.05.01", exact_version: "openclaw-2026.05.01", channel: "stable", created_at: "2026-05-01" },
    ])),
    listRootfsReleases: vi.fn(() => Promise.resolve([
      { version: "rootfs-2026.05.01", exact_version: "rootfs-2026.05.01", channel: "stable", created_at: "2026-05-01" },
    ])),
    upgradeMachineOpenClaw: vi.fn(),
    rollbackMachineOpenClaw: vi.fn(),
    upgradeMachineRootfs: vi.fn(),
    rollbackMachineRootfs: vi.fn(),
  };
});

const baseMachine: Machine = {
  id: "m-1",
  account_id: 1,
  name: "Test",
  slug: "test",
  status: "stopped",
  vcpus: 2,
  memory_mb: 2048,
  created_at: "2026-05-01T00:00:00Z",
  actual_openclaw_version: "runtime-current",
  actual_rootfs_version: "rootfs-current",
  openclaw_version: "runtime-current",
  rootfs_snapshot: "rootfs-current",
};

const mockedOpenClawReleases = listOpenClawReleases as unknown as ReturnType<typeof vi.fn>;
const mockedRootfsReleases = listRootfsReleases as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.clearAllMocks();
});

describe("OverviewTab RuntimeVersionSection", () => {
  it("loads Hermes rootfs releases with the Hermes kind and hides OpenClaw targeting", async () => {
    render(<OverviewTab machine={{ ...baseMachine, kind: "hermes" }} accountId={1} onTabChange={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));

    await waitFor(() => {
      expect(mockedRootfsReleases).toHaveBeenCalledWith(1, "stable", "hermes");
    });
    expect(mockedOpenClawReleases).not.toHaveBeenCalled();
    expect(screen.getByText("Hermes:")).toBeInTheDocument();
    expect(screen.getByText("Hermes rootfs:")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "OpenClaw" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rootfs" })).toBeInTheDocument();
    expect(screen.getByText("Select Hermes rootfs version...")).toBeInTheDocument();
  });

  it("loads OpenClaw runtime and rootfs releases with the OpenClaw kind", async () => {
    render(<OverviewTab machine={{ ...baseMachine, kind: "openclaw" }} accountId={1} onTabChange={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));

    await waitFor(() => {
      expect(mockedOpenClawReleases).toHaveBeenCalledWith(1, "stable", "openclaw");
      expect(mockedRootfsReleases).toHaveBeenCalledWith(1, "stable", "openclaw");
    });
    expect(screen.getByRole("button", { name: "OpenClaw" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rootfs" })).toBeInTheDocument();
  });
});
