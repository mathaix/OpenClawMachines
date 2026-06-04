export interface DocPage {
  slug: string;
  title: string;
  order: number;
  excerpt: string;
  content: string;
}

// Load all markdown files at build time
const modules = import.meta.glob("../content/docs/*.md", {
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

function parseDoc(filename: string, raw: string): DocPage {
  const { data, content } = parseFrontmatter(raw);
  return {
    slug: data.slug || filename.replace(/\.md$/, ""),
    title: data.title || "Untitled",
    order: parseInt(data.order, 10) || 0,
    excerpt: data.excerpt || "",
    content,
  };
}

// Parse all docs and sort by order
export const allDocs: DocPage[] = Object.entries(modules)
  .map(([path, raw]) => {
    const filename = path.split("/").pop() || "";
    return parseDoc(filename, raw);
  })
  .sort((a, b) => a.order - b.order);

export function getDocBySlug(slug: string): DocPage | undefined {
  return allDocs.find((d) => d.slug === slug);
}
