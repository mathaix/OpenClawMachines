import { createContext, useContext, useEffect, useState, useCallback, type ReactNode } from "react";
import type { User, Account } from "./types";
import { authMe, listAccounts, listPendingInvitations } from "./api";
import { firebaseSignOut } from "./firebase";

const ACTIVE_ACCOUNT_KEY = "ocm_active_account_id";

interface AuthState {
  user: User | null;
  account: Account | null;
  accounts: Account[];
  loading: boolean;
  accountError: boolean;
  isAdmin: boolean;
  pendingInvitationCount: number;
  setAccount: (account: Account) => void;
  refreshAccounts: () => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthState>({
  user: null,
  account: null,
  accounts: [],
  loading: true,
  accountError: false,
  isAdmin: false,
  pendingInvitationCount: 0,
  setAccount: () => {},
  refreshAccounts: async () => {},
  logout: () => {},
});

export function configuredCookieDomains(): string[] {
  const configured = (import.meta.env.VITE_COOKIE_DOMAIN || import.meta.env.VITE_DATA_PLANE_DOMAIN || "")
    .trim()
    .replace(/^\.+|\.+$/g, "");
  if (configured === "localhost" || configured === "127.0.0.1") {
    return [""];
  }
  return configured ? ["", `.${configured}`] : [""];
}

function isConfiguredAdmin(email?: string | null): boolean {
  if (!email) return false;
  const normalized = email.trim().toLowerCase();
  return (import.meta.env.VITE_OCM_ADMIN_EMAILS || "")
    .split(",")
    .map((candidate: string) => candidate.trim().toLowerCase())
    .filter(Boolean)
    .includes(normalized);
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [account, setAccount] = useState<Account | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [loading, setLoading] = useState(true);
  const [accountError, setAccountError] = useState(false);
  const [pendingInvitationCount, setPendingInvitationCount] = useState(0);

  const selectAccount = useCallback((acct: Account) => {
    setAccount(acct);
    localStorage.setItem(ACTIVE_ACCOUNT_KEY, String(acct.id));
  }, []);

  const fetchAccounts = useCallback(async () => {
    try {
      const accts = await listAccounts() ?? [];
      setAccounts(accts);
      if (accts.length > 0) {
        const savedId = localStorage.getItem(ACTIVE_ACCOUNT_KEY);
        const restored = savedId ? accts.find(a => a.id === Number(savedId)) : null;
        setAccount(restored ?? accts[0]);
      }
      setAccountError(false);
    } catch {
      setAccountError(true);
    }
  }, []);

  const fetchPendingInvitations = useCallback(async () => {
    try {
      const invs = await listPendingInvitations();
      setPendingInvitationCount(invs?.length ?? 0);
    } catch {
      // Non-critical
    }
  }, []);

  const refreshAccounts = useCallback(async () => {
    await Promise.all([fetchAccounts(), fetchPendingInvitations()]);
  }, [fetchAccounts, fetchPendingInvitations]);

  useEffect(() => {
    authMe()
      .then(async (res) => {
        setUser(res.user);
        await refreshAccounts();
      })
      .catch(() => {
        // No valid session — user needs to login
      })
      .finally(() => setLoading(false));
  }, [refreshAccounts]);

  const logout = async () => {
    setUser(null);
    setAccount(null);
    setAccounts([]);
    localStorage.removeItem(ACTIVE_ACCOUNT_KEY);

    // Tell backend to clear ocm_token cookie
    const apiBase = import.meta.env.VITE_API_URL || "/api";
    try {
      await fetch(`${apiBase}/auth/logout`, {
        method: "POST",
        credentials: "include",
      });
    } catch {
      // Best-effort
    }

    // Clear ocm_token cookie client-side as fallback
    for (const domain of configuredCookieDomains()) {
      const domainPart = domain ? `; domain=${domain}` : "";
      document.cookie = `ocm_token=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
    }

    // Sign out of Firebase
    try {
      await firebaseSignOut();
    } catch {
      // Best-effort
    }

    window.location.href = "/signed-out";
  };

  const isAdmin = isConfiguredAdmin(user?.email);

  return (
    <AuthContext.Provider value={{
      user, account, accounts, loading, accountError, isAdmin,
      pendingInvitationCount, setAccount: selectAccount, refreshAccounts, logout,
    }}>
      {children}
    </AuthContext.Provider>
  );
}

export const useAuth = () => useContext(AuthContext);
