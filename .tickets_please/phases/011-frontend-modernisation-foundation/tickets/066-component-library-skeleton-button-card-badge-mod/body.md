Build a small reusable templ component set so every migrated page composes from the same primitives, and a dev-only playground that renders all variants at once for visual review.

## Acceptance

- `internal/web/components/ui/` contains: `button.templ`, `card.templ`, `badge.templ`, `modal.templ`, `table.templ`, `form.templ` (input/select/textarea/label), `toast.templ`.
- Each component takes a typed Go struct as input (e.g. `Button(props ButtonProps)`) — no `interface{}`.
- Variants encoded as enum-like string types (e.g. `ButtonVariantPrimary`, `ButtonVariantDanger`, `ButtonVariantGhost`) — switch in templ, classes in one place.
- A `/_dev/components` route (gated behind `deps.Dev`) renders every component with every variant on one scrollable page. Used as the visual-regression smoke test during migration.
- All Tailwind classes go through the components — no raw Tailwind in page templates for primitives covered by the library.

## Hints

- Look at how shadcn/ui structures component variants (`cva` equivalent in Go can be a simple `switch`).
- Keep modals as `&lt;dialog&gt;` elements with the same JS open/close handlers as the current detail-page modals (`internal/web/templates/pages/tickets/detail.tmpl` lines ~110-130).
