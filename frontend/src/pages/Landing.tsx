import { useEffect } from "react";
import { Helmet } from "react-helmet-async";
import { Link, useNavigate } from "react-router-dom";
import { ArrowRight, Github, Server, ShieldCheck, Terminal } from "lucide-react";
import { PublicFooter } from "../components/PublicFooter";
import { PublicHeader } from "../components/PublicHeader";
import { useAuth } from "../lib/auth";

const repoURL = "https://github.com/mathaix/OpenClawMachines";
const cliURL = "https://github.com/mathaix/ocm-cli";

const container: React.CSSProperties = {
  maxWidth: 960,
  margin: "0 auto",
  padding: "0 24px",
};

const sectionStyle: React.CSSProperties = {
  padding: "56px 0",
  borderTop: "1px solid var(--l-border)",
};

export function Landing() {
  const { user, loading } = useAuth();
  const navigate = useNavigate();

  useEffect(() => {
    if (!loading && user) {
      navigate("/dashboard", { replace: true });
    }
  }, [loading, user, navigate]);

  return (
    <div className="landing" style={{ background: "var(--l-bg)", color: "var(--l-text)", minHeight: "100vh" }}>
      <Helmet>
        <title>OpenClaw Machines - Apache Firecracker control plane</title>
        <meta
          name="description"
          content="Apache-2.0 public core for running OpenClaw agents in isolated Firecracker microVMs on hardware you control."
        />
      </Helmet>

      <PublicHeader />

      <main>
        <section style={{ padding: "88px 0 64px" }}>
          <div style={container}>
            <p style={{ color: "var(--l-accent)", fontSize: 13, fontWeight: 700, letterSpacing: 0, marginBottom: 14 }}>
              Apache-2.0 public core
            </p>
            <h1
              style={{
                color: "var(--l-text)",
                fontSize: "3.25rem",
                fontWeight: 750,
                letterSpacing: 0,
                lineHeight: 1.08,
                maxWidth: 760,
              }}
            >
              Run OpenClaw agents in isolated Firecracker microVMs.
            </h1>
            <p
              style={{
                color: "var(--l-text-muted)",
                fontSize: "1.08rem",
                lineHeight: 1.65,
                marginTop: 22,
                maxWidth: 660,
              }}
            >
              OpenClaw Machines provides the minimum control plane, host enrollment, worker agent,
              and runtime pieces needed to run secure KVM-backed agent sandboxes locally or on
              infrastructure you operate.
            </p>

            <div className="flex flex-wrap items-center" style={{ gap: 12, marginTop: 34 }}>
              <a
                href={repoURL}
                className="inline-flex items-center gap-2 rounded-md text-sm font-semibold text-white"
                style={{ background: "var(--l-accent)", padding: "11px 18px" }}
              >
                <Github className="h-4 w-4" />
                View Repository
              </a>
              <a
                href={cliURL}
                className="inline-flex items-center gap-2 rounded-md text-sm font-semibold"
                style={{ border: "1px solid var(--l-border)", color: "var(--l-text)", padding: "10px 17px" }}
              >
                <Terminal className="h-4 w-4" />
                CLI Project
              </a>
              <Link
                to="/login"
                className="inline-flex items-center gap-2 rounded-md text-sm font-semibold"
                style={{ color: "var(--l-text-muted)", padding: "10px 12px" }}
              >
                Sign in
                <ArrowRight className="h-4 w-4" />
              </Link>
            </div>
          </div>
        </section>

        <section style={sectionStyle}>
          <div style={container}>
            <div className="grid grid-cols-1 md:grid-cols-3" style={{ gap: 16 }}>
              <Card
                icon={<ShieldCheck className="h-5 w-5" />}
                title="Hardware isolation"
                text="Each machine runs in a dedicated Firecracker microVM with its own guest kernel."
              />
              <Card
                icon={<Server className="h-5 w-5" />}
                title="BYO hosts"
                text="Enroll KVM-capable Linux hosts and keep the runtime on infrastructure you control."
              />
              <Card
                icon={<Terminal className="h-5 w-5" />}
                title="Companion CLI"
                text="Use the separate Apache-2.0 ocm CLI project for operator and developer workflows."
              />
            </div>
          </div>
        </section>

        <section style={sectionStyle}>
          <div style={container}>
            <h2 style={{ color: "var(--l-text)", fontSize: "1.65rem", fontWeight: 700, letterSpacing: 0 }}>
              Public-core boundary
            </h2>
            <p style={{ color: "var(--l-text-muted)", lineHeight: 1.65, maxWidth: 720, marginTop: 12 }}>
              This repository is for local, self-hosted, and operator-hosted control-plane use.
              Billing, plan enforcement, commercial hosted admin, launch funnels, and private
              infrastructure runbooks belong outside this public core.
            </p>
          </div>
        </section>
      </main>

      <PublicFooter />
    </div>
  );
}

function Card({ icon, title, text }: { icon: React.ReactNode; title: string; text: string }) {
  return (
    <div
      style={{
        border: "1px solid var(--l-border)",
        borderRadius: 8,
        background: "var(--l-card-bg)",
        padding: 24,
      }}
    >
      <div style={{ color: "var(--l-accent)", marginBottom: 14 }}>{icon}</div>
      <h3 style={{ color: "var(--l-text)", fontSize: "1rem", fontWeight: 700, marginBottom: 8 }}>{title}</h3>
      <p style={{ color: "var(--l-text-muted)", fontSize: "0.94rem", lineHeight: 1.55, margin: 0 }}>{text}</p>
    </div>
  );
}
