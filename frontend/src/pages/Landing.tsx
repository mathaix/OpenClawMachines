import { useEffect, useState } from "react";
import { Helmet } from "react-helmet-async";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { joinWaitlist } from "../lib/api";
import { WaitlistSurvey } from "../components/WaitlistSurvey";
import { PublicHeader } from "../components/PublicHeader";
import { PublicFooter } from "../components/PublicFooter";
import { Check, ArrowRight, Mail } from "lucide-react";

/* ── Inline style helpers ───────────────────────────────── */

const container: React.CSSProperties = { maxWidth: 940, margin: "0 auto", padding: "0 24px" };
const sectionStyle: React.CSSProperties = { padding: "80px 0", borderTop: "1px solid var(--l-border)" };
const shH2: React.CSSProperties = { fontSize: "2rem", fontWeight: 700, letterSpacing: "-0.02em", color: "var(--l-text)" };
const shP: React.CSSProperties = { color: "var(--l-text-muted)", fontSize: "1.05rem", marginTop: 10, maxWidth: 560 };
const cardStyle: React.CSSProperties = {
  border: "1px solid var(--l-border)",
  borderRadius: 14,
  padding: 28,
  background: "var(--l-card-bg)",
};
const labelStyle: React.CSSProperties = {
  fontSize: 12, fontWeight: 600, textTransform: "uppercase",
  letterSpacing: "0.06em", color: "var(--l-accent)", marginBottom: 10,
};

export function Landing() {
  const { user, loading } = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [showSurvey, setShowSurvey] = useState(false);

  useEffect(() => {
    if (!loading && user) {
      navigate("/dashboard", { replace: true });
    }
  }, [loading, user, navigate]);

  const handleReserve = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email) return;
    try {
      await joinWaitlist(email, "landing");
    } catch {
      // still show success — email is captured or already exists
    }
    setSubmitted(true);
    setShowSurvey(true);
  };

  return (
    <div className="landing" style={{ background: "var(--l-bg)", color: "var(--l-text)", lineHeight: 1.7 }}>
      <Helmet>
        <title>OpenClaw Machines — Instantly Provision Secure Sandboxed AI Agents</title>
        <meta name="description" content="Instantly provision secure sandboxed OpenClaw agents. Each agent runs in an isolated Firecracker MicroVM with full desktop access through Cloudflare tunnels." />
        <link rel="canonical" href="https://openclawmachines.com/" />
        <meta property="og:title" content="OpenClaw Machines — Instantly Provision Secure Sandboxed AI Agents" />
        <meta property="og:description" content="Instantly provision secure sandboxed OpenClaw agents. Each agent runs in an isolated Firecracker MicroVM with full desktop access through Cloudflare tunnels." />
        <meta property="og:url" content="https://openclawmachines.com/" />
      </Helmet>

      <PublicHeader />

      {/* ── Hero ──────────────────────────────────────────── */}
      <section style={{ padding: "100px 0 80px" }}>
        <div style={container}>
          <h1
            style={{
              fontSize: "clamp(2.4rem, 5vw, 3.6rem)",
              fontWeight: 700,
              letterSpacing: "-0.03em",
              lineHeight: 1.15,
              maxWidth: 680,
              color: "var(--l-text)",
            }}
          >
            The easiest way to run{" "}
            <span style={{ color: "var(--l-accent)" }}>OpenClaw</span>
          </h1>
          <p style={{ fontSize: "1.2rem", color: "var(--l-text-muted)", marginTop: 20, maxWidth: 540, lineHeight: 1.6 }}>
            One click. Thirty seconds. Your AI agent is live in an isolated VM
            with zero-trust security, automatic backups, and nothing to maintain.
          </p>

          <div id="reserve" style={{ marginTop: 36 }}>
            {submitted ? (
              <div
                className="inline-flex items-center gap-2 text-sm font-medium"
                style={{
                  padding: "10px 22px",
                  borderRadius: 10,
                  background: "rgba(46,168,160,0.1)",
                  border: "1px solid rgba(46,168,160,0.3)",
                  color: "var(--l-teal)",
                }}
              >
                <Check className="w-5 h-5" />
                Thanks! We'll be in touch.
              </div>
            ) : (
              <form onSubmit={handleReserve} className="flex" style={{ gap: 10, maxWidth: 420 }}>
                <div className="relative flex-1">
                  <Mail
                    className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5"
                    style={{ color: "var(--l-text-muted)", opacity: 0.5 }}
                  />
                  <input
                    type="email"
                    required
                    placeholder="your@email.com"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    style={{
                      width: "100%",
                      padding: "12px 16px 12px 44px",
                      borderRadius: 10,
                      fontSize: 14,
                      border: "1.5px solid var(--l-border)",
                      background: "var(--l-card-bg)",
                      color: "var(--l-text)",
                      outline: "none",
                    }}
                  />
                </div>
                <button
                  type="submit"
                  className="inline-flex items-center gap-2 font-semibold text-sm text-white"
                  style={{ padding: "10px 22px", borderRadius: 10, background: "var(--l-accent)", border: "none", cursor: "pointer" }}
                >
                  Reserve a Spot
                  <ArrowRight className="w-4 h-4" />
                </button>
              </form>
            )}
          </div>

          <p style={{ marginTop: 14, fontSize: 13, color: "var(--l-text-muted)", opacity: 0.7 }}>
            Early access — limited spots available.
          </p>
        </div>
      </section>

      {/* ── Provisioning ──────────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div style={{ marginBottom: 48 }}>
            <h2 style={shH2}>Zero to running agent in 30 seconds</h2>
            <p style={shP}>No servers to rent. No Docker. No config files. Click a button and it's live.</p>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4" style={{ gap: 16 }}>
            <Card label="Deploy" title="One-Click" text="Pick a name, choose a size, hit create. Your agent boots in a dedicated MicroVM in under 30 seconds." />
            <Card label="Access" title="Web Terminal" text="Full shell access in your browser. Persistent sessions that survive disconnects. No SSH keys." />
            <Card label="Control" title="Start & Stop" text="Start when you need it. Stop when you don't. Data, configs, and packages are always preserved." />
            <Card label="Network" title="Custom Domains" text="Dedicated subdomain instantly, or point your own. Automatic TLS, no certificate management." />
          </div>
        </div>
      </section>

      {/* ── Security ──────────────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div style={{ marginBottom: 48 }}>
            <h2 style={shH2}>Security that VPS hosting can't touch</h2>
            <p style={shP}>Every agent runs in its own hardware-isolated VM with three layers of authentication.</p>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4" style={{ gap: 16 }}>
            <Card label="Isolation" title="KVM Hardware" text="Every machine runs in its own Firecracker MicroVM with full KVM virtualization. Not sharing a kernel." />
            <Card label="Edge" title="Zero-Trust Auth" text="Cloudflare Access authenticates every request before it reaches your agent. No open ports. No exposed IPs." />
            <Card label="Secrets" title="Encrypted at Rest" text="API keys encrypted with AES-256-GCM. Injected securely at boot. Never logged. Never exposed." />
            <Card label="Network" title="Zero Public IPs" text="Your agent lives on a private network behind Cloudflare's edge. Nothing to scan, nothing to attack." />
          </div>
        </div>
      </section>

      {/* ── Auth ──────────────────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div style={{ marginBottom: 48 }}>
            <h2 style={shH2}>Sign in once. Access everything.</h2>
            <p style={shP}>No passwords to create or remember. Authenticate with providers you already use.</p>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4" style={{ gap: 16 }}>
            <Card title="Passwordless Email" text="One-time PIN to your inbox. No passwords to store or rotate." />
            <Card title="Google & GitHub" text="One click, no extra signup. Your team is onboarded in seconds." />
            <Card title="Enterprise SSO" text="Okta, Azure AD, OneLogin, or any SAML/OIDC provider. Business plans." />
            <Card title="Three-Layer Auth" text="Edge + per-machine token + gateway validation. No shortcuts. No bypasses." />
          </div>
        </div>
      </section>

      {/* ── Teams ─────────────────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div style={{ marginBottom: 48 }}>
            <h2 style={shH2}>Built for teams</h2>
            <p style={shP}>Everyone gets their own isolated machine. You keep control.</p>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-3" style={{ gap: 16 }}>
            <Card title="Team Accounts" text="Create an organization. Invite your team. Everyone provisions and manages their own agents." />
            <Card title="Self-Service" text="Team members spin up machines in seconds. No tickets. No waiting." />
            <Card title="Budget Controls" text="Set token spend caps per machine. Your team experiments freely. You never get a surprise bill." />
          </div>
        </div>
      </section>

      {/* ── Maintenance Free ──────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div className="grid grid-cols-1 md:grid-cols-2 items-center" style={{ gap: 48 }}>
            <div>
              <h2 style={{ ...shH2, marginBottom: 12 }}>Zero maintenance. Seriously.</h2>
              <p style={{ ...shP, marginBottom: 24 }}>With VPS hosting, you're the sysadmin. With us, you just use your agent.</p>
              <ul style={{ listStyle: "none", padding: 0, margin: 0 }}>
                {[
                  "Automatic backups before every upgrade",
                  "One-click rollback in seconds",
                  "Seamless OpenClaw version upgrades",
                  "Health monitoring with auto-restart",
                  "Persistent storage across restarts",
                ].map((item) => (
                  <li key={item} className="flex items-center" style={{ padding: "4px 0", fontSize: "0.9rem", color: "var(--l-text-muted)", gap: 10 }}>
                    <span style={{ display: "inline-block", width: 6, height: 6, borderRadius: "50%", background: "var(--l-teal)", flexShrink: 0 }} />
                    {item}
                  </li>
                ))}
              </ul>
            </div>

            {/* Terminal mockup */}
            <div style={{ background: "var(--l-terminal-bg)", borderRadius: 14, overflow: "hidden", border: "1px solid rgba(255,255,255,0.06)" }}>
              <div className="flex items-center" style={{ padding: "10px 14px", gap: 6, background: "rgba(255,255,255,0.04)" }}>
                <span style={{ width: 10, height: 10, borderRadius: "50%", background: "#e05050", opacity: 0.6 }} />
                <span style={{ width: 10, height: 10, borderRadius: "50%", background: "#e0a030", opacity: 0.6 }} />
                <span style={{ width: 10, height: 10, borderRadius: "50%", background: "#40c060", opacity: 0.6 }} />
              </div>
              <pre
                className="font-mono"
                style={{
                  padding: 20,
                  margin: 0,
                  fontSize: 13,
                  lineHeight: 1.8,
                  background: "transparent",
                  color: "#9ca3af",
                  overflow: "auto",
                }}
              >
                <span style={{ textDecoration: "line-through", opacity: 0.4 }}>$ ssh root@my-vps.com</span>{"\n"}
                <span style={{ textDecoration: "line-through", opacity: 0.4 }}>$ apt update && apt upgrade -y</span>{"\n"}
                <span style={{ textDecoration: "line-through", opacity: 0.4 }}>$ docker pull openclaw/openclaw:latest</span>{"\n"}
                <span style={{ textDecoration: "line-through", opacity: 0.4 }}>$ docker-compose down && up -d</span>{"\n"}
                <span style={{ textDecoration: "line-through", opacity: 0.4 }}>$ certbot renew --nginx</span>{"\n"}
                <span style={{ textDecoration: "line-through", opacity: 0.4 }}>$ pg_dump openclaw {">"} backup.sql</span>{"\n"}
                {"\n"}
                <span style={{ color: "var(--l-accent)" }}>$ # With OpenClawMachines:</span>{"\n"}
                <span style={{ color: "var(--l-teal)" }}>$ # Click "Create Machine". Done.</span>
              </pre>
            </div>
          </div>
        </div>
      </section>

      {/* ── Comparison Table ──────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div style={{ marginBottom: 48 }}>
            <h2 style={shH2}>Stop babysitting your VPS</h2>
          </div>
          <div style={{ overflowX: "auto" }}>
            <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 14 }}>
              <thead>
                <tr>
                  <th style={thStyle} />
                  <th style={{ ...thStyle, color: "var(--l-accent)" }}>OpenClawMachines</th>
                  <th style={thStyle}>VPS Hosting</th>
                  <th style={thStyle}>MoltWorker</th>
                </tr>
              </thead>
              <tbody>
                <CRow f="Time to deploy" ocm="30 seconds" vps="30-60 minutes" mw="5-10 minutes" />
                <CRow f="Setup required" ocm="None" vps="SSH, Docker, configs" mw="CF account + config" />
                <CRow f="Isolation" ocm="Hardware VM (KVM)" vps="Shared kernel" mw="CF sandbox" />
                <CRow f="Security" ocm="Zero-trust, 3-layer" vps="You configure it" mw="CF Workers" />
                <CRow f="Backups" ocm="Automatic" vps="$6/mo extra" mw="R2 sync" />
                <CRow f="Upgrades" ocm="One-click + rollback" vps="Manual SSH" mw="Redeploy" />
                <CRow f="Web terminal" ocm="Built in" vps="No" mw="No" />
                <CRow f="Team support" ocm="Multi-user accounts" vps="Share SSH keys" mw="Single user" />
                <CRow f="LLM budgets" ocm="Per-machine caps" vps="No" mw="No" />
                <CRow f="SSO" ocm="Google, GitHub, SAML" vps="You build it" mw="CF only" />
              </tbody>
            </table>
          </div>
        </div>
      </section>

      {/* ── Coming Soon ───────────────────────────────────── */}
      <section style={sectionStyle}>
        <div style={container}>
          <div style={{ marginBottom: 48 }}>
            <h2 style={shH2}>Coming soon</h2>
            <p style={shP}>The platform your agents deserve. Here's what's next.</p>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-3" style={{ gap: 16 }}>
            <Card label="Billing" title="Shared Token Quotas" text="One bill for your entire team. Per-agent and per-member limits across Anthropic, OpenAI, and Google." />
            <Card label="Infrastructure" title="Shared Services" text="Shared browsing pools, Postgres databases, and vector memory. Agents collaborate on shared infra." />
            <Card label="Scale" title="Fleet Management" text="Manage dozens of agents from one dashboard. Monitor status, usage, and health across your fleet." />
          </div>
        </div>
      </section>

      {/* ── Final CTA ─────────────────────────────────────── */}
      <section style={{ ...sectionStyle, textAlign: "center" }}>
        <div style={container}>
          <h2 style={{ ...shH2, marginBottom: 12 }}>Your agent deserves better than a bare VPS</h2>
          <p style={{ color: "var(--l-text-muted)", fontSize: "1.05rem", marginBottom: 28 }}>
            Be the first to launch a fully managed, hardware-isolated OpenClaw instance.
          </p>
          {submitted ? (
            <div
              className="inline-flex items-center gap-2 text-sm font-medium mx-auto"
              style={{
                padding: "10px 22px",
                borderRadius: 10,
                background: "rgba(46,168,160,0.1)",
                border: "1px solid rgba(46,168,160,0.3)",
                color: "var(--l-teal)",
              }}
            >
              <Check className="w-5 h-5" />
              You're on the list!
            </div>
          ) : (
            <form
              onSubmit={handleReserve}
              className="flex mx-auto"
              style={{ gap: 10, maxWidth: 420, justifyContent: "center" }}
            >
              <input
                type="email"
                required
                placeholder="your@email.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                style={{
                  flex: 1,
                  padding: "12px 16px",
                  borderRadius: 10,
                  fontSize: 14,
                  border: "1.5px solid var(--l-border)",
                  background: "var(--l-card-bg)",
                  color: "var(--l-text)",
                  outline: "none",
                }}
              />
              <button
                type="submit"
                className="inline-flex items-center gap-2 font-semibold text-sm text-white"
                style={{ padding: "10px 22px", borderRadius: 10, background: "var(--l-accent)", border: "none", cursor: "pointer" }}
              >
                Reserve a Spot
              </button>
            </form>
          )}
          <p style={{ marginTop: 14, fontSize: 13, color: "var(--l-text-muted)", opacity: 0.7 }}>
            Early access — no credit card required.
          </p>
        </div>
      </section>

      <PublicFooter />

      <WaitlistSurvey email={email} open={showSurvey} onClose={() => setShowSurvey(false)} />
    </div>
  );
}

/* ── Reusable Components ──────────────────────────────── */

function Card({ label, title, text }: { label?: string; title: string; text: string }) {
  return (
    <div style={cardStyle}>
      {label && <div style={labelStyle}>{label}</div>}
      <h3 style={{ fontSize: "1.1rem", fontWeight: 600, marginBottom: 8, color: "var(--l-text)" }}>{title}</h3>
      <p style={{ color: "var(--l-text-muted)", fontSize: "0.95rem", margin: 0 }}>{text}</p>
    </div>
  );
}

const thStyle: React.CSSProperties = {
  padding: "14px 16px",
  textAlign: "left",
  borderBottom: "1px solid var(--l-border)",
  fontWeight: 600,
  fontSize: 13,
  color: "var(--l-text-muted)",
  textTransform: "uppercase",
  letterSpacing: "0.04em",
};

const tdStyle: React.CSSProperties = {
  padding: "14px 16px",
  textAlign: "left",
  borderBottom: "1px solid var(--l-border)",
};

function CRow({ f, ocm, vps, mw }: { f: string; ocm: string; vps: string; mw: string }) {
  return (
    <tr>
      <td style={{ ...tdStyle, color: "var(--l-text-muted)", fontWeight: 500 }}>{f}</td>
      <td style={{ ...tdStyle, color: "var(--l-accent)", fontWeight: 600 }}>{ocm}</td>
      <td style={{ ...tdStyle, color: "var(--l-text-muted)" }}>{vps}</td>
      <td style={{ ...tdStyle, color: "var(--l-text-muted)" }}>{mw}</td>
    </tr>
  );
}
