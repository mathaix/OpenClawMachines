import { Link } from "react-router-dom";
import { Helmet } from "react-helmet-async";
import { ArrowRight } from "lucide-react";
import { PublicHeader } from "@/components/PublicHeader";
import { PublicFooter } from "@/components/PublicFooter";
import {
  publishedPosts,
  type BlogPost,
} from "@/lib/blog";

export function Blog() {
  const posts = publishedPosts;

  return (
    <div className="min-h-screen bg-gray-950">
      <Helmet>
        <title>Blog — OpenClaw Machines</title>
        <meta name="description" content="Deep dives on AI infrastructure, autonomous agents, and the OpenClaw ecosystem. Technical analysis and product updates from the OpenClaw Machines team." />
        <link rel="canonical" href="https://openclawmachines.com/blog" />
        <meta property="og:title" content="Blog — OpenClaw Machines" />
        <meta property="og:description" content="Deep dives on AI infrastructure, autonomous agents, and the OpenClaw ecosystem." />
        <meta property="og:url" content="https://openclawmachines.com/blog" />
      </Helmet>
      <PublicHeader />

      {/* Page header */}
      <section className="pt-20 pb-12">
        <div className="max-w-5xl mx-auto px-6">
          <h1 className="text-5xl font-semibold text-white tracking-tight">Blog</h1>
        </div>
      </section>

      {/* Separator */}
      <div className="max-w-5xl mx-auto px-6">
        <div className="border-b border-gray-800/60" />
      </div>

      {/* Posts grid */}
      <div className="max-w-5xl mx-auto px-6 py-12">
        {posts.length > 0 ? (
          <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-x-8 gap-y-12">
            {posts.map((post) => (
              <PostCard key={post.slug} post={post} />
            ))}
          </div>
        ) : (
          <p className="text-gray-500 text-center py-20">
            No articles yet.
          </p>
        )}
      </div>

      <PublicFooter />
    </div>
  );
}

function PostCard({ post }: { post: BlogPost }) {
  return (
    <Link
      to={`/blog/${post.slug}`}
      className="group flex flex-col rounded-xl border border-gray-800 p-6 hover:border-gray-700 transition-colors"
    >
      {/* Meta line */}
      <div className="flex items-center gap-2 text-[13px] text-gray-500 mb-2.5">
        <span>{post.author}</span>
        <span>&middot;</span>
        <span>{post.date}</span>
        <ArrowRight className="w-3.5 h-3.5 ml-auto opacity-0 group-hover:opacity-100 transition-opacity text-gray-400" />
      </div>

      {/* Title */}
      <h2 className="text-[17px] font-medium text-gray-200 leading-snug mb-2 group-hover:text-white transition-colors">
        {post.title}
      </h2>

      {/* Description */}
      <p className="text-sm text-gray-500 leading-relaxed line-clamp-3">
        {post.excerpt}
      </p>
    </Link>
  );
}

export default Blog;
