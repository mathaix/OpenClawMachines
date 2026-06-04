import type { BrowserVM } from "./types";

export type BrowserImage = "kernel-stable" | "kernel-experimental";

export const BROWSER_IMAGE_OPTIONS: { id: BrowserImage; label: string }[] = [
  { id: "kernel-stable", label: "Kernel stable" },
  { id: "kernel-experimental", label: "Kernel experimental" },
];

export function browserImageLabelForVM(vm: BrowserVM) {
  if (vm.desired_rootfs_manifest?.includes("kernel-browser-rootfs")) {
    return vm.desired_rootfs_version ? "Kernel stable" : "Kernel experimental";
  }
  if (vm.desired_rootfs_manifest?.includes("browser-rootfs")) {
    return "Legacy CDP (disabled)";
  }
  return "Kernel stable (default)";
}
