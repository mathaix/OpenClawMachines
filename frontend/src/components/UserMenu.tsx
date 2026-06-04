import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { Sun, Moon, Monitor, LogOut, Users } from "lucide-react";
import { useAuth } from "../lib/auth";
import { useTheme } from "../lib/theme";

const themeOptions = [
  { value: "light" as const, label: "Light", Icon: Sun },
  { value: "dark" as const, label: "Dark", Icon: Moon },
  { value: "system" as const, label: "System", Icon: Monitor },
];

export function UserMenu() {
  const { user, account, accounts, setAccount, logout } = useAuth();
  const { theme, setTheme } = useTheme();

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button className="text-xs md:text-sm font-medium px-3 md:px-4 py-2 rounded-lg bg-white text-gray-900 hover:bg-gray-100 transition-all duration-150 outline-none truncate max-w-[120px] md:max-w-none">
          {user?.email}
        </button>
      </DropdownMenu.Trigger>

      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={6}
          className="min-w-[200px] bg-card border border-border rounded-[var(--radius)] p-1 shadow-elevated z-[200] animate-fade-up"
        >
          {/* Account info */}
          <div className="px-3 py-2 border-b border-border-subtle mb-1">
            <div className="text-[12px] font-medium text-text-primary">{account?.name}</div>
            <div className="text-[11px] text-text-muted">{user?.email}</div>
          </div>

          {/* Account switcher - only if multiple accounts available */}
          {accounts.length > 1 && (
            <>
              <DropdownMenu.Label className="px-3 py-1 text-[10px] font-medium text-text-muted uppercase tracking-wider">
                Switch Account
              </DropdownMenu.Label>
              {accounts.filter(a => a.id !== account?.id).map(a => (
                <DropdownMenu.Item
                  key={a.id}
                  onClick={() => setAccount(a)}
                  className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-text-secondary rounded-[var(--radius-sm)] cursor-pointer hover:bg-[rgba(255,255,255,0.03)] hover:text-text-primary outline-none"
                >
                  <Users className="w-3.5 h-3.5 opacity-50" />
                  {a.name}
                </DropdownMenu.Item>
              ))}
              <DropdownMenu.Separator className="h-px bg-border-subtle my-1" />
            </>
          )}

          {/* Theme */}
          <DropdownMenu.Label className="px-3 py-1 text-[10px] font-medium text-text-muted uppercase tracking-wider">
            Theme
          </DropdownMenu.Label>
          {themeOptions.map(({ value, label, Icon }) => (
            <DropdownMenu.Item
              key={value}
              onClick={() => setTheme(value)}
              className={`flex items-center gap-2 px-3 py-1.5 text-[12px] rounded-[var(--radius-sm)] cursor-pointer outline-none ${
                theme === value
                  ? "text-brand-500 bg-brand-glow"
                  : "text-text-secondary hover:bg-[rgba(255,255,255,0.03)] hover:text-text-primary"
              }`}
            >
              <Icon className="w-3.5 h-3.5" />
              {label}
            </DropdownMenu.Item>
          ))}

          <DropdownMenu.Separator className="h-px bg-border-subtle my-1" />

          {/* Logout */}
          <DropdownMenu.Item
            onClick={logout}
            className="flex items-center gap-2 px-3 py-2 text-[12px] font-medium text-red-400 rounded-[var(--radius-sm)] cursor-pointer hover:bg-red-500/10 hover:text-red-300 outline-none"
          >
            <LogOut className="w-4 h-4" />
            Sign out
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
