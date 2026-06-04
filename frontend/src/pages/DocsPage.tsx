import { useParams, Link } from "react-router-dom";
import { Helmet } from "react-helmet-async";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeRaw from "rehype-raw";
import { ArrowLeft, ChevronLeft, ChevronRight } from "lucide-react";
import { PublicHeader } from "@/components/PublicHeader";
import { PublicFooter } from "@/components/PublicFooter";
import { getDocBySlug, allDocs } from "@/lib/docs";

export function DocsPage() {
  const { slug } = useParams<{ slug: string }>();
  const doc = slug ? getDocBySlug(slug) : undefined;

  if (!doc) {
    return (
      <div className="min-h-screen bg-gray-950">
        <PublicHeader />
        <div className="max-w-3xl mx-auto px-4 py-20 text-center">
          <h1 className="text-3xl font-bold text-white mb-4">Page not found</h1>
          <Link to="/docs" className="text-orange-400 hover:text-orange-300">
            Back to Docs
          </Link>
        </div>
        <PublicFooter />
      </div>
    );
  }

  const currentIndex = allDocs.findIndex((d) => d.slug === doc.slug);
  const prevDoc = currentIndex > 0 ? allDocs[currentIndex - 1] : undefined;
  const nextDoc = currentIndex < allDocs.length - 1 ? allDocs[currentIndex + 1] : undefined;

  const canonicalUrl = `https://openclawmachines.com/docs/${doc.slug}`;

  return (
    <div className="min-h-screen bg-gray-950">
      <Helmet>
        <title>{doc.title} — OpenClaw Machines Docs</title>
        <meta name="description" content={doc.excerpt} />
        <link rel="canonical" href={canonicalUrl} />
        <meta property="og:title" content={`${doc.title} — OpenClaw Machines Docs`} />
        <meta property="og:description" content={doc.excerpt} />
        <meta property="og:url" content={canonicalUrl} />
      </Helmet>
      <PublicHeader />

      <div className="max-w-6xl mx-auto px-4 py-12 md:py-16 lg:flex lg:gap-12">
        {/* Sidebar */}
        <nav className="lg:w-56 flex-shrink-0 mb-8 lg:mb-0">
          <Link
            to="/docs"
            className="inline-flex items-center gap-1.5 text-sm text-gray-500 hover:text-orange-400 transition-colors mb-6"
          >
            <ArrowLeft className="w-4 h-4" />
            All Docs
          </Link>
          <ul className="space-y-1">
            {allDocs.map((d) => (
              <li key={d.slug}>
                <Link
                  to={`/docs/${d.slug}`}
                  className={`block px-3 py-2 rounded-lg text-sm transition-colors ${
                    d.slug === doc.slug
                      ? "bg-gray-800 text-white font-medium"
                      : "text-gray-400 hover:text-white hover:bg-gray-800/50"
                  }`}
                >
                  {d.title}
                </Link>
              </li>
            ))}
          </ul>
        </nav>

        {/* Main content */}
        <article className="min-w-0 flex-1 max-w-[680px]">
          <div className="prose prose-invert prose-lg max-w-none blog-content">
            <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeRaw]}>
              {doc.content}
            </ReactMarkdown>
          </div>

          {/* Previous / Next navigation */}
          <div className="flex items-center justify-between mt-12 pt-8 border-t border-gray-800">
            {prevDoc ? (
              <Link
                to={`/docs/${prevDoc.slug}`}
                className="flex items-center gap-2 text-sm text-gray-400 hover:text-white transition-colors"
              >
                <ChevronLeft className="w-4 h-4" />
                {prevDoc.title}
              </Link>
            ) : (
              <div />
            )}
            {nextDoc ? (
              <Link
                to={`/docs/${nextDoc.slug}`}
                className="flex items-center gap-2 text-sm text-gray-400 hover:text-white transition-colors"
              >
                {nextDoc.title}
                <ChevronRight className="w-4 h-4" />
              </Link>
            ) : (
              <div />
            )}
          </div>
        </article>
      </div>

      <PublicFooter />
    </div>
  );
}

export default DocsPage;
