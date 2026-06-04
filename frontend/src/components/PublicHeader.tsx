import { Link } from "react-router-dom";

const repoURL = "https://github.com/mathaix/OpenClawMachines";
const cliURL = "https://github.com/mathaix/ocm-cli";

export function PublicHeader() {
  return (
    <nav
      className="sticky top-0 z-50"
      style={{
        background: "color-mix(in srgb, var(--l-bg) 85%, transparent)",
        backdropFilter: "blur(12px)",
        borderBottom: "1px solid var(--l-border)",
      }}
    >
      <div
        className="mx-auto flex h-14 items-center justify-between"
        style={{ maxWidth: 940, padding: "0 24px" }}
      >
        <Link
          to="/"
          className="text-lg font-semibold"
          style={{ letterSpacing: 0, color: "var(--l-text)" }}
        >
          <span style={{ color: "var(--l-accent)" }}>OpenClaw</span>
          <span style={{ color: "var(--l-teal)" }}>Machines</span>
        </Link>

        <div className="flex items-center" style={{ gap: 28 }}>
          <a className="hidden sm:block text-sm font-medium" href={repoURL} style={{ color: "var(--l-text-muted)" }}>
            GitHub
          </a>
          <a className="hidden sm:block text-sm font-medium" href={cliURL} style={{ color: "var(--l-text-muted)" }}>
            CLI
          </a>
          <Link
            to="/login"
            className="inline-flex items-center font-semibold text-sm transition-all"
            style={{
              padding: "8px 18px",
              borderRadius: 10,
              border: "1.5px solid var(--l-border)",
              color: "var(--l-text)",
            }}
          >
            Sign In
          </Link>
        </div>
      </div>
    </nav>
  );
}
