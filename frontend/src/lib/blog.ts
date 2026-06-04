export interface BlogPost {
  slug: string;
  title: string;
  subtitle?: string;
  date: string;
  category: string;
  excerpt: string;
  readTime: string;
  author: string;
  authorUrl?: string;
  heroImage?: string;
  featured: boolean;
  draft: boolean;
  content: string;
}

// Load all markdown files at build time
const modules = import.meta.glob("../content/blog/*.md", {
  eager: true,
  query: "?raw",
  import: "default",
}) as Record<string, string>;

function parseFrontmatter(raw: string): { data: Record<string, string>; content: string } {
  const match = raw.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);
  if (!match) return { data: {}, content: raw };
  const data: Record<string, string> = {};
  for (const line of match[1].split("\n")) {
    const idx = line.indexOf(":");
    if (idx === -1) continue;
    const key = line.slice(0, idx).trim();
    const val = line.slice(idx + 1).trim().replace(/^["']|["']$/g, "");
    if (key) data[key] = val;
  }
  return { data, content: match[2] };
}

function parsePost(filename: string, raw: string): BlogPost {
  const { data, content } = parseFrontmatter(raw);
  return {
    slug: data.slug || filename.replace(/\.md$/, ""),
    title: data.title || "Untitled",
    subtitle: data.subtitle,
    date: data.date || "",
    category: data.category || "Product",
    excerpt: data.excerpt || "",
    readTime: data.readTime || "",
    author: data.author || "OCM Team",
    authorUrl: data.authorUrl,
    heroImage: data.heroImage,
    featured: data.featured === "true",
    draft: data.draft === "true",
    content,
  };
}

// Parse all posts and sort by date (newest first)
export const allPosts: BlogPost[] = Object.entries(modules)
  .map(([path, raw]) => {
    const filename = path.split("/").pop() || "";
    return parsePost(filename, raw);
  })
  .sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime());

// Published posts only (no drafts)
export const publishedPosts: BlogPost[] = allPosts.filter((p) => !p.draft);

export function getPostBySlug(slug: string): BlogPost | undefined {
  return allPosts.find((p) => p.slug === slug);
}

export function getPostsByCategory(category: string): BlogPost[] {
  if (category === "All") return publishedPosts;
  return publishedPosts.filter((p) => p.category === category);
}

export function getFeaturedPost(): BlogPost | undefined {
  return publishedPosts.find((p) => p.featured);
}
