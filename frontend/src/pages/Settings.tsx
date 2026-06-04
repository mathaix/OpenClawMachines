import { useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { updateAccount } from "../lib/api";
import { UsageDashboard } from "../components/UsageDashboard";
import { MembersTab } from "../components/MembersTab";
import { ThemeToggle } from "../components/ThemeToggle";

type SettingsTab = "profile" | "members" | "usage" | "billing";

const TABS: { id: SettingsTab; label: string }[] = [
  { id: "profile", label: "Profile" },
  { id: "members", label: "Members" },
  { id: "usage", label: "Usage" },
  { id: "billing", label: "Billing" },
];

export function Settings() {
  const { tab } = useParams<{ tab?: string }>();
  const navigate = useNavigate();
  const { user, account, refreshAccounts } = useAuth();
  const activeTab: SettingsTab = (TABS.find((t) => t.id === tab) ? tab as SettingsTab : "profile");
  const [accountName, setAccountName] = useState("");
  const [savingName, setSavingName] = useState(false);
  const [nameMessage, setNameMessage] = useState<{ type: "success" | "error"; text: string } | null>(null);

  function handleTabChange(tabId: SettingsTab) {
    navigate(`/settings/${tabId}`, { replace: true });
  }

  return (
    <div className="max-w-3xl">
      <h1 className="text-2xl md:text-3xl font-bold text-text-primary mb-6 md:mb-8">Settings</h1>

      {/* Horizontal underline tab bar */}
      <div className="border-b border-border mb-6 md:mb-8">
        <div className="overflow-x-auto -mx-4 px-4 md:mx-0 md:px-0">
          <nav className="flex gap-0 min-w-max md:min-w-0">
            {TABS.map((t) => (
              <button
                key={t.id}
                onClick={() => handleTabChange(t.id)}
                className={`px-4 py-2.5 text-sm font-medium transition-colors -mb-px whitespace-nowrap ${
                  activeTab === t.id
                    ? "border-b-2 border-brand-500 text-text-primary"
                    : "text-text-secondary hover:text-text-primary"
                }`}
              >
                {t.label}
              </button>
            ))}
          </nav>
        </div>
      </div>

      {/* Tab content */}
      <div>
        {activeTab === "profile" && (
          <div className="space-y-4">
            {/* Account section */}
            <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4 md:p-6 space-y-4">
              <h2 className="text-xs md:text-sm font-medium text-text-tertiary uppercase tracking-wider">Account</h2>
              <div>
                <label htmlFor="account-name" className="block text-[11px] text-text-tertiary uppercase tracking-wider mb-1">
                  Account Name
                </label>
                <div className="flex gap-2">
                  <input
                    id="account-name"
                    type="text"
                    value={accountName || account?.name || ""}
                    onChange={(e) => { setAccountName(e.target.value); setNameMessage(null); }}
                    onFocus={() => { if (!accountName && account) setAccountName(account.name); }}
                    maxLength={100}
                    className="flex-1 bg-input border border-border rounded-[var(--radius-sm)] px-3 py-2 text-[13px] text-text-primary outline-none focus:border-brand-500"
                  />
                  <button
                    onClick={async () => {
                      if (!account || !accountName.trim() || accountName.trim() === account.name) return;
                      setSavingName(true);
                      setNameMessage(null);
                      try {
                        await updateAccount(account.id, { name: accountName.trim() });
                        await refreshAccounts();
                        setNameMessage({ type: "success", text: "Account name updated." });
                        setAccountName("");
                      } catch (err: unknown) {
                        const error = err as { message?: string };
                        setNameMessage({ type: "error", text: error?.message || "Failed to update name." });
                      } finally {
                        setSavingName(false);
                      }
                    }}
                    disabled={savingName || !accountName.trim() || accountName.trim() === account?.name}
                    className="bg-brand-600 text-white text-[12px] font-medium px-3 py-[5px] rounded-[var(--radius-sm)] hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {savingName ? "Saving..." : "Save"}
                  </button>
                </div>
                {nameMessage && (
                  <p className={`mt-1 text-[11px] ${nameMessage.type === "success" ? "text-green-500" : "text-red-500"}`}>
                    {nameMessage.text}
                  </p>
                )}
              </div>
              <div>
                <p className="text-[11px] text-text-tertiary uppercase tracking-wider mb-0.5 field-label">Slug</p>
                <p className="text-[14px] font-medium text-text-primary">{account?.slug}</p>
              </div>
              <div>
                <p className="text-[11px] text-text-tertiary uppercase tracking-wider mb-0.5">Plan</p>
                <p className="text-[14px] font-medium text-text-primary capitalize">{account?.plan}</p>
              </div>
            </div>

            {/* User section */}
            <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4 md:p-6 space-y-4">
              <h2 className="text-xs md:text-sm font-medium text-text-tertiary uppercase tracking-wider">User</h2>
              <div>
                <p className="text-[11px] text-text-tertiary uppercase tracking-wider mb-0.5">Email</p>
                <p className="text-[14px] font-medium text-text-primary">{user?.email}</p>
              </div>
              <div>
                <p className="text-[11px] text-text-tertiary uppercase tracking-wider mb-0.5">Name</p>
                <p className="text-[14px] font-medium text-text-primary">{user?.name}</p>
              </div>
              <div>
                <p className="text-[11px] text-text-tertiary uppercase tracking-wider mb-0.5">Auth Provider</p>
                <p className="text-[14px] font-medium text-text-primary capitalize">{user?.auth_provider}</p>
              </div>
            </div>

            {/* Theme preference */}
            <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4 md:p-6">
              <h2 className="text-xs md:text-sm font-medium text-text-tertiary uppercase tracking-wider mb-4">Appearance</h2>
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-[13px] font-medium text-text-primary">Theme</p>
                  <p className="text-[11px] text-text-tertiary">Switch between light and dark mode</p>
                </div>
                <ThemeToggle />
              </div>
            </div>
          </div>
        )}

        {activeTab === "members" && account && (
          <MembersTab accountId={account.id} />
        )}

        {activeTab === "members" && !account && (
          <p className="text-[13px] text-text-secondary">Loading account...</p>
        )}

        {activeTab === "usage" && account && (
          <UsageDashboard accountId={account.id} />
        )}

        {activeTab === "usage" && !account && (
          <p className="text-[13px] text-text-secondary">Loading account...</p>
        )}

        {activeTab === "billing" && (
          <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card p-4 md:p-6">
            <h2 className="text-xs md:text-sm font-medium text-text-tertiary uppercase tracking-wider mb-3">Billing</h2>
            <p className="text-[14px] text-text-secondary">Billing settings coming soon</p>
          </div>
        )}
      </div>
    </div>
  );
}
