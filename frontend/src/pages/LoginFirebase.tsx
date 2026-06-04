import { useState, useEffect, useRef } from "react";
import { useSearchParams, Link } from "react-router-dom";
import {
  signInWithGoogle, signInWithEmail, signUpWithEmail,
  waitForRedirectUser, isRedirectPending, clearRedirectFlag,
} from "../lib/firebase";

const BASE = import.meta.env.VITE_API_URL || "/api";

async function exchangeToken(idToken: string): Promise<{ user: { id: number; email: string } }> {
  const res = await fetch(`${BASE}/auth/session/exchange`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "include",
    body: JSON.stringify({ id_token: idToken }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(body.error || `Exchange failed: ${res.status}`);
  }
  return res.json();
}

export function LoginFirebase() {
  const [searchParams] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [mode, setMode] = useState<"login" | "signup">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const returnTo = (() => {
    const raw = searchParams.get("return") || "/dashboard";
    return raw.startsWith("/") && !raw.startsWith("//") ? raw : "/dashboard";
  })();

  const checkedRedirect = useRef(false);
  useEffect(() => {
    if (checkedRedirect.current || !isRedirectPending()) return;
    checkedRedirect.current = true;
    clearRedirectFlag();

    setLoading(true);
    setError(null);

    waitForRedirectUser().then(async (idToken) => {
      if (!idToken) {
        setError("Sign-in was cancelled or timed out. Please try again.");
        setLoading(false);
        return;
      }
      try {
        await exchangeToken(idToken);
        window.location.href = returnTo;
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Login failed");
        setLoading(false);
      }
    });
  }, [returnTo]);

  async function handleLogin(getIdToken: () => Promise<string>) {
    setError(null);
    setLoading(true);
    try {
      const idToken = await getIdToken();
      if (!idToken) return;
      await exchangeToken(idToken);
      window.location.href = returnTo;
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Login failed";
      setError(message);
      setLoading(false);
    }
  }

  const inputStyle: React.CSSProperties = {
    background: "var(--l-card-bg)",
    border: "1.5px solid var(--l-border)",
    borderRadius: 10,
    color: "var(--l-text)",
  };

  return (
    <div
      className="landing min-h-screen flex items-center justify-center p-4"
      style={{ background: "var(--l-bg)", color: "var(--l-text)" }}
    >
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <Link
            to="/"
            className="text-lg font-semibold inline-block mb-6"
            style={{ letterSpacing: "-0.02em" }}
          >
            <span style={{ color: "var(--l-accent)" }}>OpenClaw</span>
            <span style={{ color: "var(--l-teal)" }}>Machines</span>
          </Link>
          <h1 className="text-2xl font-bold" style={{ color: "var(--l-text)" }}>
            {mode === "signup" ? "Create your account" : "Sign in"}
          </h1>
          <p className="text-sm mt-2" style={{ color: "var(--l-text-muted)" }}>
            {mode === "signup" ? "Get started with OpenClaw Machines" : "Choose a sign-in method to continue"}
          </p>
        </div>

        <div className="space-y-3">
          <button
            onClick={() => handleLogin(signInWithGoogle)}
            disabled={loading}
            className="w-full flex items-center justify-center gap-3 px-4 py-3 font-medium disabled:opacity-50 transition-colors"
            style={{
              background: "#fff",
              color: "#1a1a2e",
              borderRadius: 10,
              border: "1.5px solid var(--l-border)",
              cursor: "pointer",
            }}
          >
            <svg className="w-5 h-5" viewBox="0 0 24 24">
              <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z"/>
              <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
              <path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"/>
              <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"/>
            </svg>
            {loading ? "Signing in..." : "Continue with Google"}
          </button>
        </div>

        <div className="flex items-center gap-3 my-6">
          <div className="flex-1 h-px" style={{ background: "var(--l-border)" }} />
          <span className="text-xs uppercase" style={{ color: "var(--l-text-muted)" }}>or</span>
          <div className="flex-1 h-px" style={{ background: "var(--l-border)" }} />
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            const fn = mode === "signup"
              ? () => signUpWithEmail(email, password)
              : () => signInWithEmail(email, password);
            handleLogin(fn);
          }}
          className="space-y-3"
        >
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="Email address"
            required
            className="w-full px-4 py-3 text-sm focus:outline-none"
            style={inputStyle}
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Password"
            required
            minLength={6}
            className="w-full px-4 py-3 text-sm focus:outline-none"
            style={inputStyle}
          />
          <button
            type="submit"
            disabled={loading}
            className="w-full px-4 py-3 text-white font-semibold text-sm disabled:opacity-50 transition-all"
            style={{
              borderRadius: 10,
              background: "var(--l-accent)",
              border: "none",
              cursor: "pointer",
            }}
          >
            {loading ? "Signing in..." : mode === "signup" ? "Create account" : "Sign in with email"}
          </button>
        </form>

        <p className="text-center text-sm mt-4" style={{ color: "var(--l-text-muted)" }}>
          {mode === "login" ? (
            <>
              {"Don't have an account? "}
              <button
                onClick={() => setMode("signup")}
                className="font-medium"
                style={{ color: "var(--l-accent)" }}
              >
                Sign up
              </button>
            </>
          ) : (
            <>
              {"Already have an account? "}
              <button
                onClick={() => setMode("login")}
                className="font-medium"
                style={{ color: "var(--l-accent)" }}
              >
                Sign in
              </button>
            </>
          )}
        </p>

        {error && (
          <div
            className="mt-4 p-3 text-sm"
            style={{
              background: "rgba(224,80,48,0.08)",
              border: "1px solid rgba(224,80,48,0.25)",
              borderRadius: 10,
              color: "var(--l-accent)",
            }}
          >
            {error}
          </div>
        )}
      </div>
    </div>
  );
}
