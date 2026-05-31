package web

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// The static directory recurses into everything that doesn't start with _ or
// . — which is exactly what we want for static/_src/ (Tailwind sources, not
// shipped in the binary). Templates are no longer embedded: pages are
// templ-generated Go code compiled into the binary directly.
//
//go:embed static
var embeddedStatic embed.FS

// staticFS returns the file system /static/ is served from. In prod
// (dev=false) it's the embedded FS frozen at build time. In dev mode it's
// the on-disk static/ directory next to this source file, so editing CSS
// and refreshing the browser shows the change without rebuilding.
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
// dev mode to find static/ on disk relative to the package. Stable as long
// as the file isn't moved within the package.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(file)
}
