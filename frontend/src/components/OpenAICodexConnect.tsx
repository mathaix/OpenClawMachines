import { useEffect, useState } from "react";
import { machineExec, putMachineCredential, deleteMachineCredential, testMachineCredential } from "../lib/api";

interface OpenAICodexConnectProps {
  accountId: number;
  machineId: string;
  machineStatus?: string;
}

type OAuthStep = "idle" | "checking" | "generating" | "waiting_for_user" | "exchanging" | "success" | "error";
type TestResult = { ok: boolean; error?: string; code?: string; status?: number; expires_in_hours?: number } | null;

export function OpenAICodexConnect({ accountId, machineId, machineStatus }: OpenAICodexConnectProps) {
  const [step, setStep] = useState<OAuthStep>("checking");
  const [authUrl, setAuthUrl] = useState("");
  const [callbackUrl, setCallbackUrl] = useState("");
  const [errorMsg, setErrorMsg] = useState("");
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<TestResult>(null);

  useEffect(() => {
    if (machineStatus !== "running") {
      setStep("idle");
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const result = await machineExec(accountId, machineId, ["codex-auth", "check"]);
        if (cancelled) return;
        if (result.exit_code === 0) {
          const parsed = JSON.parse(result.stdout.trim());
          setStep(parsed.connected ? "success" : "idle");
        } else {
          setStep("idle");
        }
      } catch {
        if (!cancelled) setStep("idle");
      }
    })();
    return () => { cancelled = true; };
  }, [accountId, machineId, machineStatus]);

  const handleStartAuth = async () => {
    setStep("generating");
    setErrorMsg("");
    try {
      const result = await machineExec(accountId, machineId, ["codex-auth", "generate-url"]);
      if (result.exit_code !== 0) {
        throw new Error(result.stderr || "Failed to generate auth URL");
      }
      const parsed = JSON.parse(result.stdout.trim());
      setAuthUrl(parsed.url);
      setStep("waiting_for_user");
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to start auth flow");
      setStep("error");
    }
  };

  const handleSubmitCallback = async () => {
    if (!callbackUrl.trim()) return;
    setStep("exchanging");
    setErrorMsg("");
    try {
      const result = await machineExec(accountId, machineId, ["codex-auth", "exchange", callbackUrl.trim()]);
      if (result.exit_code !== 0) {
        throw new Error(result.stderr || "Token exchange failed");
      }
      // Store a flag credential in the backend so config assembly includes
      // the codex provider and auth.profiles. The actual token lives on the VM.
      try {
        await putMachineCredential(accountId, machineId, "openai-codex", {
          value: "vm-managed",
          credential_type: "oauth",
          label: "ChatGPT (OAuth)",
        });
      } catch (e) {
        console.warn("Failed to store codex credential flag:", e);
      }
      setStep("success");
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Token exchange failed");
      setStep("error");
    }
  };

  const handleReset = () => {
    setStep("idle");
    setAuthUrl("");
    setCallbackUrl("");
    setErrorMsg("");
    setTestResult(null);
  };

  const handleDisconnect = async () => {
    try {
      await deleteMachineCredential(accountId, machineId, "openai-codex");
    } catch (e) {
      console.warn("Failed to delete codex credential:", e);
    }
    handleReset();
  };

  const handleTest = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const result = await testMachineCredential(accountId, machineId, "openai-codex");
      setTestResult(result);
    } catch {
      setTestResult({ ok: false, error: "failed to reach backend" });
    } finally {
      setTesting(false);
    }
  };

  const isRunning = machineStatus === "running";
  const isConnected = step === "success";

  return (
    <div className="flex flex-col">
      <div
        className={`flex items-center gap-3 bg-elevated border rounded-[var(--radius-sm)] p-3 md:p-[14px] transition-all hover:bg-card-hover ${
          isConnected
            ? "border-border hover:border-[var(--border-hover)]"
            : "border-dashed border-[var(--border-hover)]"
        }`}
      >
        <div className="flex-shrink-0 w-[36px] h-[36px] md:w-10 md:h-10 rounded-lg flex items-center justify-center bg-white p-1.5">
          <img src="/providers/openai.svg" alt="OpenAI" className="w-full h-full object-contain" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[12px] md:text-[13px] font-semibold text-text-primary mb-px">
            OpenAI Codex (ChatGPT)
          </div>
          <div className="text-[11px] text-text-tertiary">
            {!isRunning
              ? "Machine must be running"
              : step === "checking"
                ? "Checking..."
                : isConnected
                  ? "Connected via OAuth"
                  : "OAuth · ChatGPT Plus/Pro"}
          </div>
        </div>
        <div className="flex-shrink-0">
          {isRunning && isConnected ? (
            <div className="flex items-center gap-2">
              <span className="text-[11px] font-medium text-green-500">Connected</span>
              <button
                onClick={handleTest}
                disabled={testing}
                className="text-[10px] text-text-muted hover:text-text-secondary transition-colors disabled:opacity-50"
              >
                {testing ? "..." : "Test"}
              </button>
              <button
                onClick={handleStartAuth}
                className="text-[10px] text-text-muted hover:text-text-secondary transition-colors"
              >
                Reconnect
              </button>
              <button
                onClick={handleDisconnect}
                className="text-[10px] text-text-muted hover:text-red-400 transition-colors"
              >
                ×
              </button>
            </div>
          ) : isRunning && step === "idle" ? (
            <button
              onClick={handleStartAuth}
              className="text-xs md:text-sm font-medium px-2.5 py-1 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary transition-colors"
            >
              Connect
            </button>
          ) : null}
        </div>
      </div>

      {/* Test result */}
      {testResult && (
        <div className={`text-[11px] px-3 py-2 mt-1.5 mx-2 rounded-[var(--radius-sm)] ${
          testResult.ok
            ? "bg-[rgba(52,211,153,0.06)] border border-[rgba(52,211,153,0.15)] text-emerald-500"
            : "bg-[rgba(248,113,113,0.06)] border border-[rgba(248,113,113,0.15)] text-red-400"
        }`}>
          {testResult.ok
            ? `Token valid — expires in ${testResult.expires_in_hours}h`
            : (
              <div className="space-y-1">
                <div>Token failed{testResult.status ? ` (HTTP ${testResult.status})` : ""}{testResult.code ? ` [${testResult.code}]` : ""}</div>
                {testResult.error && (
                  <div className="text-[10px] opacity-70 break-all whitespace-pre-wrap">{testResult.error}</div>
                )}
              </div>
            )}
        </div>
      )}

      {/* OAuth flow content */}
      {step === "generating" && (
        <p className="text-[11px] text-text-tertiary mt-1.5 px-2">Generating auth URL...</p>
      )}

      {step === "waiting_for_user" && (
        <div className="mt-1.5 px-2 space-y-2">
          <div className="space-y-1">
            <p className="text-[11px] font-medium text-text-primary">Step 1: Open this link and sign in with your ChatGPT account</p>
            <a
              href={authUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 text-[11px] font-medium text-brand-500 hover:text-brand-400 underline break-all"
            >
              Open OpenAI Login
            </a>
          </div>
          <div className="space-y-1">
            <p className="text-[11px] font-medium text-text-primary">
              Step 2: After signing in, copy the URL from your browser&apos;s address bar and paste it here:
            </p>
            <div className="flex gap-2">
              <input
                type="text"
                value={callbackUrl}
                onChange={(e) => setCallbackUrl(e.target.value)}
                placeholder="http://localhost:1455/auth/callback?code=..."
                className="flex-1 px-3 py-1.5 text-[12px] bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                onKeyDown={(e) => e.key === "Enter" && handleSubmitCallback()}
              />
              <button
                onClick={handleSubmitCallback}
                disabled={!callbackUrl.trim()}
                className="text-[11px] font-medium px-2.5 py-1 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
              >
                Submit
              </button>
            </div>
          </div>
          <button onClick={handleReset} className="text-[11px] text-text-muted hover:text-text-secondary transition-colors">
            Cancel
          </button>
        </div>
      )}

      {step === "exchanging" && (
        <p className="text-[11px] text-text-tertiary mt-1.5 px-2">Exchanging code for tokens...</p>
      )}

      {step === "error" && (
        <div className="mt-1.5 px-2 space-y-2">
          <p className="text-[11px] text-red-400">{errorMsg}</p>
          <button onClick={handleReset} className="text-[11px] text-text-muted hover:text-text-secondary underline transition-colors">
            Try again
          </button>
        </div>
      )}
    </div>
  );
}
