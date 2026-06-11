import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@/test/test-utils";
import { OverviewTab } from "./OverviewTab";
import type { Machine } from "../../lib/types";
import {
  authMe,
  listOpenClawReleases,
  listRootfsReleases,
  listAdminOpenClawReleases,
  listAdminRootfsReleases,
} from "../../lib/api";

vi.mock("../../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api")>();
  return {
    ...actual,
    authMe: vi.fn(() => Promise.reject(new Error("Unauthorized"))),
    listAccounts: vi.fn(() => Promise.resolve([])),
    listPendingInvitations: vi.fn(() => Promise.resolve([])),
    listMachineCapabilities: vi.fn(() => Promise.resolve([])),
    listOpenClawReleases: vi.fn(() => Promise.resolve([
      { version: "openclaw-2026.05.01", exact_version: "openclaw-2026.05.01", channel: "stable", created_at: "2026-05-01" },
    ])),
    listRootfsReleases: vi.fn(() => Promise.resolve([
      { version: "rootfs-2026.05.01", exact_version: "rootfs-2026.05.01", channel: "stable", created_at: "2026-05-01" },
    ])),
    listAdminOpenClawReleases: vi.fn(() => Promise.resolve({
      releases: [
        { version: "v2026.6.5-r1", channel: "rc", created_at: "2026-06-10T13:29:18Z" },
        { version: "v2026.5.28-r4", channel: "stable", created_at: "2026-06-02T01:02:02Z" },
      ],
    })),
    listAdminRootfsReleases: vi.fn(() => Promise.resolve({
      releases: [
        { version: "25ed4e7-20260610T123001Z", channel: "rc", created_at: "2026-06-10T12:35:20Z" },
        { version: "ce1c3df-20260601T214501Z", channel: "stable", created_at: "2026-06-01T21:45:53Z" },
      ],
    })),
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

afterEach(() => {
  vi.unstubAllEnvs();
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

  it("superadmin sees stable + rc releases with channel labels in both dropdowns", async () => {
    vi.stubEnv("VITE_OCM_ADMIN_EMAILS", "admin@example.com");
    (authMe as unknown as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      user: { id: 23, email: "admin@example.com" },
    });

    render(<OverviewTab machine={{ ...baseMachine, kind: "openclaw" }} accountId={1} onTabChange={vi.fn()} />);

    // Wait for the auth context to resolve isAdmin before expanding.
    await waitFor(() => {
      expect(authMe).toHaveBeenCalled();
    });
    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));

    await waitFor(() => {
      expect(listAdminOpenClawReleases).toHaveBeenCalled();
      expect(listAdminRootfsReleases).toHaveBeenCalled();
    });
    expect(mockedOpenClawReleases).not.toHaveBeenCalled();
    expect(mockedRootfsReleases).not.toHaveBeenCalled();

    // OpenClaw dropdown (default target) shows rc with channel label, stable without.
    await waitFor(() => {
      expect(screen.getByRole("option", { name: "v2026.6.5-r1 (rc)" })).toBeInTheDocument();
    });
    expect(screen.getByRole("option", { name: /v2026\.5\.28-r4/ })).toBeInTheDocument();

    // Switch to rootfs target and check its options too.
    fireEvent.click(screen.getByRole("button", { name: "Rootfs" }));
    await waitFor(() => {
      expect(screen.getByRole("option", { name: "25ed4e7-20260610T123001Z (rc)" })).toBeInTheDocument();
    });
  });

  it("non-admin keeps the stable-only account release lists", async () => {
    render(<OverviewTab machine={{ ...baseMachine, kind: "openclaw" }} accountId={1} onTabChange={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /Runtime/ }));

    await waitFor(() => {
      expect(mockedOpenClawReleases).toHaveBeenCalledWith(1, "stable", "openclaw");
    });
    expect(listAdminOpenClawReleases).not.toHaveBeenCalled();
    expect(listAdminRootfsReleases).not.toHaveBeenCalled();
  });
});
