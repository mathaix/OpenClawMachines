import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import Sitemap from "vite-plugin-sitemap";
import path from "path";
import fs from "fs";

// Read blog post slugs from frontmatter at build time
function getBlogRoutes(): string[] {
  const blogDir = path.resolve(__dirname, "src/content/blog");
  if (!fs.existsSync(blogDir)) return [];
  return fs.readdirSync(blogDir)
    .filter((f) => f.endsWith(".md"))
    .filter((f) => {
      const content = fs.readFileSync(path.join(blogDir, f), "utf-8");
      return !/^draft:\s*true/m.test(content);
    })
    .map((f) => `/blog/${f.replace(/\.md$/, "")}`);
}

export default defineConfig({
  plugins: [
    react(),
    Sitemap({
      // Operators: set VITE_SITE_URL so generated sitemap URLs use your domain.
      hostname: process.env.VITE_SITE_URL || "https://openclawmachines.com",
      dynamicRoutes: ["/blog", ...getBlogRoutes()],
      exclude: ["/dashboard", "/admin", "/branding/preview"],
      generateRobotsTxt: false,
    }),
  ],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
