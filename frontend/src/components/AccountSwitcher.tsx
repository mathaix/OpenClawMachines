import { useState, useRef, useEffect } from "react";
import { useAuth } from "../lib/auth";
import { useNavigate } from "react-router-dom";
import { ChevronDown } from "lucide-react";

export function AccountSwitcher() {
  const { account, accounts, setAccount } = useAuth();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  if (!account || accounts.length === 0) return null;

  return (
    <div className="relative" ref={ref}>
      <button
        onClick={() => setOpen(v => !v)}
        className="flex items-center gap-1.5 text-sm font-medium text-gray-700 dark:text-gray-300 hover:text-gray-900 dark:hover:text-gray-100 px-2 py-1 rounded-md hover:bg-gray-100 dark:hover:bg-surface-elevated transition-colors"
      >
        {account.name}
        <ChevronDown className="h-3.5 w-3.5" />
      </button>
      {open && (
        <div className="absolute left-0 mt-1 w-64 bg-white dark:bg-surface-card border border-gray-200 dark:border-border rounded-lg shadow-lg py-1 z-50">
          {accounts.map(acct => (
            <button
              key={acct.id}
              onClick={() => { setAccount(acct); setOpen(false); }}
              className={`w-full flex items-center justify-between px-3 py-2 text-sm ${
                acct.id === account.id
                  ? "bg-brand-50 dark:bg-brand-600/10"
                  : "hover:bg-gray-50 dark:hover:bg-surface-elevated"
              }`}
            >
              <span className="text-gray-900 dark:text-gray-100 truncate">{acct.name}</span>
              {acct.id === account.id && (
                <span className="h-2 w-2 rounded-full bg-brand-500 flex-shrink-0" />
              )}
            </button>
          ))}
          <div className="border-t border-gray-200 dark:border-border mt-1 pt-1">
            <button
              onClick={() => { navigate("/dashboard/settings"); setOpen(false); }}
              className="w-full text-left px-3 py-2 text-sm text-brand-600 dark:text-brand-400 hover:bg-gray-50 dark:hover:bg-surface-elevated"
            >
              + Create New Account
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
