/** @type {import('tailwindcss').Config} */
module.exports = {
  // Templates live one level up, in ../templates (relative to static/, which
  // is where the tailwindcss CLI runs from per static/README.md). Without
  // these globs Tailwind would purge every utility because it can't see the
  // markup that references them — and worse, @layer components also gets
  // dropped, so the entire app.css comes out empty (0 bytes).
  content: [
    "../templates/**/*.tmpl",
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
  // Safelist every class our hand-authored components rely on. Tailwind's
  // content scan picks up classes that appear literally in the templates,
  // but server-rendered class names (e.g. badge-{{.Column}} → badge-todo)
  // don't appear as static strings, so they need to be safelisted explicitly.
  safelist: [
    "topbar", "brand", "search", "agent", "banner-warn",
    "layout", "sidebar", "sidebar-heading", "sidebar-list", "sidebar-link",
    "sidebar-empty", "sidebar-actions", "sidebar-action", "is-active",
    "flash", "flash-success", "flash-info", "flash-error",
    "empty-state", "form-page", "stack", "hint", "muted",
    "error", "form-error", "form-actions",
    "card", "page-header", "page-actions", "tabs", "tab",
    "btn", "btn-link", "btn-danger", "btn-primary",
    "data-table", "bare-list", "markdown",
    "danger-zone",
    "board-page", "board-columns", "board-column", "board-column-header",
    "muted-section", "ticket-cards", "ticket-cards-empty", "ticket-card",
    "ticket-card-title", "ticket-card-meta", "blocked", "inline-filter",
    "badge",
    { pattern: /^badge-(todo|in_progress|testing|done|system|blocked)$/ },
    "frozen-badge", "frozen-card",
    "comments-section", "comments-list", "comments-empty",
    "comment-row", "comment-system", "comment-meta", "comment-author",
    "comment-body", "comment-form",
    "search-page", "search-form", "search-tabs", "search-tab", "active",
    "search-results", "search-hits", "search-hit", "search-hit-row",
    "search-hit-title", "search-hit-snippet", "score",
    "wave-section",
    "assign-phase-form",
    "phases-index", "phase-detail", "phase-summary", "phases-empty",
    "project-detail", "project-summary", "project-index",
    "ticket-detail", "ticket-grid", "ticket-main", "ticket-side",
    "ticket-meta", "id-pill", "transition", "subtitle",
    "card-compact", "btn-sm", "short", "count",
    "breadcrumb-sep", "footnote",
    { pattern: /^board-column-(todo|in_progress|testing|done)$/ },
  ],
  plugins: [],
};
