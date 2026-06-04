/** @type {import('tailwindcss').Config} */
module.exports = {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        deep: 'var(--deep)',
        surface: { DEFAULT: 'var(--surface)' },
        card: { DEFAULT: 'var(--card)', hover: 'var(--card-hover)' },
        elevated: 'var(--elevated)',
        input: 'var(--input)',
        border: { DEFAULT: 'var(--border)', subtle: 'var(--border-subtle)', hover: 'var(--border-hover)' },
        brand: {
          50: "#fff7ed", 100: "#ffedd5", 200: "#fed7aa", 300: "#fdba74",
          400: "var(--brand-400)", 500: "var(--brand-500)",
          600: "var(--brand-600)", 700: "var(--brand-700)",
          glow: 'var(--brand-glow)',
        },
        'text-primary': 'var(--text-primary)',
        'text-secondary': 'var(--text-secondary)',
        'text-tertiary': 'var(--text-tertiary)',
        'text-muted': 'var(--text-muted)',
        green: { 400: 'var(--green-400)', 500: 'var(--green-500)', 600: 'var(--green-600)' },
        yellow: { 400: 'var(--yellow-400)' },
        red: { 400: 'var(--red-400)', 600: 'var(--red-600)' },
        blue: { 400: 'var(--blue-400)' },
        teal: { 400: 'var(--teal-400)' },
        purple: { 400: 'var(--purple-400)' },
      },
      fontFamily: {
        body: ['"DM Sans"', "system-ui", "sans-serif"],
        mono: ['"JetBrains Mono"', '"SF Mono"', "monospace"],
      },
      borderRadius: {
        sm: 'var(--radius-sm)',
        DEFAULT: 'var(--radius)',
        lg: 'var(--radius-lg)',
      },
      boxShadow: {
        card: 'var(--shadow-card)',
        elevated: 'var(--shadow-elevated)',
        modal: 'var(--shadow-modal)',
      },
      typography: {
        DEFAULT: { css: { maxWidth: "680px", fontSize: "1.125rem", lineHeight: "1.7" } },
        invert: {
          css: {
            "--tw-prose-body": "#d1d5db", "--tw-prose-headings": "#f9fafb",
            "--tw-prose-links": "#2dd4bf", "--tw-prose-bold": "#f3f4f6",
            "--tw-prose-quotes": "#9ca3af", "--tw-prose-quote-borders": "#ef4444",
            "--tw-prose-counters": "#9ca3af", "--tw-prose-bullets": "#6b7280",
            "--tw-prose-hr": "#374151", "--tw-prose-th-borders": "#374151",
            "--tw-prose-td-borders": "#1f2937", "--tw-prose-code": "#f9fafb",
            "--tw-prose-pre-bg": "#0a0f1e", "--tw-prose-pre-code": "#d1d5db",
          },
        },
      },
    },
  },
  plugins: [require("@tailwindcss/typography")],
}
