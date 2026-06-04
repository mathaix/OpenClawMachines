import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Terminal } from "lucide-react";
import { useAuth } from "../lib/auth";

export function CliAuth() {
  const [searchParams] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const { user, loading } = useAuth();

  useEffect(() => {
    if (loading) return;

    // If not logged in, redirect to login with return to cli-auth
    if (!user) {
      const returnPath = `/cli-auth?${searchParams.toString()}`;
      window.location.href = `/login?return=${encodeURIComponent(returnPath)}`;
      return;
    }

    const portStr = searchParams.get("port");
    if (!portStr) {
      setError("Missing port parameter.");
      return;
    }

    const port = parseInt(portStr, 10);
    if (isNaN(port) || port < 1024 || port > 65535) {
      setError("Invalid port number. Must be between 1024 and 65535.");
      return;
    }

    // Request a CLI token — authenticated via ocm_token cookie
    fetch("/api/auth/cli-token", {
      method: "POST",
      credentials: "include",
    })
      .then(async (resp) => {
        if (!resp.ok) {
          const body = await resp.json().catch(() => ({ error: "unknown error" }));
          throw new Error(body.error || `HTTP ${resp.status}`);
        }
        return resp.json();
      })
      .then((data) => {
        window.location.href = `http://localhost:${port}/callback?token=${encodeURIComponent(data.token)}`;
      })
      .catch((err) => {
        setError(`Authentication failed — ${err.message}`);
      });
  }, [searchParams, user, loading]);

  if (error) {
    return (
      <div className="min-h-screen bg-gray-950 flex items-center justify-center px-4">
        <div className="max-w-sm text-center">
          <div className="w-16 h-16 rounded-full bg-red-900/30 flex items-center justify-center mx-auto mb-6">
            <Terminal className="w-7 h-7 text-red-400" />
          </div>
          <h1 className="text-2xl font-bold text-white mb-2">CLI Login Failed</h1>
          <p className="text-gray-400 text-sm">{error}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-gray-950 flex items-center justify-center px-4">
      <div className="max-w-sm text-center">
        <div className="w-16 h-16 rounded-full bg-gray-800 flex items-center justify-center mx-auto mb-6">
          <Terminal className="w-7 h-7 text-orange-400" />
        </div>
        <h1 className="text-2xl font-bold text-white mb-2">
          {loading ? "Loading..." : "Redirecting to CLI..."}
        </h1>
        <p className="text-gray-400 text-sm">
          Completing authentication. You can close this tab once your terminal confirms login.
        </p>
      </div>
    </div>
  );
}
