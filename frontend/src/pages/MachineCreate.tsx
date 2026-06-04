import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { createMachine, setSecret } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useSizes } from "../lib/useSizes";
import { selectDefaultRegion, useRegions } from "../lib/useRegions";
import type { MachineKind } from "../lib/types";

export function MachineCreate() {
  const navigate = useNavigate();
  const { account } = useAuth();
  const sizes = useSizes();
  const regions = useRegions();
  const [machineKind, setMachineKind] = useState<MachineKind>("openclaw");
  const [name, setName] = useState("");
  const [sizeId, setSizeId] = useState("standard");
  const [regionCode, setRegionCode] = useState("");
  const [releaseMode, setReleaseMode] = useState<"channel" | "pinned">("channel");
  const [releaseChannel, setReleaseChannel] = useState<"stable" | "rc" | "dev">("stable");
  const [rootfsVersion, setRootfsVersion] = useState("");
  const [openclawVersion, setOpenclawVersion] = useState("");
  const [anthropicKey, setAnthropicKey] = useState("");
  const [discordToken, setDiscordToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const selectedSize = sizes.find((s) => s.id === sizeId) ?? sizes.find((s) => s.is_default) ?? sizes[0];

  useEffect(() => {
    if (regions.length === 0) return;
    if (regionCode === "" || !regions.some((r) => r.code === regionCode)) {
      setRegionCode(selectDefaultRegion(regions));
    }
  }, [regions, regionCode]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!account || !selectedSize) return;
    setSubmitting(true);
    setError(null);
    try {
      if (machineKind === "openclaw" && releaseMode === "pinned" && !rootfsVersion.trim() && !openclawVersion.trim()) {
        setError("Set at least one pinned version (rootfs or OpenClaw), or switch to channel mode.");
        setSubmitting(false);
        return;
      }
      const payload: Parameters<typeof createMachine>[1] = {
        name,
        kind: machineKind,
        size: selectedSize.id,
        preferred_region: regionCode || undefined,
        auto_start: true,
        runtime_source: "artifact",
      };
      if (machineKind === "openclaw" && releaseMode === "channel") {
        payload.channel = releaseChannel;
      } else if (machineKind === "openclaw") {
        payload.rootfs_version = rootfsVersion.trim() || undefined;
        payload.openclaw_version = openclawVersion.trim() || undefined;
      }
      const machine = await createMachine(account.id, payload);
      // Store secrets (if provided)
      if (anthropicKey.trim()) {
        await setSecret(account.id, machine.id, "ANTHROPIC_API_KEY", anthropicKey.trim());
      }
      if (discordToken.trim()) {
        await setSecret(account.id, machine.id, "DISCORD_BOT_TOKEN", discordToken.trim());
      }
      navigate(`/machines/${machine.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create machine");
    } finally {
      setSubmitting(false);
    }
  };

  if (!account) return <p className="text-gray-500">Loading account...</p>;

  return (
    <div className="max-w-lg">
      <h1 className="text-2xl font-semibold text-gray-900 mb-6">Create Machine</h1>

      <form onSubmit={handleSubmit} className="space-y-5">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Kind</label>
          <div className="grid grid-cols-2 gap-2">
            {([
              ["openclaw", "OpenClaw"],
              ["hermes", "Hermes"],
            ] as const).map(([kind, label]) => (
              <button
                key={kind}
                type="button"
                onClick={() => setMachineKind(kind)}
                className={`rounded-lg border px-3 py-2 text-sm ${
                  machineKind === kind
                    ? "border-brand-600 bg-brand-50 text-brand-700"
                    : "border-gray-200 text-gray-600 hover:border-gray-300"
                }`}
              >
                {label}
              </button>
            ))}
          </div>
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Machine Name</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm"
            placeholder="Jarvis-01"
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Sizing</label>
          <div className="grid grid-cols-3 gap-3">
            {sizes.map((size) => (
              <button
                key={size.id}
                type="button"
                onClick={() => setSizeId(size.id)}
                className={`relative rounded-lg border p-3 text-left transition-colors ${
                  sizeId === size.id
                    ? "border-brand-600 bg-brand-50 ring-1 ring-brand-600"
                    : "border-gray-200 hover:border-gray-300"
                }`}
              >
                <p className="text-sm font-medium text-gray-900">{size.label}</p>
                <p className="text-xs text-gray-500 mt-0.5">{size.description}</p>
              </button>
            ))}
          </div>
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Region</label>
          <select
            value={regionCode}
            onChange={(e) => setRegionCode(e.target.value)}
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm bg-white"
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
        <div className="space-y-2">
          <label className="block text-sm font-medium text-gray-700">Runtime Version</label>
          <div className="grid grid-cols-2 gap-2">
            <button
              type="button"
              onClick={() => setReleaseMode("channel")}
              className={`rounded-lg border px-3 py-2 text-sm ${
                releaseMode === "channel"
                  ? "border-brand-600 bg-brand-50 text-brand-700"
                  : "border-gray-200 text-gray-600 hover:border-gray-300"
              }`}
            >
              Track Channel
            </button>
            <button
              type="button"
              onClick={() => setReleaseMode("pinned")}
              className={`rounded-lg border px-3 py-2 text-sm ${
                releaseMode === "pinned"
                  ? "border-brand-600 bg-brand-50 text-brand-700"
                  : "border-gray-200 text-gray-600 hover:border-gray-300"
              }`}
            >
              Pin Version
            </button>
          </div>
          {releaseMode === "channel" ? (
            <select
              value={releaseChannel}
              onChange={(e) => setReleaseChannel(e.target.value as "stable" | "rc" | "dev")}
              className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm bg-white"
            >
              <option value="stable">stable</option>
              <option value="rc">rc</option>
              <option value="dev">dev</option>
            </select>
          ) : (
            <div className="space-y-2">
              <input
                type="text"
                value={rootfsVersion}
                onChange={(e) => setRootfsVersion(e.target.value)}
                className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm"
                placeholder="rootfs version (optional)"
              />
              <input
                type="text"
                value={openclawVersion}
                onChange={(e) => setOpenclawVersion(e.target.value)}
                className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm"
                placeholder="openclaw version (optional)"
              />
            </div>
          )}
        </div>
        )}

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Anthropic API Key <span className="text-gray-400 font-normal">(optional)</span>
          </label>
          <input
            type="password"
            value={anthropicKey}
            onChange={(e) => setAnthropicKey(e.target.value)}
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm font-mono"
            placeholder="sk-ant-..."
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Discord Bot Token <span className="text-gray-400 font-normal">(optional)</span>
          </label>
          <input
            type="password"
            value={discordToken}
            onChange={(e) => setDiscordToken(e.target.value)}
            className="w-full border border-gray-300 rounded-lg px-3 py-2 text-sm font-mono"
            placeholder="MTk..."
          />
        </div>

        {error && <p className="text-red-600 text-sm">{error}</p>}

        <button
          type="submit"
          disabled={submitting}
          className="w-full bg-brand-600 text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-brand-700 disabled:opacity-50"
        >
          {submitting ? "Creating..." : "Create Machine"}
        </button>
      </form>
    </div>
  );
}
