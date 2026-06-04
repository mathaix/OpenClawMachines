import { useState } from "react";
import { dataPlaneUrl } from "../../lib/api";
import { useAuth } from "../../lib/auth";
import type { Machine } from "../../lib/types";

interface FilesTabProps {
  machine: Machine;
  accountId: number;
}

export function FilesTab({ machine, accountId }: FilesTabProps) {
  const [loading, setLoading] = useState(true);
  const { account } = useAuth();
  const isRunning = machine.status === "running";

  // Route through Cloud Run data plane proxy to avoid CF Access X-Frame-Options
  const filesUrl = isRunning
    ? dataPlaneUrl(account?.slug, machine.slug, "files/", accountId, machine.id)
    : null;

  if (!isRunning) {
    return (
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-5">
          <p className="text-[13px] md:text-[14px] text-text-secondary">Start the machine to browse files.</p>
        </div>
      </div>
    );
  }

  return (
    <div className="bg-card border border-border rounded-[var(--radius)] overflow-hidden shadow-card" style={{ height: "calc(100vh - 260px)", minHeight: "500px" }}>
      {loading && (
        <div className="absolute inset-0 flex items-center justify-center bg-card z-10">
          <div className="flex flex-col items-center gap-3">
            <div className="h-6 w-6 rounded-full border-2 border-brand-500 border-t-transparent animate-spin" />
            <span className="text-[12px] text-text-secondary">Loading file browser...</span>
          </div>
        </div>
      )}
      {filesUrl && (
        <iframe
          src={filesUrl}
          onLoad={() => setLoading(false)}
          className="w-full h-full border-0"
          title="File Browser"
        />
      )}
    </div>
  );
}
