import { Link, Outlet, useLocation, Navigate } from "react-router-dom";
import { Activity, LayoutDashboard, Settings, Globe, PanelsTopLeft } from "lucide-react";
import { useAuth } from "../lib/auth";
import { UserMenu } from "./UserMenu";

const navItems = [
  { label: "Dashboard", path: "/dashboard", Icon: LayoutDashboard },
  { label: "Observability", path: "/observability", Icon: Activity },
  { label: "Workspaces", path: "/workspaces", Icon: PanelsTopLeft },
  { label: "Browser VMs", path: "/browser-vms", Icon: Globe },
  { label: "Settings", path: "/settings", Icon: Settings },
];

function isNavActive(path: string, pathname: string): boolean {
  if (path === "/dashboard") {
    return pathname === "/dashboard" || (pathname.startsWith("/dashboard/") && !pathname.startsWith("/dashboard/admin"));
  }
  return pathname.startsWith(path);
}

export function AppShell() {
  const { user, account, loading, accountError, isAdmin } = useAuth();
  const location = useLocation();

  if (!loading && user && !account && !accountError) {
    return <Navigate to="/welcome" replace />;
  }

  // Admin navbar: Dashboard, Admin, then the rest of the regular nav
  // (Browser VMs + Settings). Previously this appended only
  // navItems[1], so adding Browser VMs silently dropped Settings from
  // the admin nav.
  const allNavItems = isAdmin
    ? [navItems[0], { label: "Admin", path: "/dashboard/admin", Icon: Settings }, ...navItems.slice(1)]
    : navItems;

  return (
    <div className="min-h-screen flex flex-col">
      {/* Desktop top navbar */}
      <header className="hidden md:block bg-[#2a2a2a] text-white sticky top-0 z-50">
        <div className="flex items-center justify-between px-4 md:px-6 py-3">
          <div className="flex items-center gap-3 md:gap-6">
            <Link to="/dashboard" className="flex items-center gap-2 hover:opacity-80 transition-opacity">
              <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-orange-500 to-red-600 flex items-center justify-center flex-shrink-0">
                <img src="/branding/mascots.svg" alt="" className="h-5 w-auto" />
              </div>
              <span className="text-orange-400 font-semibold">OpenClaw</span>
              <span className="text-emerald-400 ml-1 font-semibold">Machines</span>
            </Link>
            <nav className="flex items-center gap-4 text-sm">
              {allNavItems.map(({ label, path, Icon }) => {
                const active = isNavActive(path, location.pathname);
                return (
                  <Link
                    key={path}
                    to={path}
                    className={`flex items-center gap-2 transition-colors ${
                      active
                        ? "text-orange-400"
                        : "text-gray-400 hover:text-white"
                    }`}
                  >
                    <Icon className="w-4 h-4" />
                    {label}
                  </Link>
                );
              })}
            </nav>
          </div>
          <div className="flex items-center gap-2 md:gap-3">
            <UserMenu />
          </div>
        </div>
      </header>

      {/* Mobile top header */}
      <header className="flex md:hidden sticky top-0 z-50 bg-[#2a2a2a] text-white px-4 py-3 items-center justify-between">
        <Link to="/dashboard" className="flex items-center gap-2">
          <div className="w-7 h-7 rounded-lg bg-gradient-to-br from-orange-500 to-red-600 flex items-center justify-center flex-shrink-0">
            <img src="/branding/mascots.svg" alt="" className="h-4 w-auto" />
          </div>
          <span className="text-orange-400 font-semibold text-sm">OpenClaw</span>
          <span className="text-emerald-400 ml-0.5 font-semibold text-sm">Machines</span>
        </Link>
        <UserMenu />
      </header>

      {/* Main content */}
      <main className="flex-1">
        <div className="max-w-6xl w-full mx-auto px-6 py-7 pb-24 md:pb-7">
          <Outlet />
        </div>
      </main>

      {/* Mobile bottom tab bar */}
      <nav
        className="flex md:hidden fixed bottom-0 left-0 right-0 z-50 h-14
          bg-[#2a2a2a] border-t border-[rgba(255,255,255,0.08)]"
      >
        {navItems.map(({ label, path, Icon }) => {
          const active = isNavActive(path, location.pathname);
          return (
            <Link
              key={path}
              to={path}
              className={`flex flex-1 flex-col items-center justify-center gap-0.5 text-[10px] transition-colors ${
                active ? "text-orange-400" : "text-gray-500"
              }`}
            >
              <Icon className="w-5 h-5" />
              {label}
            </Link>
          );
        })}
      </nav>
    </div>
  );
}
