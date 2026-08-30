package svc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// seedRepoWithProjectAndTicket builds a real per-repo data dir at
// <tmp>/<dirName>/.tickets_please containing one project + one ticket, by
// driving a throwaway Service with cfg.DataDir pointing at that dir. Returns
// the absolute repo path (parent of .tickets_please) and the seeded ticket id.
//
// Used by tests that exercise the registered-mount CRUD paths without having
// to hand-roll yaml files.
func seedRepoWithProjectAndTicket(t *testing.T, parent, dirName, projectSlug, ticketTitle string) (repoPath, ticketID string) {
	t.Helper()
	repoPath = filepath.Join(parent, dirName)
	dataDir := filepath.Join(repoPath, ".tickets_please")
	cfg := config.Config{DataDir: dataDir, DataRoot: t.TempDir()}
	seedSvc := freshServiceWithCfg(t, cfg)
	ctx, _ := authedCtx(t, seedSvc)
	if _, err := seedSvc.CreateProject(ctx, projectSlug, projectSlug, "", validSummary()); err != nil {
		t.Fatalf("seed CreateProject: %v", err)
	}
	tk, err := seedSvc.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: projectSlug,
		Title:           ticketTitle,
		Body:            "Seed body for mount-CRUD regression test.",
	})
	if err != nil {
		t.Fatalf("seed CreateTicket: %v", err)
	}
	// Drop the seeding service before the consumer mounts the same dir; closing
	// here releases its watcher so the next Service can take ownership.
	seedSvc.Close()
	return repoPath, tk.ID
}

// TestMountedProject_CRUDPathsResolveCorrectStore is the regression for the
// bug where Service operations that addressed a ticket by id (get_ticket,
// move_ticket, complete_ticket, list_comments) returned ErrNotFound when the
// host project was registered via RegisterProjectMount instead of living in
// the default s.Store. The same call sites also wrote via s.Store.BeginOp
// regardless of mount, so even after a successful read the writes targeted
// the wrong on-disk root.
//
// The test seeds a real per-repo data dir, mounts it in a fresh Service with
// an unrelated empty central store, then exercises the previously-broken
// methods plus ListProjects. All must succeed and reflect the mounted store
// in their reads + writes.
func TestMountedProject_CRUDPathsResolveCorrectStore(t *testing.T) {
	tmp := t.TempDir()
	repoPath, ticketID := seedRepoWithProjectAndTicket(t, tmp, "liquidity", "liquidity-hud", "Render the HUD")

	// Consumer service: empty central DataDir, no eager mount; the mount is
	// registered explicitly below to mirror the real `register_agent` flow.
	s := freshServiceNoDataDir(t, config.Config{})
	ctx, agent := authedCtx(t, s)
	mountedSlug, err := s.RegisterProjectMount(ctx, repoPath)
	if err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	if mountedSlug != "liquidity-hud" {
		t.Fatalf("mounted slug = %q, want liquidity-hud", mountedSlug)
	}

	// hostStoreForTicket — the linchpin: every id-based CRUD op routes through
	// it, so if this is wrong everything downstream is wrong.
	hostStore, hostSlug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		t.Fatalf("hostStoreForTicket: %v", err)
	}
	if hostSlug != "liquidity-hud" {
		t.Fatalf("hostSlug = %q, want liquidity-hud", hostSlug)
	}
	if hostStore == s.Store {
		t.Fatal("hostStoreForTicket returned default Store; expected the per-repo mount")
	}
	if got, want := hostStore.Root, filepath.Join(repoPath, ".tickets_please"); got != want {
		t.Fatalf("hostStore.Root = %q, want %q", got, want)
	}

	// GetTicket — was failing with ErrNotFound for mounted projects.
	got, err := s.GetTicket(ctx, ticketID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.ID != ticketID {
		t.Fatalf("GetTicket id mismatch: got %q want %q", got.ID, ticketID)
	}

	// ListComments — also failing pre-fix.
	if _, err := s.ListComments(ctx, ticketID); err != nil {
		t.Fatalf("ListComments: %v", err)
	}

	// MoveTicket todo→in_progress — write path must hit the mount's store, not
	// s.Store. We confirm by inspecting the on-disk yaml afterwards.
	if _, err := s.MoveTicket(ctx, ticketID, domain.ColumnInProgress, "starting work"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}

	// CompleteTicket — most error-prone path: writes ticket.yaml +
	// completion.md + system_completion comment. All three must land in the
	// mount's data dir, not the central one.
	if _, err := s.CompleteTicket(
		ctx, ticketID,
		"Tested via regression suite — `go test ./internal/svc -run MountedProject`.",
		"Verified CRUD paths route through the mounted store after registry hookup.",
		"Mount-aware lookup must be threaded through every write site, not just the read path.",
	); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	// All write paths above must land under the mount's root, NOT s.Store.Root.
	// We re-walk via the mounted store (the API guarantees per-mount resolution
	// after this fix) and assert the canonical files exist there.
	mountStore, err := s.ResolveProjectStore(ctx, "liquidity-hud")
	if err != nil {
		t.Fatalf("ResolveProjectStore: %v", err)
	}
	relDir, _, err := s.findTicketDirAndNumber(mountStore, "liquidity-hud", ticketID)
	if err != nil {
		t.Fatalf("findTicketDirAndNumber on mount: %v", err)
	}
	for _, f := range []string{"ticket.yaml", "completion.md"} {
		path := filepath.Join(mountStore.Root, relDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist in mount store, missing: %s (%v)", f, path, err)
		}
	}

	// And s.Store must NOT have grown a stray copy of the ticket — that would
	// be the old bug silently writing to the central root.
	if _, _, err := s.findTicketDirAndNumber(s.Store, "liquidity-hud", ticketID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound walking s.Store for the mounted ticket, got %v", err)
	}

	// ListProjects — must include the mounted project. Pre-fix it returned an
	// empty slice because the walk was confined to s.Store.
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var found bool
	for _, p := range projects {
		if p.Slug == "liquidity-hud" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListProjects did not include mounted project; got %d projects, want one with slug 'liquidity-hud'", len(projects))
	}
	_ = agent
}
