export function isRestartRequiredLiveUpdate(error?: string): boolean {
  const text = (error || "").toLowerCase();
  return (
    text.includes("restart the machine") ||
    text.includes("current hermes rootfs") ||
    text.includes("live hermes config sync support") ||
    text.includes("hermes config sync helper")
  );
}
