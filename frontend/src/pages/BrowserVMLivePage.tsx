import { useState, useEffect } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { ArrowLeft, Globe, Loader2, RefreshCw, X } from "lucide-react";
import { useAuth } from "../lib/auth";
import { getBrowserVM, browserVMLiveUrl } from "../lib/api";
import type { BrowserVM } from "../lib/types";

/**
 * Full-screen live view of a paired browser VM. Opens in a new tab via
 * `target="_blank"` from the machine's Browser tab.
 *
 * Layout: a thin top bar (back/status/refresh) over the iframe which
 * fills the rest of the viewport. The iframe points at the backend's
 * /browser-vms/:id/live/ proxy.
 */
export function BrowserVMLivePage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { account } = useAuth();
  const accountId = account?.id;

  const [bvm, setBvm] = useState<BrowserVM | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [iframeKey, setIframeKey] = useState(0);

  useEffect(() => {
    if (!id || !accountId) return;
    let cancelled = false;
    setLoading(true);
    getBrowserVM(accountId, id)
      .then((data) => {
        if (!cancelled) {
          setBvm(data);
          setError(null);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to load browser VM");
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [id, accountId]);

  const handleRefresh = () => setIframeKey((k) => k + 1);
  const handleClose = () => {
    if (window.opener) {
      window.close();
    } else {
      navigate(-1);
    }
  };

  if (!accountId) {
    return (
      <div className="h-screen flex items-center justify-center bg-deep text-text-primary">
        Sign in required
      </div>
    );
  }

  return (
    <div className="h-screen w-screen flex flex-col bg-black">
      {/* Header bar — kept minimal so the iframe gets almost all the vertical space */}
      <header className="flex items-center justify-between h-10 px-3 bg-card border-b border-border flex-shrink-0">
        <div className="flex items-center gap-3 min-w-0">
          <button
            onClick={handleClose}
            className="p-1 rounded hover:bg-white/5 text-text-tertiary hover:text-text-primary transition-colors"
            title={window.opener ? "Close tab" : "Back"}
          >
            {window.opener ? <X className="w-4 h-4" /> : <ArrowLeft className="w-4 h-4" />}
          </button>
          <div className="flex items-center gap-2 min-w-0">
            <Globe className="w-4 h-4 text-green-400 flex-shrink-0" />
            <span className="text-sm font-medium text-text-primary truncate">
              {bvm?.slug ?? (loading ? "Loading…" : "Browser VM")}
            </span>
            {bvm && (
              <span
                className={`text-[11px] px-2 py-0.5 rounded-full font-medium flex-shrink-0 ${
                  bvm.status === "running"
                    ? "bg-green-500/10 text-green-400 border border-green-500/20"
                    : "bg-yellow-500/10 text-yellow-400 border border-yellow-500/20"
                }`}
              >
                {bvm.status}
              </span>
            )}
            {bvm?.vm_ip && (
              <span className="text-xs text-text-tertiary truncate hidden md:inline">
                CDP {bvm.vm_ip}:{bvm.cdp_port}
              </span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={handleRefresh}
            disabled={!bvm || bvm.status !== "running"}
            className="p-1 rounded hover:bg-white/5 text-text-tertiary hover:text-text-primary disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            title="Reload live view"
          >
            <RefreshCw className="w-4 h-4" />
          </button>
        </div>
      </header>

      {/* Live view — fills remaining viewport */}
      <main className="flex-1 min-h-0 relative">
        {loading && (
          <div className="absolute inset-0 flex items-center justify-center text-text-tertiary gap-2">
            <Loader2 className="w-5 h-5 animate-spin" />
            Loading browser VM…
          </div>
        )}
        {!loading && error && (
          <div className="absolute inset-0 flex items-center justify-center text-red-400 px-4 text-center">
            {error}
          </div>
        )}
        {!loading && !error && bvm && bvm.status === "running" && (
          <iframe
            key={iframeKey}
            title={`Live browser ${bvm.slug}`}
            src={browserVMLiveUrl(accountId, bvm.id)}
            className="w-full h-full border-0 bg-black"
            allow="clipboard-read; clipboard-write; fullscreen; autoplay"
          />
        )}
        {!loading && !error && bvm && bvm.status !== "running" && (
          <div className="absolute inset-0 flex items-center justify-center text-text-tertiary">
            Live preview is available when the browser VM is running (current: {bvm.status}).
          </div>
        )}
      </main>
    </div>
  );
}
