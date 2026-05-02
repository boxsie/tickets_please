## Testing evidence
Ran npm --prefix frontend run build and make build successfully, producing embedded assets under internal/web/static/dist and a rebuilt tickets_please binary. Live smoke against ./tickets_please web verified / returns the Vite index, /projects/tickets-please returns HTTP 200 via SPA fallback, /assets/no-such.js returns 404, and /api/nope returns 404 instead of the app shell.

## Work summary
Added embedded frontend assets with go:embed, changed the web root handler to serve the production Vite build, added SPA fallback for extensionless browser routes, preserved API 404 behavior under /api, and wired make build to build the frontend before the Go binary.

## Learnings
Go's http.FileServer redirects /index.html to ./, so SPA fallback should serve the embedded filesystem root path instead of rewriting to /index.html. Keep missing extension-bearing asset paths as real 404s so broken bundles are visible.
