package web

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// _-prefixed files are listed explicitly because Go's embed skips them
// otherwise. The directory entries (templates/pages, templates/partials,
// static) recurse and pick up everything inside that doesn't start with _ or
// . — which is exactly what we want for static/_src/ (Tailwind sources, not
// shipped in the binary).
//
//go:embed templates/_layout.tmpl templates/_nav.tmpl
//go:embed templates/pages templates/partials
var embeddedTemplates embed.FS

//go:embed static
var embeddedStatic embed.FS

// templatesFS returns the file system templates are loaded from. In prod
// (dev=false) it's the embedded FS frozen at build time. In dev mode it's
// the on-disk templates/ directory next to this source file, so editing a
// .tmpl and refreshing the browser shows the change without rebuilding.
func templatesFS(dev bool) fs.FS {
	if !dev {
		sub, err := fs.Sub(embeddedTemplates, "templates")
		if err != nil {
			// Should be unreachable: the embed directive matches templates/.
			panic(err)
		}
		return sub
	}
	return os.DirFS(filepath.Join(sourceDir(), "templates"))
}

// staticFS returns the file system /static/ is served from. Same prod/dev
// split as templatesFS.
func staticFS(dev bool) fs.FS {
	if !dev {
		sub, err := fs.Sub(embeddedStatic, "static")
		if err != nil {
			panic(err)
		}
		return sub
	}
	return os.DirFS(filepath.Join(sourceDir(), "static"))
}

// sourceDir returns the absolute directory holding this source file. Used by
// dev mode to find templates/ and static/ on disk relative to the package.
// Stable as long as the file isn't moved within the package.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(file)
}
