import { useEffect, useState, useCallback } from "react";
import { listSecrets, setSecret, deleteSecret, type SecretEntry } from "../lib/api";

const SUGGESTED_KEYS = ["ANTHROPIC_API_KEY", "DISCORD_BOT_TOKEN", "OPENAI_API_KEY"];

interface SecretVaultProps {
  accountId: number;
  machineId: string;
}

export function SecretVault({ accountId, machineId }: SecretVaultProps) {
  const [secrets, setSecrets] = useState<SecretEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const fetchSecrets = useCallback(async () => {
    try {
      const data = await listSecrets(accountId, machineId);
      setSecrets(data || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load secrets");
    } finally {
      setLoading(false);
    }
  }, [accountId, machineId]);

  useEffect(() => {
    fetchSecrets();
  }, [fetchSecrets]);

  const handleAdd = async () => {
    if (!newKey.trim() || !newValue.trim()) return;
    setSaving(true);
    setError(null);
    try {
      await setSecret(accountId, machineId, newKey.trim(), newValue.trim());
      setNewKey("");
      setNewValue("");
      setShowAdd(false);
      await fetchSecrets();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save secret");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (key: string) => {
    setDeleting(key);
    setError(null);
    try {
      await deleteSecret(accountId, machineId, key);
      await fetchSecrets();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete secret");
    } finally {
      setDeleting(null);
    }
  };

  if (loading) return <p className="text-sm text-gray-500 dark:text-gray-400">Loading secrets...</p>;

  const existingKeys = new Set(secrets.map((s) => s.key));
  const suggestions = SUGGESTED_KEYS.filter((k) => !existingKeys.has(k));

  return (
    <div className="space-y-4">
      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-red-700 dark:text-red-400 text-sm rounded-lg p-3">
          {error}
        </div>
      )}

      {secrets.length > 0 ? (
        <div className="space-y-2">
          {secrets.map((sec) => (
            <div key={sec.key} className="flex items-center justify-between bg-gray-50 dark:bg-surface rounded-lg px-4 py-3">
              <div>
                <p className="text-sm font-mono font-medium text-gray-900 dark:text-gray-100">{sec.key}</p>
                <p className="text-xs text-gray-500 dark:text-gray-400">Updated {new Date(sec.updated_at).toLocaleDateString()}</p>
              </div>
              <button
                onClick={() => handleDelete(sec.key)}
                disabled={deleting === sec.key}
                className="text-xs text-red-600 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300 disabled:opacity-50"
              >
                {deleting === sec.key ? "..." : "Remove"}
              </button>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-sm text-gray-500 dark:text-gray-400">No secrets configured.</p>
      )}

      {showAdd ? (
        <div className="border border-gray-200 dark:border-border rounded-lg p-4 space-y-3">
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Key</label>
            <input
              type="text"
              value={newKey}
              onChange={(e) => setNewKey(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ""))}
              className="w-full border border-gray-300 dark:border-border rounded-lg px-3 py-2 text-sm font-mono bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100"
              placeholder="API_KEY_NAME"
            />
            {suggestions.length > 0 && !newKey && (
              <div className="flex gap-2 mt-2">
                {suggestions.map((s) => (
                  <button
                    key={s}
                    type="button"
                    onClick={() => setNewKey(s)}
                    className="text-xs px-2 py-1 rounded bg-brand-50 text-brand-700 hover:bg-brand-100 dark:bg-brand-600/10 dark:text-brand-400 dark:hover:bg-brand-600/20"
                  >
                    {s}
                  </button>
                ))}
              </div>
            )}
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Value</label>
            <input
              type="password"
              value={newValue}
              onChange={(e) => setNewValue(e.target.value)}
              className="w-full border border-gray-300 dark:border-border rounded-lg px-3 py-2 text-sm font-mono bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100"
              placeholder="sk-..."
            />
          </div>
          <div className="flex gap-2">
            <button
              onClick={handleAdd}
              disabled={saving || !newKey.trim() || !newValue.trim()}
              className="px-4 py-2 text-sm font-medium text-white bg-brand-600 rounded-lg hover:bg-brand-700 disabled:opacity-50"
            >
              {saving ? "Saving..." : "Save"}
            </button>
            <button
              onClick={() => {
                setShowAdd(false);
                setNewKey("");
                setNewValue("");
              }}
              className="px-4 py-2 text-sm font-medium text-gray-700 dark:text-gray-300 border border-gray-300 dark:border-border rounded-lg hover:bg-gray-50 dark:hover:bg-surface-elevated"
            >
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <button
          onClick={() => setShowAdd(true)}
          className="text-sm font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400 dark:hover:text-brand-300"
        >
          + Add Secret
        </button>
      )}
    </div>
  );
}
