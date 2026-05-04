package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Filesystem picker for /p/load. The browser can't give the server an
// absolute filesystem path (security feature of <input type="file">), but
// the server runs locally so it can list directories and let the user
// click-navigate. Each entry that contains `.tickets_please/project.yaml`
// gets a marker badge so the user can see at a glance which folders are
// loadable.
//
// Read-only. No POST routes here — the actual mount goes through the
// existing CSRF-checked POST /p/load.

const fsMaxEntries = 500

// fsEntry is one row in the directory listing. Only directories are
// returned; project files aren't useful for the picker.
type fsEntry struct {
	Name      string `json:"name"`
	IsDir     bool   `json:"isDir"`
	HasMarker bool   `json:"hasMarker"`
}

// fsListing is the payload for both the JSON API form and the partial
// template. Cwd is always absolute. Parent is empty when Cwd is the
// filesystem root.
type fsListing struct {
	Cwd       string    `json:"cwd"`
	Parent    string    `json:"parent"`
	Crumbs    []fsCrumb `json:"crumbs"`
	Entries   []fsEntry `json:"entries"`
	Truncated bool      `json:"truncated"`
	Error     string    `json:"error,omitempty"`
	// HasMarker reports whether Cwd itself is a loadable project — controls
	// the "Load this directory" button enabled state in the template.
	HasMarker bool `json:"hasMarker"`
}

// fsCrumb is one segment in the breadcrumb of the current path.
type fsCrumb struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// handleFSBrowse serves GET /api/fs?path=<abs>. On HX-Request it returns
// the fs_picker partial (rendered into #picker via outerHTML); otherwise
// returns JSON for clients that want the raw shape.
func (a *app) handleFSBrowse(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		// Default to $HOME so the picker opens somewhere useful.
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		} else {
			path = "/"
		}
	}

	listing := buildFSListing(path)

	if r.Header.Get("HX-Request") == "true" {
		a.renderer.Partial(w, r, "fs_picker", listing)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if listing.Error != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	_ = json.NewEncoder(w).Encode(listing)
}

// buildFSListing walks one directory level and returns the rendered
// payload. Errors (non-existent path, not a directory, permission denied)
// land in listing.Error so the template can surface them inline rather
// than the whole request 500'ing.
func buildFSListing(path string) fsListing {
	listing := fsListing{}
	if !filepath.IsAbs(path) {
		listing.Error = "path must be absolute"
		listing.Cwd = path
		return listing
	}
	abs := filepath.Clean(path)
	listing.Cwd = abs
	listing.Crumbs = breadcrumbsFor(abs)
	if parent := filepath.Dir(abs); parent != abs {
		listing.Parent = parent
	}

	info, err := os.Stat(abs)
	if err != nil {
		listing.Error = friendlyFSError(err)
		return listing
	}
	if !info.IsDir() {
		listing.Error = "not a directory"
		return listing
	}

	listing.HasMarker = hasProjectMarker(abs)

	entries, err := os.ReadDir(abs)
	if err != nil {
		listing.Error = friendlyFSError(err)
		return listing
	}

	out := make([]fsEntry, 0, len(entries))
	for _, e := range entries {
		// Filter to dirs (incl. symlinks to dirs) — projects are dirs.
		// Skip hidden dirs (start with ".") to keep the listing scannable
		// — `.tickets_please` itself is never the right *target* of a load,
		// only the marker we test for inside other dirs.
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, fsEntry{
			Name:      name,
			IsDir:     true,
			HasMarker: hasProjectMarker(filepath.Join(abs, name)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > fsMaxEntries {
		listing.Truncated = true
		out = out[:fsMaxEntries]
	}
	listing.Entries = out
	return listing
}

// hasProjectMarker is the cheap "is this a loadable project?" test.
// We stat the marker file rather than read it — RegisterProjectMount
// already does the YAML parse on submit; flagging in the picker is just a
// hint.
func hasProjectMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".tickets_please", "project.yaml"))
	return err == nil
}

// breadcrumbsFor returns clickable parent-path links for the current cwd.
// On a root path ("/") returns a single crumb pointing to "/".
func breadcrumbsFor(abs string) []fsCrumb {
	abs = filepath.Clean(abs)
	if abs == "/" {
		return []fsCrumb{{Label: "/", Path: "/"}}
	}
	parts := strings.Split(strings.Trim(abs, string(os.PathSeparator)), string(os.PathSeparator))
	out := make([]fsCrumb, 0, len(parts)+1)
	out = append(out, fsCrumb{Label: "/", Path: "/"})
	cur := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = cur + "/" + p
		out = append(out, fsCrumb{Label: p, Path: cur})
	}
	return out
}

// friendlyFSError maps the common os errors to user-readable strings.
// Anything else falls through to err.Error().
func friendlyFSError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "directory not found"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	default:
		return err.Error()
	}
}
