package svc

import (
	"context"
	"path/filepath"
	"testing"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// TestMountedProject_FeedbackStoreAttached confirms RegisterProjectMount wires
// a usable FeedbackStore onto the ProjectMount and that mutations persist to
// the per-project feedback.yaml in the repo's data dir.
func TestMountedProject_FeedbackStoreAttached(t *testing.T) {
	tmp := t.TempDir()
	repoPath, ticketID := seedRepoWithProjectAndTicket(t, tmp, "fbproj", "fb-proj", "Wire up feedback")

	s := freshServiceNoDataDir(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	slug, err := s.RegisterProjectMount(ctx, repoPath)
	if err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	if slug != "fb-proj" {
		t.Fatalf("slug = %q", slug)
	}

	s.mountsMu.Lock()
	mount := s.projectMounts[slug]
	s.mountsMu.Unlock()
	if mount == nil {
		t.Fatal("mount missing post-register")
	}
	if mount.Feedback == nil {
		t.Fatal("Feedback store not attached to mount")
	}

	key := domain.TicketEntryKey(ticketID)
	if err := mount.Feedback.RecordRating(context.Background(), key, domain.RatingLike, "useful"); err != nil {
		t.Fatalf("RecordRating: %v", err)
	}
	rec, ok := mount.Feedback.Get(key)
	if !ok || rec.Likes != 1 {
		t.Fatalf("post-rating record = %+v ok=%v", rec, ok)
	}

	// Verify the on-disk file lives at the per-project feedback.yaml path.
	wantPath := filepath.Join(repoPath, ".tickets_please", "feedback.yaml")
	if _, err := mount.Store.ReadProject(slug); err != nil {
		t.Fatalf("project sanity: %v", err)
	}
	if mount.Store.Root != filepath.Dir(wantPath) {
		t.Fatalf("mount Store.Root = %q, want parent of feedback.yaml at %q", mount.Store.Root, wantPath)
	}
}

// TestDeleteTicket_CascadesFeedback confirms DeleteTicket removes the ticket's
// own feedback entry (and its learning placeholder) so feedback.yaml doesn't
// accumulate references to no-longer-existing entities.
func TestDeleteTicket_CascadesFeedback(t *testing.T) {
	tmp := t.TempDir()
	repoPath, ticketID := seedRepoWithProjectAndTicket(t, tmp, "fbdel", "fb-del", "Doomed ticket")

	s := freshServiceNoDataDir(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	slug, err := s.RegisterProjectMount(ctx, repoPath)
	if err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}

	s.mountsMu.Lock()
	mount := s.projectMounts[slug]
	s.mountsMu.Unlock()
	if mount == nil || mount.Feedback == nil {
		t.Fatal("mount or feedback missing")
	}

	keys := []domain.EntryKey{
		domain.TicketEntryKey(ticketID),
		domain.LearningEntryKey(ticketID),
	}
	for _, k := range keys {
		if err := mount.Feedback.RecordRating(ctx, k, domain.RatingLike, ""); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	if err := s.DeleteTicket(ctx, ticketID); err != nil {
		t.Fatalf("DeleteTicket: %v", err)
	}

	for _, k := range keys {
		if _, ok := mount.Feedback.Get(k); ok {
			t.Errorf("feedback for %q still present after DeleteTicket", k)
		}
	}
}
