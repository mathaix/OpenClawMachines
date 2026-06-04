// Sizing tiers for new browser VMs. Backend default (browser_vms.go) is
// 2 vCPU / 4096 MB; these named tiers expose the common picks. Heavier
// tiers fit sustained sessions on memory-heavy SPAs (LinkedIn, Gmail,
// Figma) and multi-tab agent workflows.
export type BrowserVMSize = {
  id: string;
  label: string;
  vcpus: number;
  memory_mb: number;
};

export const BROWSER_VM_SIZES: BrowserVMSize[] = [
  { id: "small",  label: "Small  · 2 vCPU · 2 GB",   vcpus: 2, memory_mb: 2048 },
  { id: "medium", label: "Medium · 2 vCPU · 4 GB",   vcpus: 2, memory_mb: 4096 },
  { id: "large",  label: "Large  · 2 vCPU · 8 GB",   vcpus: 2, memory_mb: 8192 },
  { id: "xlarge", label: "X-Large · 4 vCPU · 12 GB", vcpus: 4, memory_mb: 12288 },
];

// Default tier used when the user hasn't explicitly picked one. Mirrors
// the backend default so create-without-size-arg lands here too.
export const DEFAULT_BROWSER_VM_SIZE: BrowserVMSize = BROWSER_VM_SIZES[1];

// Compact "2 vCPU · 4 GB" label for display next to a known VM.
export function formatBrowserVMSize(vcpus: number, memoryMB: number): string {
  return `${vcpus} vCPU · ${Math.round(memoryMB / 1024)} GB`;
}
