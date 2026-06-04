export function PublicFooter() {
  return (
    <footer
      className="py-6 text-center text-sm"
      style={{
        color: "var(--l-text-muted)",
        borderTop: "1px solid var(--l-border)",
      }}
    >
      <div className="mx-auto" style={{ maxWidth: 940, padding: "0 24px" }}>
        &copy; {new Date().getFullYear()}{" "}
        <span style={{ color: "var(--l-accent)" }}>OpenClaw</span>
        <span style={{ color: "var(--l-teal)" }}>Machines</span>
      </div>
    </footer>
  );
}
