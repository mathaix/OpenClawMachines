import { useEffect, useState, useCallback } from "react";
import { listFiles, dataPlaneUrl } from "../lib/api";
import type { FileEntry } from "../lib/types";

interface FileBrowserProps {
  accountId: number;
  machineId: string;
  accountSlug?: string;
  machineSlug?: string;
}

function formatSize(bytes: number): string {
  if (bytes === 0) return "-";
  const units = ["B", "KB", "MB", "GB"];
  let i = 0;
  let size = bytes;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function pathSegments(path: string): { name: string; path: string }[] {
  const parts = path.split("/").filter(Boolean);
  const segments: { name: string; path: string }[] = [{ name: "~", path: "/" }];
  let current = "";
  for (const part of parts) {
    current += "/" + part;
    segments.push({ name: part, path: current });
  }
  return segments;
}

export function FileBrowser({ accountId, machineId, accountSlug, machineSlug }: FileBrowserProps) {
  const [currentPath, setCurrentPath] = useState("/");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchFiles = useCallback(
    async (path: string) => {
      setLoading(true);
      setError(null);
      try {
        const files = await listFiles(accountId, machineId, path);
        // Sort: directories first, then alphabetically
        files.sort((a, b) => {
          if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
          return a.name.localeCompare(b.name);
        });
        setEntries(files);
        setCurrentPath(path);
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to list files");
      } finally {
        setLoading(false);
      }
    },
    [accountId, machineId],
  );

  useEffect(() => {
    fetchFiles("/");
  }, [fetchFiles]);

  const navigateTo = (path: string) => {
    fetchFiles(path);
  };

  const breadcrumbs = pathSegments(currentPath);

  return (
    <div className="space-y-3">
      {/* Breadcrumbs + Download ZIP */}
      <div className="flex items-center justify-between">
        <nav className="flex items-center gap-1 text-sm text-gray-600 overflow-x-auto">
          {breadcrumbs.map((seg, i) => (
            <span key={seg.path} className="flex items-center gap-1 whitespace-nowrap">
              {i > 0 && <span className="text-gray-400">/</span>}
              <button
                onClick={() => navigateTo(seg.path)}
                className={`hover:text-brand-600 ${
                  i === breadcrumbs.length - 1 ? "font-medium text-gray-900" : ""
                }`}
              >
                {seg.name}
              </button>
            </span>
          ))}
        </nav>
        <a
          href={dataPlaneUrl(accountSlug, machineSlug, `files/download-zip?path=${encodeURIComponent(currentPath)}`, accountId, machineId)}
          className="text-xs font-medium text-brand-600 hover:text-brand-700 whitespace-nowrap ml-4"
        >
          Download ZIP
        </a>
      </div>

      {/* Error */}
      {error && (
        <div className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg p-3">
          {error}
        </div>
      )}

      {/* File list */}
      {loading ? (
        <p className="text-sm text-gray-500 py-4">Loading files...</p>
      ) : entries.length === 0 ? (
        <p className="text-sm text-gray-500 py-4">This directory is empty.</p>
      ) : (
        <div className="border border-gray-200 rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-gray-50 text-left text-xs text-gray-500 uppercase tracking-wide">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium w-24">Size</th>
                <th className="px-4 py-2 font-medium w-40">Modified</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {entries.map((entry) => (
                <tr key={entry.path} className="hover:bg-gray-50">
                  <td className="px-4 py-2">
                    {entry.is_dir ? (
                      <button
                        onClick={() => navigateTo(entry.path)}
                        className="flex items-center gap-2 text-brand-600 hover:text-brand-700 font-medium"
                      >
                        <svg className="w-4 h-4 text-yellow-500" fill="currentColor" viewBox="0 0 20 20">
                          <path d="M2 6a2 2 0 012-2h5l2 2h5a2 2 0 012 2v6a2 2 0 01-2 2H4a2 2 0 01-2-2V6z" />
                        </svg>
                        {entry.name}
                      </button>
                    ) : (
                      <a
                        href={dataPlaneUrl(accountSlug, machineSlug, `files/download?path=${encodeURIComponent(entry.path)}`, accountId, machineId)}
                        className="flex items-center gap-2 text-gray-900 hover:text-brand-600"
                      >
                        <svg className="w-4 h-4 text-gray-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M7 21h10a2 2 0 002-2V9.414a1 1 0 00-.293-.707l-5.414-5.414A1 1 0 0012.586 3H7a2 2 0 00-2 2v14a2 2 0 002 2z" />
                        </svg>
                        {entry.name}
                      </a>
                    )}
                  </td>
                  <td className="px-4 py-2 text-gray-500 tabular-nums">
                    {entry.is_dir ? "-" : formatSize(entry.size)}
                  </td>
                  <td className="px-4 py-2 text-gray-500">
                    {entry.modified
                      ? new Date(entry.modified).toLocaleDateString(undefined, {
                          month: "short",
                          day: "numeric",
                          hour: "2-digit",
                          minute: "2-digit",
                        })
                      : "-"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
