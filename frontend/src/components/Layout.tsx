import { useState, useRef, useEffect } from "react";
import { Link, Outlet, useLocation, Navigate } from "react-router-dom";
import { Sun, Moon, Monitor } from "lucide-react";
import { useAuth } from "../lib/auth";
import { useTheme } from "../lib/theme";
import { AccountSwitcher } from "./AccountSwitcher";

const baseNav = [
  { label: "Dashboard", path: "/dashboard" },
  { label: "Chat", path: "/chat" },
  { label: "Admin", path: "/dashboard/admin", adminOnly: true },
  { label: "Settings", path: "/dashboard/settings" },
];

const themeOptions = [
  { value: "light" as const, label: "Light", Icon: Sun },
  { value: "dark" as const, label: "Dark", Icon: Moon },
  { value: "system" as const, label: "System", Icon: Monitor },
];

export function Layout() {
  const { user, account, loading, accountError, logout, isAdmin, pendingInvitationCount } = useAuth();
  const location = useLocation();

  // Truly new users (no account created yet) go to onboarding
  if (!loading && user && !account && !accountError) {
    return <Navigate to="/welcome" replace />;
  }
  const { theme, setTheme } = useTheme();
  const [themeMenuOpen, setThemeMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!themeMenuOpen) return;
    const handler = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setThemeMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [themeMenuOpen]);

  const ActiveIcon = themeOptions.find((o) => o.value === theme)?.Icon ?? Monitor;

  return (
    <div className="min-h-screen flex flex-col">
      <header className="bg-white dark:bg-surface border-b border-gray-200 dark:border-border px-6 py-3 flex items-center justify-between">
        <div className="flex items-center gap-6">
          <Link to="/dashboard" className="flex items-center gap-2 text-lg font-semibold text-gray-900 dark:text-gray-100">
            <img src="/branding/mascots.svg" alt="OpenClaw Machines" className="h-7 w-auto" />
            <span className="text-red-500">OpenClaw</span><span className="text-teal-400">Machines</span>
          </Link>
          <AccountSwitcher />
          <nav className="flex gap-1">
            {baseNav
              .filter((item) => !item.adminOnly || isAdmin)
              .map((item) => (
              <Link
                key={item.path}
                to={item.path}
                className={`text-sm px-3 py-1.5 rounded-lg transition-colors ${
                  location.pathname === item.path
                    ? "text-brand-600 font-medium bg-brand-50 dark:bg-surface-elevated"
                    : "text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-200 hover:bg-gray-100 dark:hover:bg-surface-elevated"
                }`}
              >
                {item.label}
              </Link>
            ))}
          </nav>
        </div>
        <div className="flex items-center gap-4">
          {/* Theme toggle */}
          <div className="relative" ref={menuRef}>
            <button
              onClick={() => setThemeMenuOpen((v) => !v)}
              className="p-1.5 rounded-md text-gray-500 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-surface-elevated transition-colors"
              aria-label="Toggle theme"
            >
              <ActiveIcon className="h-4 w-4" />
            </button>
            {themeMenuOpen && (
              <div className="absolute right-0 mt-1 w-36 bg-white dark:bg-surface-card border border-gray-200 dark:border-border rounded-lg shadow-lg py-1 z-50">
                {themeOptions.map(({ value, label, Icon }) => (
                  <button
                    key={value}
                    onClick={() => {
                      setTheme(value);
                      setThemeMenuOpen(false);
                    }}
                    className={`w-full flex items-center gap-2 px-3 py-1.5 text-sm ${
                      theme === value
                        ? "text-brand-600 bg-brand-50 dark:bg-brand-600/10"
                        : "text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-surface-elevated"
                    }`}
                  >
                    <Icon className="h-4 w-4" />
                    {label}
                  </button>
                ))}
              </div>
            )}
          </div>
          <span className="text-sm text-gray-600 dark:text-gray-400">{user?.email}</span>
          <button
            onClick={logout}
            className="text-sm text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200"
          >
            Sign out
          </button>
        </div>
      </header>
      {pendingInvitationCount > 0 && (
        <div className="bg-brand-50 dark:bg-brand-600/10 border-b border-brand-200 dark:border-brand-600/20 px-6 py-2 text-sm text-gray-700 dark:text-gray-300">
          You have {pendingInvitationCount} pending invitation{pendingInvitationCount > 1 ? "s" : ""}.{" "}
          <Link to="/dashboard/settings" className="text-brand-600 dark:text-brand-400 font-medium hover:underline">View</Link>
        </div>
      )}
      <main className="flex-1 p-6">
        <div className="max-w-6xl mx-auto">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
