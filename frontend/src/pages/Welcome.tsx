import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { createAccount } from "../lib/api";
import { toSlug } from "../lib/utils";

const DATA_PLANE_DOMAIN = (import.meta.env.VITE_DATA_PLANE_DOMAIN || "localhost").trim().replace(/^\./, "");

export function Welcome() {
  const { user, setAccount, refreshAccounts } = useAuth();
  const navigate = useNavigate();
  const [displayName, setDisplayName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const effectiveSlug = slugTouched ? slug : toSlug(displayName);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!displayName.trim()) return;

    setSubmitting(true);
    setError(null);
    try {
      const account = await createAccount({ name: displayName.trim(), slug: effectiveSlug });
      setAccount(account);
      await refreshAccounts();
      navigate("/dashboard");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create account");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50 dark:bg-gray-900 px-4">
      <div className="bg-white dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 p-8 w-full max-w-md">
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100 mb-1">
          Welcome to <span className="text-red-500">OpenClaw</span>
        </h1>
        <p className="text-sm text-gray-500 dark:text-gray-400 mb-6">
          Complete your profile to get started.
        </p>

        {user?.email && (
          <p className="text-xs text-gray-400 dark:text-gray-500 mb-4">
            Signed in as {user.email}
          </p>
        )}

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label htmlFor="displayName" className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Display Name
            </label>
            <input
              id="displayName"
              type="text"
              required
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Your name or team name"
              className="w-full rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 px-3 py-2 text-sm text-gray-900 dark:text-gray-100 placeholder-gray-400 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
            />
          </div>

          <div>
            <label htmlFor="slug" className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              URL Slug
              <span className="text-gray-400 dark:text-gray-500 font-normal ml-1">(optional)</span>
            </label>
            <div className="flex items-center gap-1 text-sm text-gray-400 dark:text-gray-500 mb-1">
              <span>{effectiveSlug || "your-slug"}.{DATA_PLANE_DOMAIN}</span>
            </div>
            <input
              id="slug"
              type="text"
              value={slugTouched ? slug : effectiveSlug}
              onChange={(e) => {
                setSlugTouched(true);
                setSlug(e.target.value);
              }}
              placeholder="auto-generated from name"
              className="w-full rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 px-3 py-2 text-sm text-gray-900 dark:text-gray-100 placeholder-gray-400 focus:outline-none focus:ring-2 focus:ring-brand-500 focus:border-transparent"
            />
          </div>

          {error && (
            <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
          )}

          <button
            type="submit"
            disabled={submitting || !displayName.trim()}
            className="w-full bg-gray-900 dark:bg-gray-100 text-white dark:text-gray-900 rounded-lg px-4 py-2 text-sm font-medium hover:bg-gray-800 dark:hover:bg-gray-200 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {submitting ? "Creating..." : "Get Started"}
          </button>
        </form>
      </div>
    </div>
  );
}
