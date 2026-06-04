import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@/test/test-utils";
import { Welcome } from "./Welcome";

const mockNavigate = vi.fn();
vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual("react-router-dom");
  return { ...actual, useNavigate: () => mockNavigate };
});

vi.mock("../lib/api", () => ({
  authMe: vi.fn(() => Promise.resolve({ user: { id: 1, email: "test@example.com", name: "", auth_provider: "cfaccess", created_at: "2024-01-01T00:00:00Z" } })),
  listAccounts: vi.fn(() => Promise.resolve([])),
  createAccount: vi.fn(() => Promise.resolve({ id: 1, name: "Test", slug: "test", plan: "free", created_by: 1, created_at: "2024-01-01T00:00:00Z" })),
}));

import { createAccount } from "../lib/api";
const mockCreateAccount = vi.mocked(createAccount);

describe("Welcome page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("should render the welcome form", () => {
    render(<Welcome />);

    expect(screen.getByText(/Welcome to/)).toBeInTheDocument();
    expect(screen.getByText("OpenClaw")).toBeInTheDocument();
    expect(screen.getByLabelText("Display Name")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Get Started" })).toBeInTheDocument();
  });

  it("should auto-generate slug from display name", () => {
    render(<Welcome />);

    fireEvent.change(screen.getByLabelText("Display Name"), {
      target: { value: "My Team" },
    });

    expect(screen.getByText("my-team.openclawmachines.com")).toBeInTheDocument();
  });

  it("should allow custom slug override", () => {
    render(<Welcome />);

    fireEvent.change(screen.getByLabelText("Display Name"), {
      target: { value: "My Team" },
    });

    const slugInput = screen.getByPlaceholderText("auto-generated from name");
    fireEvent.change(slugInput, { target: { value: "custom-slug" } });

    expect(screen.getByText("custom-slug.openclawmachines.com")).toBeInTheDocument();
  });

  it("should submit and navigate to dashboard", async () => {
    render(<Welcome />);

    fireEvent.change(screen.getByLabelText("Display Name"), {
      target: { value: "My Team" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Get Started" }));

    await waitFor(() => {
      expect(mockCreateAccount).toHaveBeenCalledWith({
        name: "My Team",
        slug: "my-team",
      });
    });

    expect(mockNavigate).toHaveBeenCalledWith("/dashboard");
  });

  it("should show error on submission failure", async () => {
    mockCreateAccount.mockRejectedValueOnce(new Error("Slug already taken"));

    render(<Welcome />);

    fireEvent.change(screen.getByLabelText("Display Name"), {
      target: { value: "My Team" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Get Started" }));

    await waitFor(() => {
      expect(screen.getByText("Slug already taken")).toBeInTheDocument();
    });
  });

  it("should disable submit button when display name is empty", () => {
    render(<Welcome />);

    expect(screen.getByRole("button", { name: "Get Started" })).toBeDisabled();
  });
});
