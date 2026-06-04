import { useEffect, useState } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import {
  createMachine,
  listAdminOpenClawReleases,
  listAdminRootfsReleases,
  listOpenClawReleases,
  listRootfsReleases,
} from "../lib/api";
import { useAuth } from "../lib/auth";
import { useSizes } from "../lib/useSizes";
import { selectDefaultRegion, useRegions } from "../lib/useRegions";
import { useToast } from "./Toast";
import type { MachineKind } from "../lib/types";

type ReleaseOption = {
  value: string;
  label: string;
  channel?: string;
};

interface CreateMachineModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  accountId: number;
  onCreated: (machineId: string) => void;
}

function randomHex(): string {
  return crypto.getRandomValues(new Uint8Array(2)).reduce(
    (s, b) => s + b.toString(16).padStart(2, "0"),
    "",
  );
}

export function CreateMachineModal({ open, onOpenChange, accountId, onCreated }: CreateMachineModalProps) {
  const { toast } = useToast();
  const { isAdmin } = useAuth();
  const sizes = useSizes();
  const regions = useRegions();
  const [submitting, setSubmitting] = useState(false);
  const [machineKind, setMachineKind] = useState<MachineKind>("openclaw");
  const [name, setName] = useState(() => "machine-" + randomHex());
  const [sizeId, setSizeId] = useState("standard");
  const [regionCode, setRegionCode] = useState("");
  const [openclawVersion, setOpenclawVersion] = useState("");
  const [rootfsVersion, setRootfsVersion] = useState("");
  const [openclawOptions, setOpenclawOptions] = useState<ReleaseOption[]>([]);
  const [rootfsOptions, setRootfsOptions] = useState<ReleaseOption[]>([]);

  useEffect(() => {
    if (!open || !accountId) return;
    if (machineKind !== "openclaw") {
      setOpenclawOptions([]);
      setRootfsOptions([]);
      return;
    }
    if (isAdmin) {
      // Admin sees every individual release across stable + rc, with a channel
      // badge — needed to provision a machine on a non-stable runtime without
      // first booting it on stable and re-pinning.
      listAdminOpenClawReleases()
        .then((resp) =>
          setOpenclawOptions(
            resp.releases.map((r) => ({
              value: r.version,
              label: `${r.version} (${r.channel})`,
              channel: r.channel,
            })),
          ),
        )
        .catch(() => {});
      listAdminRootfsReleases()
        .then((resp) =>
          setRootfsOptions(
            resp.releases.map((r) => ({
              value: r.version,
              label: `${r.version} (${r.channel})`,
              channel: r.channel,
            })),
          ),
        )
        .catch(() => {});
    } else {
      listOpenClawReleases(accountId)
        .then((rows) =>
          setOpenclawOptions(rows.map((r) => ({ value: r.exact_version, label: r.version }))),
        )
        .catch(() => {});
      listRootfsReleases(accountId)
        .then((rows) =>
          setRootfsOptions(rows.map((r) => ({ value: r.exact_version, label: r.version }))),
        )
        .catch(() => {});
    }
  }, [open, accountId, isAdmin, machineKind]);
  const [error, setError] = useState<string | null>(null);

  const selectedSize = sizes.find((s) => s.id === sizeId) ?? sizes.find((s) => s.is_default) ?? sizes[0];

  useEffect(() => {
    if (regions.length === 0) return;
    if (regionCode === "" || !regions.some((r) => r.code === regionCode)) {
      setRegionCode(selectDefaultRegion(regions));
    }
  }, [regions, regionCode]);

  const resetForm = () => {
    setSubmitting(false);
    setMachineKind("openclaw");
    setName("machine-" + randomHex());
    setSizeId("standard");
    setRegionCode("");
    setOpenclawVersion("");
    setRootfsVersion("");
    setError(null);
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) resetForm();
    onOpenChange(nextOpen);
  };

  const handleSubmit = async () => {
    if (!selectedSize) return;
    setError(null);
    setSubmitting(true);

    try {
      const payload: Parameters<typeof createMachine>[1] = {
        name,
        kind: machineKind,
        size: selectedSize.id,
        preferred_region: regionCode || undefined,
        auto_start: true,
        runtime_source: "artifact",
      };
      if (machineKind === "openclaw" && openclawVersion) {
        payload.openclaw_version = openclawVersion;
      }
      if (machineKind === "openclaw" && rootfsVersion) {
        payload.rootfs_version = rootfsVersion;
      }

      const machine = await createMachine(accountId, payload);

      onOpenChange(false);
      resetForm();
      onCreated(machine.id);

      if (machine.start_error) {
        toast({ title: "Machine created but failed to start", description: machine.start_error, variant: "error" });
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create machine");
      setSubmitting(false);
    }
  };

  return (
    <Dialog.Root open={open} onOpenChange={handleOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50" />
        <Dialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border rounded-[var(--radius-lg)] p-6 w-full max-w-[400px] shadow-modal z-50">
          <Dialog.Title className="text-[17px] font-semibold text-text mb-5">
            New Machine
          </Dialog.Title>

          <form onSubmit={(e) => { e.preventDefault(); handleSubmit(); }} className="space-y-5">
            <div>
              <label className="block text-[11px] font-medium uppercase tracking-wide text-text-tertiary mb-1.5">
                KIND
              </label>
              <div className="grid grid-cols-2 gap-2">
                {([
                  ["openclaw", "OpenClaw"],
                  ["hermes", "Hermes"],
                ] as const).map(([kind, label]) => {
                  const isSelected = machineKind === kind;
                  return (
                    <button
                      key={kind}
                      type="button"
                      onClick={() => setMachineKind(kind)}
                      className={`border rounded-[var(--radius-sm)] px-3 py-2 text-[13px] font-medium transition-colors ${
                        isSelected
                          ? "border-brand-500 bg-brand-glow text-text-primary"
                          : "border-border bg-input text-text-secondary hover:border-border-strong"
                      }`}
                    >
                      {label}
                    </button>
                  );
                })}
              </div>
            </div>

            <div>
              <label className="block text-[11px] font-medium uppercase tracking-wide text-text-tertiary mb-1.5">
                NAME
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                placeholder="my-agent"
                className="w-full bg-input border border-border rounded-[var(--radius-sm)] text-[13px] p-[9px_12px] text-text placeholder:text-text-muted focus:outline-none focus:border-brand-500 focus:shadow-[0_0_0_2px_rgba(249,115,22,0.08)]"
              />
            </div>

            <div>
              <label className="block text-[11px] font-medium uppercase tracking-wide text-text-tertiary mb-1.5">
                SIZE
              </label>
              <div className="grid grid-cols-3 gap-2">
                {sizes.map((size) => {
                  const isSelected = sizeId === size.id;
                  return (
                    <button
                      key={size.id}
                      type="button"
                      onClick={() => setSizeId(size.id)}
                      className={`bg-input border rounded-[var(--radius-sm)] p-[10px] text-center cursor-pointer transition-all ${
                        isSelected
                          ? "border-brand-500 bg-brand-glow shadow-[0_0_0_1px_rgba(249,115,22,0.1)]"
                          : "border-border hover:border-border-strong"
                      }`}
                    >
                      <p className="text-[13px] font-semibold text-text">{size.label}</p>
                      <p className="text-[11px] text-text-tertiary mt-0.5">{size.description}</p>
                      <p className="text-[11px] font-medium text-brand-400 mt-1">${size.price_cents_mo / 100}/mo</p>
                    </button>
                  );
                })}
              </div>
            </div>

            <div>
              <label className="block text-[11px] font-medium uppercase tracking-wide text-text-tertiary mb-1.5">
                REGION
              </label>
              <select
                value={regionCode}
                onChange={(e) => setRegionCode(e.target.value)}
                className="w-full bg-input border border-border rounded-[var(--radius-sm)] text-[13px] p-[9px_12px] text-text focus:outline-none focus:border-brand-500"
              >
                {regions.length === 0 ? (
                  <option value="">Loading regions...</option>
                ) : (
                  regions.map((region) => (
                    <option key={region.code} value={region.code} disabled={!region.available}>
                      {region.name}{!region.available ? " (unavailable)" : ""}
                    </option>
                  ))
                )}
              </select>
            </div>

            {machineKind === "openclaw" && (
            <div>
              <label
                htmlFor="cm-rootfs-version"
                className="block text-[11px] font-medium uppercase tracking-wide text-text-tertiary mb-1.5"
              >
                Rootfs Version
              </label>
              <select
                id="cm-rootfs-version"
                value={rootfsVersion}
                onChange={(e) => setRootfsVersion(e.target.value)}
                className="w-full bg-input border border-border rounded-[var(--radius-sm)] text-[13px] p-[9px_12px] text-text focus:outline-none focus:border-brand-500"
              >
                <option value="">Latest (stable)</option>
                {rootfsOptions.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </select>
            </div>
            )}

            {machineKind === "openclaw" && (
            <div>
              <label
                htmlFor="cm-openclaw-version"
                className="block text-[11px] font-medium uppercase tracking-wide text-text-tertiary mb-1.5"
              >
                OpenClaw Version
              </label>
              <select
                id="cm-openclaw-version"
                value={openclawVersion}
                onChange={(e) => setOpenclawVersion(e.target.value)}
                className="w-full bg-input border border-border rounded-[var(--radius-sm)] text-[13px] p-[9px_12px] text-text focus:outline-none focus:border-brand-500"
              >
                <option value="">Latest (stable)</option>
                {openclawOptions.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </select>
            </div>
            )}

            {error && <p className="text-red-500 text-[13px]">{error}</p>}

            <div className="flex justify-end gap-2 pt-1">
              <Dialog.Close asChild>
                <button
                  type="button"
                  className="px-[14px] py-[7px] rounded-[var(--radius-sm)] text-[13px] font-medium border border-border text-text-secondary hover:bg-surface-elevated transition-colors"
                >
                  Cancel
                </button>
              </Dialog.Close>
              <button
                type="submit"
                disabled={submitting || sizes.length === 0}
                className="bg-brand-600 text-white text-[13px] font-medium px-[14px] py-[7px] rounded-[var(--radius-sm)] hover:bg-brand-700 disabled:opacity-50 transition-colors shadow-[0_1px_4px_rgba(234,88,12,0.2)]"
              >
                {submitting ? "Creating..." : "Create"}
              </button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
