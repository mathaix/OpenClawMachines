import { useState, useEffect } from "react";
import {
  putMachineCredential, deleteMachineCredential, testMachineCredential,
} from "../lib/api";

interface AnthropicSubscriptionConnectProps {
  accountId: number;
  machineId: string;
  initialConnected?: boolean;
  onCredentialChange?: () => void;
}

type Step = "idle" | "input" | "saving" | "connected" | "error";
type TestResult = { ok: boolean; error?: string; expires_in_hours?: number } | null;

export function AnthropicSubscriptionConnect({
  accountId, machineId, initialConnected, onCredentialChange,
}: AnthropicSubscriptionConnectProps) {
  const [step, setStep] = useState<Step>(initialConnected ? "connected" : "idle");
  const [keyInput, setKeyInput] = useState("");
  const [errorMsg, setErrorMsg] = useState("");
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<TestResult>(null);
  const [showInstructions, setShowInstructions] = useState(false);

  // Sync with async credential fetch — initialConnected may arrive after first render
  useEffect(() => {
    if (initialConnected && step === "idle") {
      setStep("connected");
    }
  }, [initialConnected]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSave = async () => {
    if (!keyInput.trim()) return;
    setStep("saving");
    setErrorMsg("");
    try {
      const key = keyInput.trim();
      const isSetupToken = key.startsWith("sk-ant-oat");
      await putMachineCredential(accountId, machineId, "anthropic", {
        value: key,
        credential_type: isSetupToken ? "subscription_key" : "api_key",
        label: isSetupToken ? "Anthropic (Subscription)" : "Anthropic API Key",
      });
      setKeyInput("");
      setStep("connected");
      onCredentialChange?.();
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to save key");
      setStep("error");
    }
  };

  const handleTest = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const result = await testMachineCredential(accountId, machineId, "anthropic");
      setTestResult(result);
    } catch {
      setTestResult({ ok: false, error: "failed to reach backend" });
    } finally {
      setTesting(false);
    }
  };

  const handleDisconnect = async () => {
    try {
      await deleteMachineCredential(accountId, machineId, "anthropic");
    } catch (e) {
      console.warn("Failed to delete anthropic credential:", e);
    }
    setStep("idle");
    setTestResult(null);
    onCredentialChange?.();
  };

  const handleCancel = () => {
    setStep("idle");
    setKeyInput("");
    setErrorMsg("");
    setShowInstructions(false);
  };

  const isConnected = step === "connected";
  const isExpanded = step === "input" || step === "saving" || step === "error";

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
          <img src="/providers/anthropic.svg" alt="Anthropic" className="w-full h-full object-contain" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[12px] md:text-[13px] font-semibold text-text-primary mb-px">
            Anthropic Claude
          </div>
          <div className="text-[11px] text-text-tertiary">
            {isConnected ? "Connected via setup token" : "Setup token · Claude Pro/Max/Team"}
          </div>
        </div>
        <div className="flex-shrink-0">
          {isConnected ? (
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
                onClick={() => setStep("input")}
                className="text-[10px] text-text-muted hover:text-text-secondary transition-colors"
              >
                Update
              </button>
              <button
                onClick={handleDisconnect}
                className="text-[10px] text-text-muted hover:text-red-400 transition-colors"
              >
                ×
              </button>
            </div>
          ) : (
            <button
              onClick={() => setStep("input")}
              className="text-xs md:text-sm font-medium px-2.5 py-1 rounded-[var(--radius-sm)] border border-border hover:bg-[rgba(255,255,255,0.03)] text-text-secondary transition-colors"
            >
              {isExpanded ? "Cancel" : "Connect"}
            </button>
          )}
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
            ? "Key valid — API call succeeded"
            : `Key failed: ${testResult.error || "unknown error"}`}
        </div>
      )}

      {/* Inline key input */}
      {isExpanded && (
        <div className="mt-1.5 px-2 space-y-2">
          <div className="space-y-1.5">
            <div className="flex items-center gap-2">
              <p className="text-[11px] font-medium text-text-primary">Paste your subscription key</p>
              <button
                onClick={() => setShowInstructions(!showInstructions)}
                className="text-[10px] text-brand-500 hover:text-brand-400 underline"
              >
                {showInstructions ? "Hide" : "How?"}
              </button>
            </div>

            {showInstructions && (
              <div className="text-[11px] text-text-tertiary bg-[rgba(255,255,255,0.02)] border border-border rounded-[var(--radius-sm)] p-3 space-y-1.5 leading-relaxed">
                <p className="font-medium text-text-secondary">Getting your Claude setup token:</p>
                <ol className="list-decimal list-inside space-y-1 pl-0.5">
                  <li>Run <code className="text-[10px] bg-[rgba(255,255,255,0.05)] px-1 py-0.5 rounded">claude setup-token</code> in your local terminal</li>
                  <li>Copy the token — it starts with <code className="text-[10px] bg-[rgba(255,255,255,0.05)] px-1 py-0.5 rounded">sk-ant-oat-</code></li>
                  <li>Paste it below</li>
                </ol>
                <p className="text-[10px] text-text-muted mt-1">Requires a Claude Pro, Max, or Team plan. Your token is encrypted and stored securely.</p>
              </div>
            )}

            <div className="flex gap-2">
              <input
                type="password"
                value={keyInput}
                onChange={(e) => setKeyInput(e.target.value)}
                placeholder="sk-ant-oat-..."
                className="flex-1 px-3 py-1.5 text-[12px] bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                onKeyDown={(e) => e.key === "Enter" && handleSave()}
                autoFocus
              />
              <button
                onClick={handleSave}
                disabled={!keyInput.trim() || step === "saving"}
                className="text-[11px] font-medium px-2.5 py-1 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
              >
                {step === "saving" ? "Saving..." : "Save"}
              </button>
            </div>
          </div>
          {step === "error" && (
            <p className="text-[11px] text-red-400">{errorMsg}</p>
          )}
          <button onClick={handleCancel} className="text-[11px] text-text-muted hover:text-text-secondary transition-colors">
            Cancel
          </button>
        </div>
      )}
    </div>
  );
}
