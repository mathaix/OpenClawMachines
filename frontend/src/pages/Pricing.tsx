import { useState } from "react";
import { Helmet } from "react-helmet-async";
import {
  Check,
  ArrowRight,
  Cpu,
  MemoryStick,
  HardDrive,
  Globe,
  Shield,
  Terminal,
  Zap,
  Mail,
} from "lucide-react";
import { joinWaitlist } from "../lib/api";
import { WaitlistSurvey } from "../components/WaitlistSurvey";
import { PublicHeader } from "../components/PublicHeader";
import { PublicFooter } from "../components/PublicFooter";

type Billing = "monthly" | "annual";

export function Pricing() {
  const [billing, setBilling] = useState<Billing>("monthly");
  const [email, setEmail] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [showSurvey, setShowSurvey] = useState(false);

  const handleReserve = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email) return;
    try {
      await joinWaitlist(email, "pricing");
    } catch {
      // still show success
    }
    setSubmitted(true);
    setShowSurvey(true);
  };

  return (
    <div className="min-h-screen bg-gray-950">
      <Helmet>
        <title>Pricing — OpenClaw Machines</title>
        <meta name="description" content="Simple, transparent pricing for OpenClaw Machines. Start free with 1 agent, scale to teams. Pay only for compute time you use." />
        <link rel="canonical" href="https://openclawmachines.com/pricing" />
        <meta property="og:title" content="Pricing — OpenClaw Machines" />
        <meta property="og:description" content="Simple, transparent pricing for OpenClaw Machines. Start free with 1 agent, scale to teams. Pay only for compute time you use." />
        <meta property="og:url" content="https://openclawmachines.com/pricing" />
      </Helmet>
      <PublicHeader />

      {/* ── Hero ────────────────────────────────────────── */}
      <section className="py-16 md:py-24">
        <div className="max-w-4xl mx-auto px-4 text-center">
          <h1 className="text-4xl md:text-5xl font-bold text-white mb-4">
            Simple, Transparent Pricing
          </h1>
          <p className="text-xl text-gray-400 max-w-2xl mx-auto mb-10">
            No surprise bills. No hidden fees. No per-request charges.
            Pick a plan, launch your agent.
          </p>

          {/* Billing toggle */}
          <div className="inline-flex items-center gap-1 rounded-full bg-gray-900 border border-gray-800 p-1">
            <button
              onClick={() => setBilling("monthly")}
              className={`rounded-full px-5 py-2 text-sm font-medium transition-colors ${
                billing === "monthly"
                  ? "bg-orange-500 text-white"
                  : "text-gray-400 hover:text-white"
              }`}
            >
              Monthly
            </button>
            <button
              onClick={() => setBilling("annual")}
              className={`rounded-full px-5 py-2 text-sm font-medium transition-colors ${
                billing === "annual"
                  ? "bg-orange-500 text-white"
                  : "text-gray-400 hover:text-white"
              }`}
            >
              Annual
              <span className="ml-1.5 text-xs text-teal-400 font-semibold">
                Save 33%
              </span>
            </button>
          </div>
        </div>
      </section>

      {/* ── Plans ───────────────────────────────────────── */}
      <section className="pb-20">
        <div className="max-w-5xl mx-auto px-4">
          <div className="grid md:grid-cols-2 gap-8 max-w-3xl mx-auto">
            {/* Starter */}
            <div className="rounded-2xl border border-gray-800 bg-gray-900 p-8 flex flex-col">
              <div className="mb-6">
                <h2 className="text-xl font-bold text-white mb-1">Starter</h2>
                <p className="text-gray-400 text-sm">
                  Everything you need to run a single{" "}
                  <span className="text-red-500">OpenClaw</span> agent.
                </p>
              </div>

              <div className="mb-8">
                <div className="flex items-baseline gap-1">
                  <span className="text-5xl font-bold text-white">
                    ${billing === "monthly" ? "15" : "10"}
                  </span>
                  <span className="text-gray-400">/mo</span>
                </div>
                {billing === "annual" && (
                  <p className="text-sm text-teal-400 mt-1">
                    $120/year — save $60
                  </p>
                )}
                {billing === "monthly" && (
                  <p className="text-sm text-gray-500 mt-1">
                    or $10/mo billed annually
                  </p>
                )}
              </div>

              <div className="space-y-3 mb-8 flex-1">
                <Spec icon={<Cpu className="w-4 h-4" />} label="2 vCPU" />
                <Spec
                  icon={<MemoryStick className="w-4 h-4" />}
                  label="2 GB RAM"
                />
                <Spec
                  icon={<HardDrive className="w-4 h-4" />}
                  label="50 GB NVMe storage"
                />
                <Spec
                  icon={<Globe className="w-4 h-4" />}
                  label="Custom subdomain + auto-TLS"
                />
                <Spec
                  icon={<Shield className="w-4 h-4" />}
                  label="Zero-trust security (3-layer auth)"
                />
                <Spec
                  icon={<Terminal className="w-4 h-4" />}
                  label="Web terminal built in"
                />
                <Spec
                  icon={<Zap className="w-4 h-4" />}
                  label="Automatic backups & upgrades"
                />
              </div>

              <a
                href="#reserve"
                className="block w-full text-center rounded-lg border border-orange-500 px-6 py-3 font-semibold text-orange-500 hover:bg-orange-500 hover:text-white transition-colors"
              >
                Reserve a Spot
              </a>
            </div>

            {/* Pro */}
            <div className="rounded-2xl border-2 border-orange-500 bg-gray-900 p-8 flex flex-col relative">
              <div className="absolute -top-3.5 left-1/2 -translate-x-1/2">
                <span className="rounded-full bg-orange-500 px-4 py-1 text-xs font-semibold text-white uppercase tracking-wide">
                  Most Popular
                </span>
              </div>

              <div className="mb-6">
                <h2 className="text-xl font-bold text-white mb-1">Pro</h2>
                <p className="text-gray-400 text-sm">
                  For power users and teams running browser automation, multi-agent workflows, or heavy workloads.
                </p>
              </div>

              <div className="mb-8">
                <div className="flex items-baseline gap-1">
                  <span className="text-5xl font-bold text-white">
                    ${billing === "monthly" ? "30" : "20"}
                  </span>
                  <span className="text-gray-400">/mo</span>
                </div>
                {billing === "annual" && (
                  <p className="text-sm text-teal-400 mt-1">
                    $240/year — save $120
                  </p>
                )}
                {billing === "monthly" && (
                  <p className="text-sm text-gray-500 mt-1">
                    or $20/mo billed annually
                  </p>
                )}
              </div>

              <div className="space-y-3 mb-8 flex-1">
                <Spec icon={<Cpu className="w-4 h-4" />} label="4 vCPU" />
                <Spec
                  icon={<MemoryStick className="w-4 h-4" />}
                  label="4 GB RAM"
                />
                <Spec
                  icon={<HardDrive className="w-4 h-4" />}
                  label="100 GB NVMe storage"
                />
                <Spec
                  icon={<Globe className="w-4 h-4" />}
                  label="Custom subdomain + auto-TLS"
                />
                <Spec
                  icon={<Shield className="w-4 h-4" />}
                  label="Zero-trust security (3-layer auth)"
                />
                <Spec
                  icon={<Terminal className="w-4 h-4" />}
                  label="Web terminal built in"
                />
                <Spec
                  icon={<Zap className="w-4 h-4" />}
                  label="Automatic backups & upgrades"
                />
                <Spec
                  icon={<Globe className="w-4 h-4 text-teal-400" />}
                  label="Headless browser automation"
                  highlight
                />
                <Spec
                  icon={<Check className="w-4 h-4 text-teal-400" />}
                  label="Priority support"
                  highlight
                />
              </div>

              <a
                href="#reserve"
                className="block w-full text-center rounded-lg bg-orange-500 px-6 py-3 font-semibold text-white hover:bg-orange-600 active:bg-orange-700 transition-colors inline-flex items-center justify-center gap-2"
              >
                Reserve a Spot
                <ArrowRight className="w-4 h-4 inline" />
              </a>
            </div>
          </div>
        </div>
      </section>

      {/* ── Everything Included ──────────────────────────── */}
      <section className="py-20 bg-gray-900">
        <div className="max-w-4xl mx-auto px-4">
          <h2 className="text-3xl font-bold text-white text-center mb-4">
            Everything Included. No Upsells.
          </h2>
          <p className="text-gray-400 text-center mb-12 max-w-xl mx-auto">
            Other hosts charge extra for backups, monitoring, and SSL.
            We include it all.
          </p>
          <div className="grid sm:grid-cols-2 md:grid-cols-3 gap-x-8 gap-y-4 max-w-2xl mx-auto">
            {[
              "KVM hardware isolation",
              "Zero-trust edge auth",
              "Automatic backups",
              "One-click upgrades",
              "Web terminal access",
              "SSH via CF certificates",
              "Auto-TLS certificates",
              "Health monitoring",
              "Persistent storage",
              "Google & GitHub SSO",
              "Passwordless email login",
              "Team accounts",
            ].map((item) => (
              <div key={item} className="flex items-center gap-2 py-2">
                <Check className="w-4 h-4 text-teal-400 flex-shrink-0" />
                <span className="text-gray-300 text-sm">{item}</span>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* ── FAQ ──────────────────────────────────────────── */}
      <section className="py-20 bg-gray-950">
        <div className="max-w-3xl mx-auto px-4">
          <h2 className="text-3xl font-bold text-white text-center mb-12">
            Frequently Asked Questions
          </h2>
          <div className="space-y-8">
            <FaqItem
              q="Do I need to bring my own API keys?"
              a="Yes — you add your Anthropic, OpenAI, or Google API keys in the dashboard. We encrypt them with AES-256-GCM and inject them securely at boot. Your keys never touch the VM directly."
            />
            <FaqItem
              q="What's the difference between Starter and Pro?"
              a="More compute and storage. Starter (2 vCPU, 2 GB RAM, 50 GB disk) handles chat, tool use, and light browser automation. Pro (4 vCPU, 4 GB RAM, 100 GB disk) is built for heavy Playwright sessions, multi-model workflows, and larger datasets."
            />
            <FaqItem
              q="Can I upgrade or downgrade later?"
              a="Yes. You can switch plans at any time from the dashboard. Upgrades take effect immediately. Downgrades apply at the next billing cycle."
            />
            <FaqItem
              q="Is there a free tier?"
              a="Not yet — but we're working on it. Reserve a spot and we'll notify you when it launches."
            />
            <FaqItem
              q="What about LLM costs?"
              a="LLM API costs (Anthropic, OpenAI, etc.) are separate — you pay your provider directly using your own keys. We provide per-machine budget controls so you never get a surprise bill."
            />
            <FaqItem
              q="Can I run multiple machines?"
              a="Absolutely. Each machine is a separate subscription. Team accounts let multiple users manage machines under one organization."
            />
          </div>
        </div>
      </section>

      {/* ── CTA with Reserve Form ──────────────────────── */}
      <section id="reserve" className="py-16 bg-gray-900">
        <div className="max-w-2xl mx-auto px-4 text-center">
          <h2 className="text-3xl font-bold text-white mb-4">
            Ready to Stop Babysitting Your VPS?
          </h2>
          <p className="text-gray-400 mb-8">
            Reserve your spot for early access. No credit card required.
          </p>
          {submitted ? (
              <div className="inline-flex items-center gap-2 rounded-lg bg-teal-500/10 border border-teal-500/30 px-6 py-3 text-teal-400">
                <Check className="w-5 h-5" />
                Thanks! We'll be in touch.
              </div>
          ) : (
            <form
              onSubmit={handleReserve}
              className="flex flex-col sm:flex-row gap-3 max-w-md mx-auto"
            >
              <div className="relative flex-1">
                <Mail className="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-gray-500" />
                <input
                  type="email"
                  required
                  placeholder="your@email.com"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="w-full pl-11 pr-4 py-3.5 rounded-lg bg-gray-800 border border-gray-700 text-white placeholder-gray-500 focus:outline-none focus:border-orange-500 focus:ring-1 focus:ring-orange-500 transition-colors"
                />
              </div>
              <button
                type="submit"
                className="rounded-lg bg-orange-500 px-6 py-3.5 font-semibold text-white hover:bg-orange-600 active:bg-orange-700 transition-colors inline-flex items-center justify-center gap-2 whitespace-nowrap"
              >
                Reserve a Spot
                <ArrowRight className="w-5 h-5" />
              </button>
            </form>
          )}
        </div>
      </section>

      <PublicFooter />

      <WaitlistSurvey email={email} open={showSurvey} onClose={() => setShowSurvey(false)} />
    </div>
  );
}

/* ── Reusable Components ────────────────────────────────── */

function Spec({
  icon,
  label,
  highlight,
}: {
  icon: React.ReactNode;
  label: string;
  highlight?: boolean;
}) {
  return (
    <div className="flex items-center gap-3">
      <span className={highlight ? "" : "text-gray-500"}>{icon}</span>
      <span className={highlight ? "text-teal-400 font-medium" : "text-gray-300"}>
        {label}
      </span>
    </div>
  );
}

function FaqItem({ q, a }: { q: string; a: string }) {
  return (
    <div>
      <h3 className="text-white font-semibold mb-2">{q}</h3>
      <p className="text-gray-400 leading-relaxed">{a}</p>
    </div>
  );
}
