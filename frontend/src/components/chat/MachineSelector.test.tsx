import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "../../test/test-utils";
import { MachineSelector } from "./MachineSelector";
import type { Machine } from "../../lib/types";

const mockMachines: Machine[] = [
  {
    id: "m1",
    account_id: 1,
    name: "Bot Alpha",
    slug: "bot-alpha",
    status: "running",
    gateway_token: "tok1",
    vcpus: 2,
    memory_mb: 2048,
    created_at: "2026-01-01",
  } as Machine,
  {
    id: "m2",
    account_id: 1,
    name: "Bot Beta",
    slug: "bot-beta",
    status: "running",
    gateway_token: "tok2",
    vcpus: 2,
    memory_mb: 2048,
    created_at: "2026-01-01",
  } as Machine,
];

describe("MachineSelector", () => {
  it("renders machine options", () => {
    render(
      <MachineSelector
        machines={mockMachines}
        selectedId="m1"
        onSelect={vi.fn()}
        loading={false}
      />
    );
    expect(screen.getByText("Bot Alpha")).toBeInTheDocument();
  });

  it("calls onSelect when machine is chosen", () => {
    const onSelect = vi.fn();
    render(
      <MachineSelector
        machines={mockMachines}
        selectedId="m1"
        onSelect={onSelect}
        loading={false}
      />
    );
    fireEvent.change(screen.getByRole("listbox"), { target: { value: "m2" } });
    expect(onSelect).toHaveBeenCalledWith("m2");
  });

  it("shows loading state", () => {
    render(
      <MachineSelector
        machines={[]}
        selectedId={null}
        onSelect={vi.fn()}
        loading={true}
      />
    );
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows empty state when no machines", () => {
    render(
      <MachineSelector
        machines={[]}
        selectedId={null}
        onSelect={vi.fn()}
        loading={false}
      />
    );
    expect(screen.getByText(/no running machines/i)).toBeInTheDocument();
  });
});
