import { useState, useEffect, useCallback } from "react";
import { Globe, Plus, Loader2, ExternalLink } from "lucide-react";
import {
  listBrowserVMs,
  createBrowserVM,
  startBrowserVM,
  pairBrowser,
  unpairBrowser,
  getBrowserVM,
  browserVMLiveUrl,
} from "../../lib/api";
import { useToast } from "../../components/Toast";
import type { Machine } from "../../lib/types";
import type { BrowserVM } from "../../lib/types";
import {
  BROWSER_VM_SIZES,
  DEFAULT_BROWSER_VM_SIZE,
  formatBrowserVMSize,
  type BrowserVMSize,
} from "../../lib/browser-vm-sizes";
import {
  BROWSER_IMAGE_OPTIONS,
  browserImageLabelForVM,
  type BrowserImage,
} from "../../lib/browser-vm-images";

const EMBEDDED_BROWSER_VIEWPORT = { width: 1280, height: 720, rate: 30 };

interface BrowserTabProps {
  machine: Machine;
  accountId: number;
}

export function BrowserTab({ machine, accountId }: BrowserTabProps) {
  const [pairedBVM, setPairedBVM] = useState<BrowserVM | null>(null);
  const [availableBVMs, setAvailableBVMs] = useState<BrowserVM[]>([]);
  const [selectedBVMId, setSelectedBVMId] = useState("");
  const [loading, setLoading] = useState(true);
  const [pairing, setPairing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [launchSize, setLaunchSize] = useState<BrowserVMSize>(DEFAULT_BROWSER_VM_SIZE);
  const [launchImage, setLaunchImage] = useState<BrowserImage>("kernel-stable");
  const { toast } = useToast();

  const reload = useCallback(async () => {
    // Reset loading on every reload so switching machines (prop change)
    // doesn't briefly render stale pair data from the previous mount.
    setLoading(true);
    try {
      if (machine.browser_vm_id) {
        const bvm = await getBrowserVM(accountId, machine.browser_vm_id);
        setPairedBVM(bvm);
      } else {
        setPairedBVM(null);
      }
      const all = (await listBrowserVMs(accountId)) ?? [];
      const available = all.filter(
        (bvm) =>
          bvm.status === "running" &&
          bvm.host_id === machine.host_id &&
          bvm.id !== machine.browser_vm_id
      );
      setAvailableBVMs(available);
    } catch (err) {
      // Previously this was silently swallowed, which left the UI
      // stuck on "Loading..." when the API was unreachable. Surface
      // the failure via console + toast so users can retry instead
      // of wondering why the tab is blank.
      console.error("BrowserTab.reload failed", err);
      toast({
        title: "Failed to load browser VMs",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setLoading(false);
    }
  }, [accountId, machine.browser_vm_id, machine.host_id, toast]);

  useEffect(() => {
    reload();
  }, [reload]);

  const handlePair = async () => {
    if (!selectedBVMId) return;
    setPairing(true);
    try {
      await pairBrowser(accountId, machine.id, selectedBVMId);
      const bvm = await getBrowserVM(accountId, selectedBVMId);
      setPairedBVM(bvm);
      // Drop the just-paired VM from the "available" list so the dropdown
      // doesn't offer it again until unpair — mirrors handleCreateAndPair.
      setAvailableBVMs((prev) => prev.filter((item) => item.id !== bvm.id));
      setSelectedBVMId("");
      toast({ title: "Browser paired", description: `Connected to ${bvm.slug}`, variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to pair",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setPairing(false);
    }
  };

  const handleUnpair = async () => {
    setPairing(true);
    try {
      await unpairBrowser(accountId, machine.id);
      setPairedBVM(null);
      try {
        const all = (await listBrowserVMs(accountId)) ?? [];
        const available = all.filter(
          (bvm) =>
            bvm.status === "running" &&
            bvm.host_id === machine.host_id
        );
        setAvailableBVMs(available);
      } catch {
        // ignore
      }
      toast({ title: "Browser unpaired", variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to unpair",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setPairing(false);
    }
  };

  const handleCreateAndPair = async () => {
    if (!machine.host_id) return;
    setCreating(true);
    try {
      const created = await createBrowserVM(accountId, {
        name: `${machine.slug || machine.name || "Machine"} browser`,
        vcpus: launchSize.vcpus,
        memory_mb: launchSize.memory_mb,
        browser_image: launchImage,
      });
      const started = await startBrowserVM(accountId, created.id, { host_id: machine.host_id });
      await pairBrowser(accountId, machine.id, started.id);
      const updated = await getBrowserVM(accountId, started.id);
      setPairedBVM(updated);
      setAvailableBVMs((prev) => prev.filter((item) => item.id !== updated.id));
      toast({ title: "Browser launched", description: `Connected to ${updated.slug}`, variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to launch browser",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
      await reload();
    } finally {
      setCreating(false);
    }
  };

  if (loading) {
    return <div className="p-6 text-text-tertiary">Loading...</div>;
  }

  if (machine.kind === "hermes") {
    return (
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <p className="text-lg md:text-xl font-semibold text-text-primary mb-1">Browser Tools</p>
          <p className="text-xs md:text-sm text-text-tertiary">
            Hermes uses the Playwright browser built into this VM. Use the dashboard or terminal to inspect browser sessions.
          </p>
        </div>
      </div>
    );
  }

  if (machine.status !== "running") {
    return (
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <p className="text-lg md:text-xl font-semibold text-text-primary mb-1">Web Browser</p>
          <p className="text-xs md:text-sm text-text-tertiary">
            Start the machine to enable browser pairing.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <p className="text-lg md:text-xl font-semibold text-text-primary mb-1">Web Browser</p>
          <p className="text-xs md:text-sm text-text-tertiary mb-4">
            Connect a browser VM to enable web browsing capabilities
          </p>

          {pairedBVM ? (
            <div className="space-y-4">
              <div className="border border-green-500/30 rounded-[var(--radius-sm)] p-3 md:p-4 bg-green-500/5">
                <div className="flex items-center justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <div className="w-9 h-9 rounded-lg bg-green-500/10 flex items-center justify-center flex-shrink-0">
                      <Globe className="w-4 h-4 text-green-400" />
                    </div>
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-[11px] px-2 py-0.5 rounded-full bg-green-500/10 text-green-400 border border-green-500/20 font-medium">
                          connected
                        </span>
                        <span className="text-sm font-medium text-text-primary">{pairedBVM.slug}</span>
                      </div>
                      <p className="text-xs text-text-tertiary mt-0.5">
                        CDP: {pairedBVM.vm_ip}:{pairedBVM.cdp_port} · {browserImageLabelForVM(pairedBVM)}
                      </p>
                    </div>
                  </div>
                  <button
                    onClick={handleUnpair}
                    disabled={pairing}
                    className="text-sm px-3 py-1.5 rounded-md border border-red-500/30 text-red-400 hover:bg-red-500/10 disabled:opacity-50"
                  >
                    Unpair
                  </button>
                </div>
              </div>
              {pairedBVM.status === "running" ? (
                <div className="border border-border rounded-[var(--radius-sm)] overflow-hidden bg-deep">
                  <div className="flex items-center justify-between px-3 py-2 border-b border-border">
                    <span className="text-sm font-medium text-text-primary">Live browser</span>
                    <div className="flex items-center gap-3">
                      <span className="text-xs text-text-tertiary hidden md:inline">
                        Agent and user share this session
                      </span>
                      <a
                        href={`/browser-vms/${pairedBVM.id}/live`}
                        target="_blank"
                        rel="noopener"
                        className="inline-flex items-center gap-1.5 text-xs text-brand-400 hover:text-brand-300 hover:underline"
                        title="Open full-screen live view in a new tab"
                      >
                        <ExternalLink className="w-3.5 h-3.5" />
                        Open full screen
                      </a>
                    </div>
                  </div>
                  <iframe
                    title={`Live browser ${pairedBVM.slug}`}
                    src={browserVMLiveUrl(accountId, pairedBVM.id, EMBEDDED_BROWSER_VIEWPORT)}
                    className="w-full aspect-video border-0 bg-black"
                    allow="clipboard-read; clipboard-write; fullscreen"
                  />
                </div>
              ) : (
                <p className="text-sm text-text-tertiary">Live preview is available when the browser VM is running.</p>
              )}
            </div>
          ) : (
            <div className="space-y-3">
              <div className="flex flex-col sm:flex-row items-stretch gap-2">
                <select
                  value={launchImage}
                  onChange={(e) => setLaunchImage(e.target.value as BrowserImage)}
                  disabled={creating || !machine.host_id}
                  className="text-sm px-3 py-2 rounded-[var(--radius-sm)] border border-border bg-card text-text-secondary disabled:opacity-50 focus:outline-none focus:ring-1 focus:ring-brand-600"
                  aria-label="Browser image for launch"
                >
                  {BROWSER_IMAGE_OPTIONS.map((option) => (
                    <option key={option.id} value={option.id}>{option.label}</option>
                  ))}
                </select>
                <select
                  value={launchSize.id}
                  onChange={(e) => {
                    const next = BROWSER_VM_SIZES.find((s) => s.id === e.target.value);
                    if (next) setLaunchSize(next);
                  }}
                  disabled={creating || !machine.host_id}
                  className="text-sm px-3 py-2 rounded-[var(--radius-sm)] border border-border bg-card text-text-secondary disabled:opacity-50 focus:outline-none focus:ring-1 focus:ring-brand-600"
                  aria-label="Browser VM size for launch"
                >
                  {BROWSER_VM_SIZES.map((s) => (
                    <option key={s.id} value={s.id}>{s.label}</option>
                  ))}
                </select>
                <button
                  onClick={handleCreateAndPair}
                  disabled={creating || !machine.host_id}
                  className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-[var(--radius-sm)] border border-dashed border-border hover:border-brand-500/50 hover:bg-brand-500/5 text-sm text-text-secondary hover:text-brand-400 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  {creating ? (
                    <>
                      <Loader2 className="w-4 h-4 animate-spin" />
                      Launching browser VM...
                    </>
                  ) : (
                    <>
                      <Plus className="w-4 h-4" />
                      Launch browser here
                    </>
                  )}
                </button>
              </div>

              {availableBVMs.length > 0 && (
                <>
                  <div className="flex items-center gap-3 text-xs text-text-tertiary">
                    <div className="flex-1 border-t border-border" />
                    <span>or pair an existing one</span>
                    <div className="flex-1 border-t border-border" />
                  </div>
                  <div className="flex items-center gap-3">
                    <label htmlFor="browser-vm-select" className="sr-only">
                      Select browser VM to pair
                    </label>
                    <select
                      id="browser-vm-select"
                      aria-label="Select browser VM to pair"
                      value={selectedBVMId}
                      onChange={(e) => setSelectedBVMId(e.target.value)}
                      className="flex-1 bg-deep border border-border rounded-md px-3 py-2 text-sm text-text-primary"
                    >
                      <option value="">Select a browser VM...</option>
                      {availableBVMs.map((bvm) => (
                        <option key={bvm.id} value={bvm.id}>
                          {bvm.slug} · {formatBrowserVMSize(bvm.vcpus, bvm.memory_mb)} ({bvm.status})
                        </option>
                      ))}
                    </select>
                    <button
                      onClick={handlePair}
                      disabled={!selectedBVMId || pairing}
                      className="px-4 py-2 rounded-md bg-brand-600 text-white text-sm hover:bg-brand-700 disabled:opacity-50"
                    >
                      Pair
                    </button>
                  </div>
                </>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
