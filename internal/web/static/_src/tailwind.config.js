/** @type {import('tailwindcss').Config} */
module.exports = {
  // Templates live two levels up, in ../../templates. Without these globs
  // Tailwind would purge every utility because it can't see the markup that
  // references them.
  content: [
    "../../templates/**/*.tmpl",
  ],
  darkMode: "media",
  theme: {
    extend: {
      colors: {
        base:   "#0f1115",
        bar:    "#15171c",
        line:   "#22252d",
        hover:  "#1c1f27",
        active: "#222a3a",
        fg:     "#e6e6e6",
        muted:  "#8a8a8a",
        accent: "#6aa9ff",
        warn:   "#f5a524",
        warnbg: "#332408",
        success: "#3ecf8e",
        error:  "#ff6b6b",
      },
      fontFamily: {
        sans: ["ui-sans-serif", "system-ui", "-apple-system", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
  // We hand-author CSS classes too (e.g. .topbar, .sidebar). Keep them as a
  // safelist so they survive purge if any future template references them
  // only via class= and Tailwind doesn't pick them up.
  safelist: [
    "topbar", "brand", "search", "agent",
    "layout", "sidebar", "sidebar-heading", "sidebar-list", "sidebar-link",
    "sidebar-empty", "sidebar-actions", "sidebar-action", "is-active",
    "banner-warn", "flash", "flash-success", "flash-info", "flash-error",
    "empty-state", "form-page", "stack", "hint", "error",
  ],
  plugins: [],
};
