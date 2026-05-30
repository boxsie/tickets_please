## Testing evidence
make generate, go build, go vet, go test ./internal/web/... all green. make check green.

## Work summary
Created internal/web/components/pages/projects/ with seven page templ files plus data.go (mirror types) and helpers.go. Added shared internal/web/components/md/ markdown helper. Switched handlers_projects.go, handlers_project_search.go, handlers_project_settings.go to RenderTempl / RenderTemplPartial. Semantic class names preserved verbatim.

## Learnings
Mirror handler-side payload types as locally-named structs in the page templ package — keeps components→web direction-free and prevents import cycles. For dynamic event handlers like onsubmit="return confirm(...)" use templ.JSUnsafeFuncCall(jsString) — bare onsubmit="…" only supports literal attributes. svc.*Hit.Score is float32 not float64. Created internal/web/components/md/ as the canonical templ markdown helper duplicating goldmark config from internal/web/markdown.go to dodge the import cycle. NOTE: fs_picker.tmpl ended up with two templ implementations because two parallel agents ported it independently — converge on components/partials/ in cleanup. Tests assert templ-rendered HTML byte strings so semantic class names preserved verbatim — utility-class rewrites deferred to Phase 2.
