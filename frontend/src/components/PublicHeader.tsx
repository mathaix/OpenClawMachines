import { Link } from "react-router-dom";

const navLinks = [
  { label: "Docs", to: "/docs" },
  { label: "Blog", to: "/blog" },
];

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
          style={{ letterSpacing: "-0.02em", color: "var(--l-text)" }}
        >
          <span style={{ color: "var(--l-accent)" }}>OpenClaw</span>
          <span style={{ color: "var(--l-teal)" }}>Machines</span>
        </Link>

        <div className="flex items-center" style={{ gap: 28 }}>
          {navLinks.map((link) => (
            <Link
              key={link.to}
              to={link.to}
              className="hidden sm:block text-sm font-medium transition-colors"
              style={{ color: "var(--l-text-muted)" }}
            >
              {link.label}
            </Link>
          ))}
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
