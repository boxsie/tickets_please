package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"tickets_please/internal/config"
	"tickets_please/internal/svc"
)

// userConfigPathFn is the indirection through which handlers locate the
// on-disk config file. Production points at config.UserConfigPath which
// resolves to ~/.tickets_please/config.yaml; tests swap this to a tempdir
// path so they don't write to the real user's home dir.
var userConfigPathFn = config.UserConfigPath

// handlers_settings.go — top-level /settings page (per W5-T2 of the
// per-project-embedders phase). Edits server defaults that gate *new*
// projects: embed_provider, embed_model (ollama_model), ollama_url, plus the
// shared OpenAI key. Existing projects pin their own choice in project.yaml,
// so flipping the defaults does not rebuild live mounts — re-embed is the
// migration tool, surfaced both per-project and as "Re-embed all projects"
// here.
//
// Comment preservation is the load-bearing requirement: users hand-curate
// ~/.tickets_please/config.yaml with comments explaining their choices, and
// the form must round-trip without scrubbing them. config.SaveYAMLNode +
// SetScalar handle that — we walk the existing yaml.Node tree and only
// mutate the targeted scalar values.
//
// Concurrency: cfg writes are rare (only via this UI) and reads are loose
// per-field, so we don't take a lock around the cfg field assignments. The
// renderer reads fields one at a time and the service constructor has
// already snapshotted the original Cfg into per-mount providers.

// settingsPageData is the payload for pages/settings.tmpl. KeyMasked is true
// when an OpenAI key is set on cfg — the form renders dots instead of the
// raw value, and a blank submit leaves it untouched (treated as "no change").
type settingsPageData struct {
	EmbedProvider string
	EmbedModel    string
	OllamaURL     string
	DataDir       string
	DataRoot      string
	ConfigPath    string
	ConfigSource  string
	KeyMasked     bool

	FormError string
	Mounts    []mountRow
}

// mountRow is one entry in the project table on /settings. Slug links to the
// project, EmbedName + EmbedModel show the resolved per-mount embedder, and
// the Re-embed button POSTs to the per-project route W5-T1 owns.
type mountRow struct {
	Slug       string
	EmbedName  string
	EmbedModel string
	EmbedDim   int
}

// handleGlobalSettings serves GET /settings. Reads from a.deps.Cfg directly
// — Service updates the same struct in handleGlobalSettingsUpdate, so the
// page always shows the live values without a round-trip to disk.
func (a *app) handleGlobalSettings(w http.ResponseWriter, r *http.Request) {
	a.renderer.Page(w, r, "settings", PageOpts{
		Title: "Settings · tickets_please",
		Body:  a.buildSettingsPageData(""),
	})
}

// buildSettingsPageData snapshots cfg + the project mount registry into a
// view-model the template can render directly. Hoisted so error re-renders
// reuse the same construction.
func (a *app) buildSettingsPageData(formErr string) settingsPageData {
	cfg := a.deps.Service.Cfg
	rows := make([]mountRow, 0)
	_ = a.deps.Service.WalkProjectMounts(func(slug string, m *svc.ProjectMount) error {
		row := mountRow{Slug: slug, EmbedModel: m.EmbedModel, EmbedDim: m.EmbedDim}
		if m.Embed != nil {
			row.EmbedName = m.Embed.Name()
		}
		rows = append(rows, row)
		return nil
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slug < rows[j].Slug })
	configPath, _ := userConfigPathFn()
	return settingsPageData{
		EmbedProvider: cfg.EmbedProvider,
		EmbedModel:    cfg.OllamaModel,
		OllamaURL:     cfg.OllamaURL,
		DataDir:       cfg.DataDir,
		DataRoot:      cfg.DataRoot,
		ConfigPath:    configPath,
		ConfigSource:  cfg.Source,
		KeyMasked:     strings.TrimSpace(cfg.OpenAIKey) != "",
		FormError:     formErr,
		Mounts:        rows,
	}
}

// handleGlobalSettingsUpdate handles POST /settings. Writes only the targeted
// scalar nodes back to ~/.tickets_please/config.yaml via SaveYAMLNode (which
// preserves the surrounding comments and key order). Updates Service.Cfg in
// place so the next render reflects the change without a Service restart.
//
// The OpenAI key field is masked: a blank submit means "leave unchanged" —
// the existing value stays put, the YAML node isn't touched. This keeps the
// dots-only display from inadvertently wiping a real key.
func (a *app) handleGlobalSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.Form.Get("embed_provider"))
	model := strings.TrimSpace(r.Form.Get("embed_model"))
	ollamaURL := strings.TrimSpace(r.Form.Get("ollama_url"))
	openAIKey := r.Form.Get("openai_api_key") // raw — preserve exact bytes

	if provider != "ollama" && provider != "openai" {
		a.renderSettingsError(w, r, fmt.Errorf("embed_provider must be 'ollama' or 'openai' (got %q)", provider), http.StatusUnprocessableEntity)
		return
	}

	path, err := userConfigPathFn()
	if err != nil {
		a.renderer.Error(w, r, http.StatusInternalServerError, err)
		return
	}
	// SaveYAMLNode requires the file to exist (it reads + parses). Ensure
	// it's there with at least an empty mapping so first-run setups can save
	// even when the user has never created the file.
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			a.renderer.Error(w, r, http.StatusInternalServerError, fmt.Errorf("create config dir: %w", mkErr))
			return
		}
		if writeErr := os.WriteFile(path, []byte("# tickets_please configuration\n"), 0o644); writeErr != nil {
			a.renderer.Error(w, r, http.StatusInternalServerError, fmt.Errorf("create config file: %w", writeErr))
			return
		}
	}

	if err := config.SaveYAMLNode(path, func(root *yaml.Node) error {
		if err := config.SetScalar(root, "embed_provider", provider); err != nil {
			return err
		}
		if err := config.SetScalar(root, "ollama_model", model); err != nil {
			return err
		}
		if err := config.SetScalar(root, "ollama_url", ollamaURL); err != nil {
			return err
		}
		// Only persist a non-empty submission — blank means "leave the
		// existing key alone" (the form rendered dots, not the raw value).
		if strings.TrimSpace(openAIKey) != "" {
			if err := config.SetScalar(root, "openai_api_key", openAIKey); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		a.renderer.Error(w, r, http.StatusInternalServerError, err)
		return
	}

	// Mirror the disk write into the live Service.Cfg so subsequent renders
	// (and any new-project mounts) see the new defaults without restarting.
	a.deps.Service.Cfg.EmbedProvider = provider
	a.deps.Service.Cfg.OllamaModel = model
	a.deps.Service.Cfg.OllamaURL = ollamaURL
	if strings.TrimSpace(openAIKey) != "" {
		a.deps.Service.Cfg.OpenAIKey = openAIKey
	}

	SetFlash(w, r, "success", "Settings saved.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// handleReembedAll handles POST /settings/reembed-all. Calls
// Service.ReembedAllProjects which iterates every cached mount and queues a
// reembed; flashes the queued count and redirects back to /settings.
func (a *app) handleReembedAll(w http.ResponseWriter, r *http.Request) {
	queued, err := a.deps.Service.ReembedAllProjects(r.Context())
	if err != nil {
		// Best-effort: flash the partial result alongside the error so the
		// user sees both rather than getting a 500 with no context.
		SetFlash(w, r, "error", fmt.Sprintf("Re-embed enqueued for %d projects (with errors: %s).", queued, err.Error()))
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	SetFlash(w, r, "success", fmt.Sprintf("Re-embed enqueued for %d projects.", queued))
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// renderSettingsError re-renders /settings with an inline error at the given
// status. Used for client-side validation failures (e.g. unknown provider).
func (a *app) renderSettingsError(w http.ResponseWriter, r *http.Request, err error, status int) {
	w.WriteHeader(status)
	a.renderer.Page(w, r, "settings", PageOpts{
		Title: "Settings · tickets_please",
		Body:  a.buildSettingsPageData(err.Error()),
	})
}
