import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@/test/test-utils";
import { OnboardingWizard } from "./OnboardingWizard";

vi.mock("../lib/api", () => ({
  authMe: vi.fn(() => Promise.resolve({
    user: {
      id: 1,
      email: "test@example.com",
      name: "",
      auth_provider: "cfaccess",
      created_at: "2024-01-01T00:00:00Z",
    },
  })),
  listAccounts: vi
    .fn()
    .mockResolvedValueOnce([])
    .mockResolvedValue([
      {
        id: 2,
        name: "My Team",
        slug: "my-team",
        created_by: 1,
        created_at: "2024-01-01T00:00:00Z",
      },
    ]),
  listPendingInvitations: vi.fn(() => Promise.resolve([])),
  createAccount: vi.fn(() => Promise.resolve({
    id: 2,
    name: "My Team",
    slug: "my-team",
    created_by: 1,
    created_at: "2024-01-01T00:00:00Z",
  })),
  listSizes: vi.fn(() => Promise.resolve([
    { id: "basic", label: "Basic", vcpus: 1, memory_mb: 3072, swap_mb: 1024, disk_gb: 10, description: "1 vCPU, 3 GB RAM, 10 GB disk" },
    { id: "standard", label: "Standard", vcpus: 2, memory_mb: 6144, swap_mb: 1024, disk_gb: 20, description: "2 vCPUs, 6 GB RAM, 20 GB disk", is_default: true },
    { id: "pro", label: "Pro", vcpus: 4, memory_mb: 12288, swap_mb: 1024, disk_gb: 50, description: "4 vCPUs, 12 GB RAM, 50 GB disk" },
  ])),
  createMachine: vi.fn(),
  putMachineCredential: vi.fn(),
  setMachineIdentity: vi.fn(),
  pushMachineConfig: vi.fn(),
  startMachine: vi.fn(),
}));

import { createAccount, listAccounts } from "../lib/api";

const mockCreateAccount = vi.mocked(createAccount);
const mockListAccounts = vi.mocked(listAccounts);

describe("OnboardingWizard", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
    mockListAccounts
      .mockResolvedValueOnce([])
      .mockResolvedValue([
        {
          id: 2,
          name: "My Team",
          slug: "my-team",
          created_by: 1,
          created_at: "2024-01-01T00:00:00Z",
        },
      ]);
    mockCreateAccount.mockResolvedValue({
      id: 2,
      name: "My Team",
      slug: "my-team",
      created_by: 1,
      created_at: "2024-01-01T00:00:00Z",
    });
  });

  it("creates an account, refreshes auth state, and advances to machine setup", async () => {
    render(<OnboardingWizard />);

    fireEvent.change(screen.getByLabelText("Display Name"), {
      target: { value: "My Team" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(mockCreateAccount).toHaveBeenCalledWith({
        name: "My Team",
        slug: "my-team",
      });
    });

    await waitFor(() => {
      expect(mockListAccounts).toHaveBeenCalledTimes(2);
      expect(screen.getByText("Configure your machine")).toBeInTheDocument();
    });
  });
});
