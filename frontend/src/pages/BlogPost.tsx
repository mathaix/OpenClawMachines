import { useParams, Link } from "react-router-dom";
import { Helmet } from "react-helmet-async";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeRaw from "rehype-raw";
import { ArrowLeft, Clock, Eye } from "lucide-react";
import { PublicHeader } from "@/components/PublicHeader";
import { PublicFooter } from "@/components/PublicFooter";
import { getPostBySlug, publishedPosts } from "@/lib/blog";

export function BlogPost() {
  const { slug } = useParams<{ slug: string }>();
  const post = slug ? getPostBySlug(slug) : undefined;

  if (!post) {
    return (
      <div className="min-h-screen bg-gray-950">
        <PublicHeader />
        <div className="max-w-3xl mx-auto px-4 py-20 text-center">
          <h1 className="text-3xl font-bold text-white mb-4">Article not found</h1>
          <Link to="/blog" className="text-orange-400 hover:text-orange-300">
            Back to Blog
          </Link>
        </div>
        <PublicFooter />
      </div>
    );
  }

  const morePosts = publishedPosts
    .filter((p) => p.slug !== post.slug)
    .slice(0, 3);

  const canonicalUrl = `https://openclawmachines.com/blog/${post.slug}`;
  const ogImage = post.heroImage
    ? `https://openclawmachines.com${post.heroImage}`
    : "https://openclawmachines.com/og-image.png";

  const jsonLd = {
    "@context": "https://schema.org",
    "@type": "BlogPosting",
    headline: post.title,
    description: post.excerpt,
    image: ogImage,
    datePublished: post.date,
    author: {
      "@type": "Person",
      name: post.author,
      ...(post.authorUrl ? { url: post.authorUrl } : {}),
    },
    publisher: {
      "@type": "Organization",
      name: "OpenClaw Machines",
      logo: {
        "@type": "ImageObject",
        url: "https://openclawmachines.com/branding/logo-icon.svg",
      },
    },
    mainEntityOfPage: canonicalUrl,
  };

  return (
    <div className="min-h-screen bg-gray-950">
      <Helmet>
        <title>{post.title} — OpenClaw Machines</title>
        <meta name="description" content={post.excerpt} />
        <link rel="canonical" href={canonicalUrl} />
        <meta property="og:type" content="article" />
        <meta property="og:title" content={post.title} />
        <meta property="og:description" content={post.excerpt} />
        <meta property="og:url" content={canonicalUrl} />
        <meta property="og:image" content={ogImage} />
        <meta property="article:published_time" content={post.date} />
        <meta property="article:author" content={post.author} />
        <meta name="twitter:card" content="summary_large_image" />
        <meta name="twitter:title" content={post.title} />
        <meta name="twitter:description" content={post.excerpt} />
        <meta name="twitter:image" content={ogImage} />
        <script type="application/ld+json">{JSON.stringify(jsonLd)}</script>
      </Helmet>
      <PublicHeader />

      <article className="max-w-[680px] mx-auto px-4 py-12 md:py-16">
        {/* Back link */}
        <Link
          to="/blog"
          className="inline-flex items-center gap-1.5 text-sm text-gray-500 hover:text-orange-400 transition-colors mb-8"
        >
          <ArrowLeft className="w-4 h-4" />
          Back to Blog
        </Link>

        {/* Draft banner */}
        {post.draft && (
          <div className="flex items-center gap-2 mb-6 px-4 py-3 rounded-lg bg-amber-500/10 border border-amber-500/30 text-amber-400 text-sm">
            <Eye className="w-4 h-4" />
            <span className="font-medium">Draft Preview</span>
            <span className="text-amber-500/70">&mdash; This article is not published yet.</span>
          </div>
        )}

        {/* Title */}
        <h1 className="text-3xl md:text-4xl lg:text-[42px] font-bold text-white leading-tight mb-4">
          {post.title}
        </h1>

        {/* Subtitle */}
        {post.subtitle && (
          <p className="text-lg md:text-xl text-gray-400 leading-relaxed mb-6">
            {post.subtitle}
          </p>
        )}

        {/* Meta */}
        <div className="flex flex-wrap items-center gap-4 text-sm text-gray-500 mb-10 pb-8 border-b border-gray-800">
          {post.authorUrl ? (
            <a href={post.authorUrl} target="_blank" rel="noopener noreferrer" className="text-teal-400 hover:text-teal-300 transition-colors">
              {post.author}
            </a>
          ) : (
            <span>{post.author}</span>
          )}
          <span>&middot;</span>
          <span>{post.date}</span>
          <span className="flex items-center gap-1">
            <Clock className="w-3.5 h-3.5" />
            {post.readTime}
          </span>
        </div>

        {/* Article body */}
        <div className="prose prose-invert prose-lg max-w-none blog-content">
          <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeRaw]}>
            {post.content}
          </ReactMarkdown>
        </div>
      </article>

      {/* More from the blog */}
      {morePosts.length > 0 && (
        <section className="border-t border-gray-800 py-16">
          <div className="max-w-6xl mx-auto px-4">
            <h2 className="text-2xl font-bold text-white mb-8">
              More from the blog
            </h2>
            <div className="grid md:grid-cols-3 gap-6">
              {morePosts.map((p) => (
                <Link
                  key={p.slug}
                  to={`/blog/${p.slug}`}
                  className="rounded-xl border border-gray-800 bg-gray-900 p-6 hover:border-orange-500/30 transition-colors"
                >
                  <span className="text-xs font-medium px-2 py-0.5 rounded-full bg-orange-500/10 text-orange-400">
                    {p.category}
                  </span>
                  <h3 className="text-lg font-bold text-white mt-3 mb-2">
                    {p.title}
                  </h3>
                  <p className="text-sm text-gray-400 line-clamp-2">
                    {p.excerpt}
                  </p>
                </Link>
              ))}
            </div>
          </div>
        </section>
      )}

      <PublicFooter />
    </div>
  );
}

export default BlogPost;
