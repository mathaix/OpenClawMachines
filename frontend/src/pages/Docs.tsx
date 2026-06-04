import { Link } from "react-router-dom";
import { Helmet } from "react-helmet-async";
import { ArrowRight } from "lucide-react";
import { PublicHeader } from "@/components/PublicHeader";
import { PublicFooter } from "@/components/PublicFooter";
import { allDocs, type DocPage } from "@/lib/docs";

export function Docs() {
  return (
    <div className="min-h-screen bg-gray-950">
      <Helmet>
        <title>Docs — OpenClaw Machines</title>
        <meta name="description" content="Learn how to set up and run your own autonomous AI agent with OpenClaw Machines. Step-by-step guides from account creation to a running agent." />
        <link rel="canonical" href="https://openclawmachines.com/docs" />
        <meta property="og:title" content="Docs — OpenClaw Machines" />
        <meta property="og:description" content="Step-by-step guides for setting up and running autonomous AI agents." />
        <meta property="og:url" content="https://openclawmachines.com/docs" />
      </Helmet>
      <PublicHeader />

      {/* Page header */}
      <section className="pt-20 pb-12">
        <div className="max-w-5xl mx-auto px-6">
          <h1 className="text-5xl font-semibold text-white tracking-tight">Documentation</h1>
          <p className="mt-4 text-lg text-gray-400 max-w-2xl">
            Everything you need to go from zero to a running autonomous agent. Follow the guides in order, or jump to a specific topic.
          </p>
        </div>
      </section>

      {/* Separator */}
      <div className="max-w-5xl mx-auto px-6">
        <div className="border-b border-gray-800/60" />
      </div>

      {/* Docs grid */}
      <div className="max-w-5xl mx-auto px-6 py-12">
        <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-x-8 gap-y-12">
          {allDocs.map((doc, i) => (
            <DocCard key={doc.slug} doc={doc} index={i + 1} />
          ))}
        </div>
      </div>

      <PublicFooter />
    </div>
  );
}

function DocCard({ doc, index }: { doc: DocPage; index: number }) {
  return (
    <Link
      to={`/docs/${doc.slug}`}
      className="group flex flex-col rounded-xl border border-gray-800 p-6 hover:border-gray-700 transition-colors"
    >
      {/* Step number */}
      <div className="flex items-center gap-2 text-[13px] text-gray-500 mb-2.5">
        <span className="text-teal-400 font-medium">Step {index}</span>
        <ArrowRight className="w-3.5 h-3.5 ml-auto opacity-0 group-hover:opacity-100 transition-opacity text-gray-400" />
      </div>

      {/* Title */}
      <h2 className="text-[17px] font-medium text-gray-200 leading-snug mb-2 group-hover:text-white transition-colors">
        {doc.title}
      </h2>

      {/* Description */}
      <p className="text-sm text-gray-500 leading-relaxed line-clamp-3">
        {doc.excerpt}
      </p>
    </Link>
  );
}

export default Docs;
