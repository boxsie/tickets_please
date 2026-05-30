// Package cache holds the in-memory project cache that warms loaded projects
// off the filesystem store. The cache is intentionally vector-free — T11
// later attaches a per-project vector index field to LoadedProject; that
// field is excluded here so the cache compiles before vecindex/embed/worker
// land.
//
// Concurrency model:
//   - ProjectCache.mu guards the loaded map and the per-slug handles map.
//   - Each LoadedProject has its own RWMutex (Lock) that callers acquire
//     when reading or mutating its fields. The convention documented by
//     T04: always take LoadedProject.Lock first, then begin the StageOp
//     under the per-project flock — never the other way around.
//   - Stale is an atomic.Bool flipped by the fsnotify watcher (and by the
//     mutating service methods themselves when they want to invalidate
//     other processes' caches). Get checks it on every lookup.
package cache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// evictTickInterval is how often RunEvictor wakes to scan for idle entries.
// SPEC pins this at 60s; tests construct a ProjectCache directly and drive
// eviction by calling the helper methods.
const evictTickInterval = 60 * time.Second

// LoadedProject is the in-memory hydrated form of a project. T11 will append
// a Vectors field for the per-project search index — same "later ticket adds
// the field" pattern T15 uses for Service.
type LoadedProject struct {
	Project      *domain.Project
	Phases       map[string]*domain.Phase     // id → phase (empty until T16)
	PhasesBySlug map[string]*domain.Phase     // slug → phase
	Tickets      map[string]*domain.Ticket    // id → ticket
	Comments     map[string][]*domain.Comment // ticket id → ordered comments
	LoadedAt     time.Time
	LastAccessAt time.Time
	Stale        atomic.Bool
	Lock         sync.RWMutex

	// watcher is closed on eviction to free the fsnotify resources. nil when
	// fsnotify is disabled by config.
	watcher *store.ProjectWatcher
	// stopWatch terminates the watcher-listen goroutine.
	stopWatch chan struct{}
}

// Resolvers is the closure bundle a ProjectCache uses to find a *store.Store
// for a given slug, plus walk every mounted store for id-only lookups. The
// indirection keeps cache→svc dependency-free: svc.New constructs and passes
// these closures, while tests can pass a fixed-store closure for the
// single-project case.
type Resolvers struct {
	// ResolveStore returns the Store hosting `slug`, or an error when no mount
	// exists for it.
	ResolveStore func(slug string) (*store.Store, error)
	// WalkAllStores invokes fn against every currently-resident project store
	// (used by the id→slug disk-walk fallback in resolveSlug).
	WalkAllStores func(fn func(*store.Store) error) error
	// FsnotifyEnabled mirrors the per-store flag: the cache used to read
	// `c.store.FsnotifyEnabled` directly; under multi-store routing the value
	// is sourced from the Service config one level up.
	FsnotifyEnabled bool
}

// ProjectCache is the slug-keyed in-memory project store with sliding TTL
// eviction. See package doc for the concurrency model.
type ProjectCache struct {
	resolvers  Resolvers
	agentStore *store.AgentStore
	idleTTL    time.Duration
	maxLoaded  int

	// Logger is the cache's structured logger. Defaults to slog.Default();
	// svc.New overrides it to share Service.Logger so eviction events land
	// in the same JSON stream as everything else.
	Logger *slog.Logger

	mu      sync.Mutex
	loaded  map[string]*LoadedProject // slug → loaded
	handles map[string]string         // diagnostic uuid → slug
	// idHandles indexes the project id → slug so Get can resolve uuids
	// against entries already loaded without re-walking the projects dir.
	idIndex map[string]string // project id → slug
}

// New builds a ProjectCache with idle TTL and max-loaded pulled from cfg. A
// zero ProjectIdleMinutes fallback to 15; zero MaxLoadedProjects to 16 so
// tests can supply a partial config without tripping the LRU bound.
//
// resolvers carries the closures the cache uses to find the Store for a given
// slug. svc.New supplies registry-backed closures; single-project tests can
// build a Resolvers whose closures always return one Store via NewWithStore.
//
// as is the central AgentStore used for agent-ref hydration (lookupAgentRef).
// It may be nil; when nil, lookupAgentRef returns a thin ref with only the id.
func New(resolvers Resolvers, as *store.AgentStore, cfg config.Config) *ProjectCache {
	idle := time.Duration(cfg.ProjectIdleMinutes) * time.Minute
	if idle <= 0 {
		idle = 15 * time.Minute
	}
	max := cfg.MaxLoadedProjects
	if max <= 0 {
		max = 16
	}
	return &ProjectCache{
		resolvers:  resolvers,
		agentStore: as,
		idleTTL:    idle,
		maxLoaded:  max,
		Logger:     slog.Default(),
		loaded:     make(map[string]*LoadedProject),
		handles:    make(map[string]string),
		idIndex:    make(map[string]string),
	}
}

// NewWithStore is a single-project convenience: builds a ProjectCache whose
// Resolvers always return st regardless of the slug requested. Used by
// single-store tests (and any future single-project caller) that want the
// previous "one Store wired in at construction" ergonomics.
func NewWithStore(st *store.Store, as *store.AgentStore, cfg config.Config) *ProjectCache {
	resolvers := Resolvers{
		ResolveStore: func(_ string) (*store.Store, error) {
			if st == nil {
				return nil, fmt.Errorf("cache: no store wired")
			}
			return st, nil
		},
		WalkAllStores: func(fn func(*store.Store) error) error {
			if st == nil {
				return nil
			}
			return fn(st)
		},
		FsnotifyEnabled: st != nil && st.FsnotifyEnabled,
	}
	return New(resolvers, as, cfg)
}

// Len returns the number of currently-loaded projects. Test helper.
func (c *ProjectCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.loaded)
}

// IdleTTL exposes the configured idle TTL. Service.LoadProject computes the
// ExpiresAt field off this.
func (c *ProjectCache) IdleTTL() time.Duration {
	return c.idleTTL
}

// MarkAccess bumps LastAccessAt for the named slug if loaded. No-op when the
// slug isn't present.
func (c *ProjectCache) MarkAccess(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lp, ok := c.loaded[slug]; ok {
		lp.LastAccessAt = time.Now()
	}
}

// Get returns the LoadedProject for the given id-or-slug, lazy-loading on
// miss and transparently reloading entries whose Stale flag is set. The
// second return is a diagnostic handle (a uuid the cache picks at insertion
// time) that the MCP load_project tool surfaces for cache introspection.
func (c *ProjectCache) Get(ctx context.Context, idOrSlug string) (*LoadedProject, string, error) {
	if idOrSlug == "" {
		return nil, "", fmt.Errorf("%w: empty id-or-slug", domain.ErrInvalidArgument)
	}

	// Resolve id → slug if we already have it loaded under that id; falls
	// through to a disk walk for unknown ids.
	slug, err := c.resolveSlug(idOrSlug)
	if err != nil {
		return nil, "", err
	}

	c.mu.Lock()
	lp, ok := c.loaded[slug]
	if ok && lp.Stale.Load() {
		c.evictLocked(slug)
		ok = false
	}
	if ok {
		lp.LastAccessAt = time.Now()
		handle := c.handleForLocked(slug)
		c.mu.Unlock()
		return lp, handle, nil
	}
	c.mu.Unlock()

	return c.loadAndInsert(ctx, slug)
}

// Load is the explicit pre-warm path used by Service.LoadProject. Identical
// semantics to Get today; kept as a separate entry point so future flavors
// (e.g. force-reload, kick a background re-embed) can diverge without
// breaking call sites.
func (c *ProjectCache) Load(ctx context.Context, idOrSlug string) (*LoadedProject, string, error) {
	return c.Get(ctx, idOrSlug)
}

// Evict drops the slug from the cache, closing its watcher. Idempotent.
func (c *ProjectCache) Evict(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(slug)
}

// Invalidate forces the next Get on the slug to reload from disk. Used by
// mutating Service methods (Update/Delete) so the in-process cache reflects
// post-write state without waiting for the fsnotify round-trip. Watcher is
// left open — the slug is still loaded; it's just marked stale.
func (c *ProjectCache) Invalidate(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lp, ok := c.loaded[slug]; ok {
		lp.Stale.Store(true)
	}
}

// CloseAll evicts every loaded project. Used by Service.Close on shutdown to
// release fsnotify resources cleanly.
func (c *ProjectCache) CloseAll() {
	c.mu.Lock()
	slugs := make([]string, 0, len(c.loaded))
	for s := range c.loaded {
		slugs = append(slugs, s)
	}
	c.mu.Unlock()
	for _, s := range slugs {
		c.Evict(s)
	}
}

// RunEvictor wakes every evictTickInterval and evicts entries whose
// LastAccessAt + idleTTL is past, plus LRU-evicts beyond maxLoaded. Returns
// when ctx is canceled.
func (c *ProjectCache) RunEvictor(ctx context.Context) {
	t := time.NewTicker(evictTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.SweepIdle(time.Now())
		}
	}
}

// SweepIdle is the body of a single eviction tick. Exposed for tests so they
// can drive eviction synchronously without waiting 60s.
func (c *ProjectCache) SweepIdle(now time.Time) {
	c.mu.Lock()
	// Idle expirations.
	expired := make([]string, 0)
	for slug, lp := range c.loaded {
		if now.Sub(lp.LastAccessAt) > c.idleTTL {
			expired = append(expired, slug)
		}
	}
	for _, slug := range expired {
		c.Logger.Info("evicted idle project", "slug", slug, "idle", now.Sub(c.loaded[slug].LastAccessAt))
		c.evictLocked(slug)
	}

	// LRU cap. Sort by LastAccessAt ascending; drop the oldest until we fit.
	if len(c.loaded) > c.maxLoaded {
		type entry struct {
			slug string
			when time.Time
		}
		es := make([]entry, 0, len(c.loaded))
		for slug, lp := range c.loaded {
			es = append(es, entry{slug, lp.LastAccessAt})
		}
		sort.Slice(es, func(i, j int) bool { return es[i].when.Before(es[j].when) })
		over := len(c.loaded) - c.maxLoaded
		for i := 0; i < over && i < len(es); i++ {
			c.Logger.Info("evicted LRU project", "slug", es[i].slug)
			c.evictLocked(es[i].slug)
		}
	}
	c.mu.Unlock()
}

// resolveSlug treats idOrSlug as a slug if any loaded entry uses it as such,
// otherwise as a project id (looked up against the in-memory id index, then
// the disk projects walk). Returns ErrNotFound when unknown.
func (c *ProjectCache) resolveSlug(idOrSlug string) (string, error) {
	c.mu.Lock()
	if _, ok := c.loaded[idOrSlug]; ok {
		c.mu.Unlock()
		return idOrSlug, nil
	}
	if slug, ok := c.idIndex[idOrSlug]; ok {
		c.mu.Unlock()
		return slug, nil
	}
	c.mu.Unlock()

	// Fall back to a projects-dir walk: callers commonly pass slugs the
	// cache hasn't seen yet, so a successful slug lookup short-circuits
	// before we touch disk for the id case.
	if slug, found, err := c.tryDiskSlug(idOrSlug); err != nil {
		return "", err
	} else if found {
		return slug, nil
	}
	if slug, found, err := c.tryDiskID(idOrSlug); err != nil {
		return "", err
	} else if found {
		return slug, nil
	}
	return "", fmt.Errorf("%w: project %q", domain.ErrNotFound, idOrSlug)
}

// tryDiskSlug returns (slug, true, nil) if a Store hosting `slug` is mounted
// and its project.yaml is readable.
func (c *ProjectCache) tryDiskSlug(slug string) (string, bool, error) {
	st, err := c.resolvers.ResolveStore(slug)
	if err != nil || st == nil {
		// Unmounted slug: treat as absent so the caller falls through to id
		// lookup or returns ErrNotFound.
		return "", false, nil
	}
	if _, err := st.ReadProject(slug); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return slug, true, nil
}

// tryDiskID walks every mounted store looking for a project whose id matches.
func (c *ProjectCache) tryDiskID(id string) (string, bool, error) {
	var found string
	err := c.resolvers.WalkAllStores(func(st *store.Store) error {
		return st.WalkProjects(func(slug string, rec *store.ProjectRecord) error {
			if rec.ID == id {
				found = slug
			}
			return nil
		})
	})
	if err != nil {
		return "", false, err
	}
	if found == "" {
		return "", false, nil
	}
	return found, true, nil
}

// loadAndInsert loads the project from disk and inserts it into the cache,
// LRU-evicting if the new entry would push us past maxLoaded.
func (c *ProjectCache) loadAndInsert(ctx context.Context, slug string) (*LoadedProject, string, error) {
	lp, err := c.loadFromDisk(ctx, slug)
	if err != nil {
		return nil, "", err
	}

	c.mu.Lock()
	// Concurrent loaders might have raced us to insertion. If another
	// goroutine got there first, drop our copy (closing its watcher) and
	// return the existing one.
	if existing, ok := c.loaded[slug]; ok && !existing.Stale.Load() {
		c.closeWatcher(lp)
		existing.LastAccessAt = time.Now()
		handle := c.handleForLocked(slug)
		c.mu.Unlock()
		return existing, handle, nil
	}
	if existing, ok := c.loaded[slug]; ok {
		// Stale entry replaced; close its watcher.
		c.closeWatcher(existing)
		delete(c.loaded, slug)
		delete(c.idIndex, existing.Project.ID)
	}

	// LRU eviction if we'd exceed max after insert.
	if len(c.loaded)+1 > c.maxLoaded {
		c.evictLRULocked(1)
	}

	c.loaded[slug] = lp
	if lp.Project != nil {
		c.idIndex[lp.Project.ID] = slug
	}
	handle := c.handleForLocked(slug)
	c.mu.Unlock()
	return lp, handle, nil
}

// evictLRULocked drops the n oldest entries by LastAccessAt. Caller must
// hold c.mu.
func (c *ProjectCache) evictLRULocked(n int) {
	if n <= 0 || len(c.loaded) == 0 {
		return
	}
	type entry struct {
		slug string
		when time.Time
	}
	es := make([]entry, 0, len(c.loaded))
	for slug, lp := range c.loaded {
		es = append(es, entry{slug, lp.LastAccessAt})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].when.Before(es[j].when) })
	for i := 0; i < n && i < len(es); i++ {
		c.Logger.Info("evicted LRU project", "slug", es[i].slug)
		c.evictLocked(es[i].slug)
	}
}

// evictLocked removes a slug from the cache and closes its watcher. Caller
// must hold c.mu.
func (c *ProjectCache) evictLocked(slug string) {
	lp, ok := c.loaded[slug]
	if !ok {
		return
	}
	c.closeWatcher(lp)
	delete(c.loaded, slug)
	if lp.Project != nil {
		delete(c.idIndex, lp.Project.ID)
	}
	// Drop any handle pointing at this slug; new Gets will mint a fresh
	// handle if the slug is reloaded.
	for h, s := range c.handles {
		if s == slug {
			delete(c.handles, h)
		}
	}
}

// closeWatcher tears down a LoadedProject's fsnotify watcher (if any) and
// stops its listener goroutine. Caller must hold c.mu for any cache entry
// shared with concurrent Get/Evict callers — the LoadedProject's watcher
// fields are guarded by c.mu, not LoadedProject.Lock. Idempotent.
func (c *ProjectCache) closeWatcher(lp *LoadedProject) {
	if lp == nil {
		return
	}
	if lp.stopWatch != nil {
		close(lp.stopWatch)
		lp.stopWatch = nil
	}
	if lp.watcher != nil {
		lp.watcher.Close()
		lp.watcher = nil
	}
}

// handleForLocked returns (or mints) a stable diagnostic handle for the
// slug. Caller must hold c.mu.
func (c *ProjectCache) handleForLocked(slug string) string {
	for h, s := range c.handles {
		if s == slug {
			return h
		}
	}
	h := uuid.NewString()
	c.handles[h] = slug
	return h
}

// loadFromDisk parses every yaml + sibling markdown file under
// projects/<slug>/, hydrates the domain types, computes BlockedBy across
// tickets, and (when fsnotify is enabled) starts a watcher goroutine that
// flips Stale on every event.
func (c *ProjectCache) loadFromDisk(ctx context.Context, slug string) (*LoadedProject, error) {
	st, err := c.resolvers.ResolveStore(slug)
	if err != nil {
		return nil, fmt.Errorf("%w: project %q", domain.ErrNotFound, slug)
	}

	rec, err := st.ReadProject(slug)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: project %q", domain.ErrNotFound, slug)
		}
		return nil, fmt.Errorf("read project: %w", err)
	}

	summary, err := st.ReadProjectSummary(slug)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read summary: %w", err)
	}

	// Build the domain.Project. CreatedBy is hydrated from the agent file
	// when the id is set; not-found is downgraded to nil so a missing
	// agent doesn't take down a load.
	proj := &domain.Project{
		ID:          rec.ID,
		Slug:        rec.Slug,
		Name:        rec.Name,
		Description: rec.Description,
		Summary:     summary,
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
	if rec.CreatedByAgentID != nil {
		proj.CreatedBy = c.lookupAgentRef(*rec.CreatedByAgentID)
	}

	// Phases (T16 populates more aggressively; we still set up empty maps
	// so callers don't have to nil-check).
	phases := map[string]*domain.Phase{}
	phasesBySlug := map[string]*domain.Phase{}
	if err := st.WalkPhases(slug, func(pr *store.PhaseRecord) error {
		ph := &domain.Phase{
			ID:          pr.ID,
			ProjectID:   pr.ProjectID,
			Slug:        pr.Slug,
			Number:      pr.Number,
			Name:        pr.Name,
			Description: pr.Description,
			CreatedAt:   pr.CreatedAt,
			UpdatedAt:   pr.UpdatedAt,
		}
		if pr.CreatedByAgentID != nil {
			ph.CreatedBy = c.lookupAgentRef(*pr.CreatedByAgentID)
		}
		phases[ph.ID] = ph
		phasesBySlug[ph.Slug] = ph
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk phases: %w", err)
	}

	// Tickets and comments.
	tickets := map[string]*domain.Ticket{}
	comments := map[string][]*domain.Comment{}
	if err := st.WalkTickets(slug, func(ticketDir, _ string, tr *store.TicketRecord) error {
		t, ts, err := c.hydrateTicket(st, ticketDir, tr)
		if err != nil {
			return err
		}
		tickets[t.ID] = t
		comments[t.ID] = ts
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk tickets: %w", err)
	}

	// Second pass: BlockedBy = depends_on entries whose ticket isn't done.
	for _, t := range tickets {
		if len(t.DependsOn) == 0 {
			continue
		}
		blocked := make([]string, 0)
		for _, dep := range t.DependsOn {
			if dt, ok := tickets[dep]; ok {
				if dt.Column != domain.ColumnDone {
					blocked = append(blocked, dep)
				}
			} else {
				// Unresolvable dependency — surface it as blocked rather
				// than silently dropping. A missing depends_on id is a
				// soft data problem; integrity check would warn.
				blocked = append(blocked, dep)
			}
		}
		if len(blocked) > 0 {
			t.BlockedBy = blocked
		}
	}

	now := time.Now()
	lp := &LoadedProject{
		Project:      proj,
		Phases:       phases,
		PhasesBySlug: phasesBySlug,
		Tickets:      tickets,
		Comments:     comments,
		LoadedAt:     now,
		LastAccessAt: now,
	}

	// Watcher: only when fsnotify is enabled in store cfg.
	if c.resolvers.FsnotifyEnabled {
		w, err := st.WatchProject(slug)
		if err != nil {
			c.Logger.Warn("watch project failed", "slug", slug, "err", err)
		} else {
			stop := make(chan struct{})
			lp.watcher = w
			lp.stopWatch = stop
			// Pass the watcher + stop channel as locals to the goroutine
			// so closeWatcher can clear lp's pointers without racing on
			// the watch loop's reads.
			go c.watchLoop(lp, slug, w, stop)
		}
	}
	_ = ctx
	return lp, nil
}

// watchLoop pumps the project watcher's events into Stale.Store(true). Stops
// when stop is closed (eviction) or the watcher's Events channel is closed
// (watcher.Close fired from another path). w + stop are captured at start
// time so they don't race with closeWatcher zeroing the LoadedProject's
// pointers.
func (c *ProjectCache) watchLoop(lp *LoadedProject, slug string, w *store.ProjectWatcher, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case _, ok := <-w.Events:
			if !ok {
				return
			}
			lp.Stale.Store(true)
			c.Logger.Debug("project marked stale", "slug", slug)
		}
	}
}

// hydrateTicket builds a domain.Ticket from a store.TicketRecord plus its
// sibling markdown files. Comments are loaded in the same pass since the
// caller already has the ticket dir resolved. st is the project's resolved
// Store (used for WalkComments), passed in so hydrateTicket doesn't have to
// re-resolve via c.resolvers.
func (c *ProjectCache) hydrateTicket(st *store.Store, ticketDir string, tr *store.TicketRecord) (*domain.Ticket, []*domain.Comment, error) {
	body, err := readFileIfExists(filepath.Join(ticketDir, "body.md"))
	if err != nil {
		return nil, nil, fmt.Errorf("read body: %w", err)
	}

	t := &domain.Ticket{
		ID:                 tr.ID,
		ProjectID:          tr.ProjectID,
		Title:              tr.Title,
		Body:               body,
		Column:             tr.Column,
		PhaseID:            tr.PhaseID,
		Wave:               tr.Wave,
		DependsOn:          append([]string(nil), tr.DependsOn...),
		ParallelizableWith: append([]string(nil), tr.ParallelizableWith...),
		CompletedAt:        tr.CompletedAt,
		Archived:           tr.Archived,
		ArchivedAt:         tr.ArchivedAt,
		CreatedAt:          tr.CreatedAt,
		UpdatedAt:          tr.UpdatedAt,
	}
	if tr.CreatedByAgentID != nil {
		t.CreatedBy = c.lookupAgentRef(*tr.CreatedByAgentID)
	}
	if tr.CompletedByAgentID != nil {
		t.CompletedBy = c.lookupAgentRef(*tr.CompletedByAgentID)
	}

	if tr.Column == domain.ColumnDone {
		comp, err := readFileIfExists(filepath.Join(ticketDir, "completion.md"))
		if err != nil {
			return nil, nil, fmt.Errorf("read completion: %w", err)
		}
		te, ws, ln := splitCompletionSections(comp)
		if te != "" {
			t.TestingEvidence = strPtr(te)
		}
		if ws != "" {
			t.WorkSummary = strPtr(ws)
		}
		if ln != "" {
			t.Learnings = strPtr(ln)
		}
	}

	cs := make([]*domain.Comment, 0)
	if err := st.WalkComments(ticketDir, func(cr *store.CommentRecord, body string) error {
		dc := &domain.Comment{
			ID:         cr.ID,
			TicketID:   cr.TicketID,
			Kind:       cr.Kind,
			Body:       body,
			FromColumn: cr.FromColumn,
			ToColumn:   cr.ToColumn,
			CreatedAt:  cr.CreatedAt,
		}
		if cr.AuthorAgentID != nil {
			dc.Author = c.lookupAgentRef(*cr.AuthorAgentID)
		}
		cs = append(cs, dc)
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("walk comments: %w", err)
	}
	return t, cs, nil
}

// lookupAgentRef returns a flat AgentRef for the given agent id, swallowing
// not-found errors so a missing agent file doesn't fail the load. Returns
// nil for the zero id. When no AgentStore is configured a thin ref (id only)
// is returned so the project still loads cleanly.
func (c *ProjectCache) lookupAgentRef(id string) *domain.AgentRef {
	if id == "" {
		return nil
	}
	if c.agentStore == nil {
		return &domain.AgentRef{ID: id}
	}
	rec, err := c.agentStore.ReadAgent(id)
	if err != nil {
		// Soft fail: integrity check surfaces dangling refs separately.
		return &domain.AgentRef{ID: id}
	}
	return &domain.AgentRef{ID: rec.ID, Name: rec.Name}
}

// splitCompletionSections parses a completion.md into its three headed
// sections. Convention: each is a `## Heading` block; we accept either of
// the two orderings the spec calls out (testing/work/learnings) and any
// missing section returns empty.
func splitCompletionSections(md string) (testing, work, learnings string) {
	if md == "" {
		return "", "", ""
	}
	lines := strings.Split(md, "\n")
	type section struct {
		name string
		buf  []string
	}
	var current *section
	sections := map[string]string{}

	flush := func() {
		if current == nil {
			return
		}
		body := strings.Join(current.buf, "\n")
		body = strings.Trim(body, "\n")
		sections[strings.ToLower(current.name)] = body
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flush()
			current = &section{name: strings.TrimSpace(strings.TrimPrefix(line, "## ")), buf: nil}
			continue
		}
		if current != nil {
			current.buf = append(current.buf, line)
		}
	}
	flush()

	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := sections[k]; ok {
				return v
			}
		}
		return ""
	}
	testing = pick("testing evidence", "testing")
	work = pick("work summary", "summary", "work")
	learnings = pick("learnings", "learning")
	return
}

func strPtr(s string) *string { return &s }

// readFileIfExists is a friendlier os.ReadFile that returns ("", nil) when
// the file is absent. Used for sibling markdown files (body.md,
// completion.md) where missing-on-disk is a soft case the integrity check
// surfaces separately.
func readFileIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
