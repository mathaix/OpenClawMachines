import { useState, useEffect, useCallback } from "react";
import { getMachineAssembledConfig } from "../lib/api";
import { CopyButton } from "./CopyButton";

interface MachineConfigProps {
  accountId: number;
  machineId: string;
}

export function MachineConfig({ accountId, machineId }: MachineConfigProps) {
  const [config, setConfig] = useState<Record<string, unknown> | null>(null);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchConfig = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await getMachineAssembledConfig(accountId, machineId);
      // Response may have { config, warnings } or be raw config JSON
      if (data && typeof data === "object" && "config" in data && "warnings" in data) {
        setConfig(data.config as Record<string, unknown>);
        setWarnings((data.warnings as string[]) || []);
      } else {
        setConfig(data);
        setWarnings([]);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load config");
    } finally {
      setLoading(false);
    }
  }, [accountId, machineId]);

  // Auto-load config on mount
  useEffect(() => {
    fetchConfig();
  }, [fetchConfig]);

  const formattedConfig = config ? JSON.stringify(config, null, 2) : "";

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium text-gray-900 dark:text-gray-100">Assembled Config</h3>
          <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
            The full gateway configuration assembled from capabilities, credentials, and platform defaults.
          </p>
        </div>
        <div className="flex gap-2">
          {config && (
            <CopyButton text={formattedConfig} />
          )}
          <button
            onClick={fetchConfig}
            disabled={loading}
            className="px-3 py-1.5 text-sm font-medium text-brand-600 dark:text-brand-400 border border-brand-300 dark:border-brand-700 rounded-lg hover:bg-brand-50 dark:hover:bg-brand-900/20 disabled:opacity-50"
          >
            {loading ? "Loading..." : config ? "Refresh" : "Load Config"}
          </button>
        </div>
      </div>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-red-700 dark:text-red-400 text-sm rounded-lg p-3">
          {error}
        </div>
      )}

      {warnings.length > 0 && (
        <div className="bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 text-yellow-700 dark:text-yellow-400 text-sm rounded-lg p-3">
          <p className="font-medium mb-1">Warnings</p>
          <ul className="list-disc list-inside space-y-0.5">
            {warnings.map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </div>
      )}

      {config && (
        <div className="bg-gray-50 dark:bg-surface-deep rounded-lg border border-gray-200 dark:border-border overflow-hidden">
          <pre className="p-4 text-xs font-mono text-gray-800 dark:text-gray-200 overflow-x-auto max-h-[600px] overflow-y-auto">
            {formattedConfig}
          </pre>
        </div>
      )}
    </div>
  );
}
