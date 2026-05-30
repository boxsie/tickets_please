## Testing evidence
go test -count=1 ./internal/web/... PASS. go test ./... PASS. make build green.

## Work summary
Created internal/web/components/pages/{logs,settings}.templ and internal/web/components/partials/{error,flash,fs_picker,frozen_actions,logs_pre}.templ. Added RenderTemplError to render.go. Switched handlers_logs.go, handlers_settings.go, handlers_fs.go to templ equivalents.

## Learnings
Adding RenderTemplError alongside the old Renderer.Error was the cleanest migration path — every untouched handler keeps working, no big-bang refactor. Chrome provider is safe to call multiple times on the same request; readAndClearFlash defers cookie-clearing but re-reading the request cookie still works — convenient when a handler builds props that need chrome and then RenderTempl also calls chrome. Templ's class={"a", "b-"+x} syntax handles dynamic class names cleanly. A templ partial mirroring a web-package struct (FSPickerProps mirrors fsListing) needs an explicit converter at the seam to dodge web→partials→web cycles. Old .tmpl partials still referenced by un-ported pages stayed on disk during parallel migration — cleanup ticket drops them once nothing references them. NOTE: parallel agents created two fs_picker.templ implementations (one in components/partials/, one in components/pages/projects/) — converge in cleanup; components/partials is canonical.
