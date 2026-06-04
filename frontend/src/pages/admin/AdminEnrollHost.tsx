import { useState } from "react";
import { createEnrollmentToken } from "@/lib/api";
import { useToast } from "@/components/Toast";

interface EnrollResult {
  token: string;
  installCommand: string;
  expiresAt: string;
}

interface Props {
  onClose: () => void;
  onComplete: () => void;
}

export function AdminEnrollHost({ onClose, onComplete }: Props) {
  const [step, setStep] = useState(1);
  const [provider, setProvider] = useState("ovhcloud");
  const [creating, setCreating] = useState(false);
  const [result, setResult] = useState<EnrollResult | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const { toast } = useToast();

  const handleCreateToken = async () => {
    setCreating(true);
    try {
      const resp = await createEnrollmentToken(provider);
      setResult({
        token: resp.token,
        installCommand: resp.install_command,
        expiresAt: resp.expires_at,
      });
      setStep(2);
    } catch (err) {
      toast({
        title: "Failed to create token",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setCreating(false);
    }
  };

  const copyToClipboard = (text: string, label: string) => {
    navigator.clipboard.writeText(text);
    setCopied(label);
    setTimeout(() => setCopied(null), 2000);
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4">
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-xl max-w-2xl w-full max-h-[90vh] overflow-y-auto">
        {/* Header */}
        <div className="flex items-center justify-between p-6 border-b border-gray-200 dark:border-gray-700">
          <h2 className="text-lg font-semibold dark:text-white">Enroll Host</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
            ✕
          </button>
        </div>

        <div className="p-6 space-y-6">
          {/* Step indicators */}
          <div className="flex items-center gap-2 text-sm">
            {[1, 2, 3, 4].map((s) => (
              <div key={s} className="flex items-center gap-2">
                <div
                  className={`w-7 h-7 rounded-full flex items-center justify-center text-xs font-medium ${
                    s < step
                      ? "bg-green-500 text-white"
                      : s === step
                        ? "bg-brand-600 text-white"
                        : "bg-gray-200 dark:bg-gray-700 text-gray-500"
                  }`}
                >
                  {s < step ? "✓" : s}
                </div>
                {s < 4 && <div className="w-8 h-px bg-gray-300 dark:bg-gray-600" />}
              </div>
            ))}
          </div>

          {/* Step 1: Choose provider */}
          {step === 1 && (
            <div className="space-y-4">
              <h3 className="font-medium dark:text-white">Step 1: Choose Provider</h3>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                Select the infrastructure provider for this host.
              </p>
              <div className="grid grid-cols-3 gap-3">
                {[
                  { id: "ovhcloud", label: "OVHcloud", desc: "Dedicated server" },
                  { id: "hetzner", label: "Hetzner", desc: "Dedicated root" },
                  { id: "customer_owned", label: "Self-owned", desc: "Your server" },
                ].map((p) => (
                  <button
                    key={p.id}
                    onClick={() => setProvider(p.id)}
                    className={`p-4 rounded-lg border text-left transition ${
                      provider === p.id
                        ? "border-brand-500 bg-brand-50 dark:bg-brand-900/20"
                        : "border-gray-200 dark:border-gray-700 hover:border-gray-300"
                    }`}
                  >
                    <div className="font-medium text-sm dark:text-white">{p.label}</div>
                    <div className="text-xs text-gray-500 dark:text-gray-400">{p.desc}</div>
                  </button>
                ))}
              </div>
              <button
                onClick={handleCreateToken}
                disabled={creating}
                className="w-full bg-brand-600 text-white px-4 py-2.5 rounded-lg text-sm font-medium hover:bg-brand-700 disabled:opacity-50"
              >
                {creating ? "Creating token..." : "Generate Enrollment Token"}
              </button>
            </div>
          )}

          {/* Step 2: Prepare server */}
          {step === 2 && result && (
            <div className="space-y-4">
              <h3 className="font-medium dark:text-white">Step 2: Prepare Your Server</h3>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                SSH into your server and ensure these prerequisites are met:
              </p>
              <div className="bg-gray-50 dark:bg-gray-800 rounded-lg p-4 text-sm space-y-2">
                <div className="flex items-center gap-2">
                  <span className="text-gray-400">•</span>
                  <span className="dark:text-gray-300">Ubuntu 22.04 or 24.04 LTS</span>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-gray-400">•</span>
                  <span className="dark:text-gray-300">
                    KVM enabled (<code className="bg-gray-200 dark:bg-gray-700 px-1 rounded">kvm-ok</code> passes)
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-gray-400">•</span>
                  <span className="dark:text-gray-300">Public IPv4 address</span>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-gray-400">•</span>
                  <span className="dark:text-gray-300">Port 9090 accessible from internet</span>
                </div>
              </div>
              <button
                onClick={() => setStep(3)}
                className="w-full bg-brand-600 text-white px-4 py-2.5 rounded-lg text-sm font-medium hover:bg-brand-700"
              >
                Server is Ready
              </button>
            </div>
          )}

          {/* Step 3: Run install */}
          {step === 3 && result && (
            <div className="space-y-4">
              <h3 className="font-medium dark:text-white">Step 3: Run Install Script</h3>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                Copy and run this command on your server as root:
              </p>
              <div className="relative">
                <pre className="bg-gray-900 text-green-400 rounded-lg p-4 text-xs overflow-x-auto font-mono">
                  {result.installCommand}
                </pre>
                <button
                  onClick={() => copyToClipboard(result.installCommand, "install")}
                  className="absolute top-2 right-2 bg-gray-700 text-gray-300 px-2 py-1 rounded text-xs hover:bg-gray-600"
                >
                  {copied === "install" ? "Copied!" : "Copy"}
                </button>
              </div>
              <div className="bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 rounded-lg p-3 text-sm">
                <p className="font-medium text-yellow-800 dark:text-yellow-200">Token expires</p>
                <p className="text-yellow-600 dark:text-yellow-400 text-xs mt-1">
                  {new Date(result.expiresAt).toLocaleString()}
                </p>
              </div>
              <details className="text-sm">
                <summary className="cursor-pointer text-gray-500 dark:text-gray-400 hover:text-gray-700">
                  Show raw token
                </summary>
                <div className="mt-2 relative">
                  <pre className="bg-gray-100 dark:bg-gray-800 rounded p-3 text-xs break-all font-mono dark:text-gray-300">
                    {result.token}
                  </pre>
                  <button
                    onClick={() => copyToClipboard(result.token, "token")}
                    className="absolute top-1 right-1 bg-gray-200 dark:bg-gray-700 px-2 py-0.5 rounded text-xs"
                  >
                    {copied === "token" ? "Copied!" : "Copy"}
                  </button>
                </div>
              </details>
              <button
                onClick={() => setStep(4)}
                className="w-full bg-brand-600 text-white px-4 py-2.5 rounded-lg text-sm font-medium hover:bg-brand-700"
              >
                I've Run the Script
              </button>
            </div>
          )}

          {/* Step 4: Verify */}
          {step === 4 && (
            <div className="space-y-4">
              <h3 className="font-medium dark:text-white">Step 4: Verify Enrollment</h3>
              <p className="text-sm text-gray-500 dark:text-gray-400">
                The host should appear in the fleet list within 60 seconds. Check agent logs on the server:
              </p>
              <div className="relative">
                <pre className="bg-gray-900 text-green-400 rounded-lg p-4 text-xs font-mono">
                  sudo journalctl -u ocm-agent -f
                </pre>
                <button
                  onClick={() => copyToClipboard("sudo journalctl -u ocm-agent -f", "logs")}
                  className="absolute top-2 right-2 bg-gray-700 text-gray-300 px-2 py-1 rounded text-xs hover:bg-gray-600"
                >
                  {copied === "logs" ? "Copied!" : "Copy"}
                </button>
              </div>
              <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-lg p-3 text-sm">
                <p className="font-medium text-blue-800 dark:text-blue-200">Need to debug?</p>
                <p className="text-blue-600 dark:text-blue-400 text-xs mt-1">
                  SSH in and install Claude Code: <code>curl -sL https://cli.claude.ai/install.sh | bash</code>
                </p>
              </div>
              <button
                onClick={() => {
                  onComplete();
                  onClose();
                }}
                className="w-full bg-green-600 text-white px-4 py-2.5 rounded-lg text-sm font-medium hover:bg-green-700"
              >
                Done — Refresh Host List
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
