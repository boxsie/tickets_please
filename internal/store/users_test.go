package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

func TestUserStore_NewCreatesDirs(t *testing.T) {
	root := t.TempDir()
	us, err := NewUserStore(root, 5)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	if got := us.usersDir(); got == "" {
		t.Fatal("usersDir empty")
	}
}

func TestUserStore_WriteReadRoundTrip(t *testing.T) {
	us, err := NewUserStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	gh := "boxsie"
	now := time.Now().UTC().Round(time.Second)
	rec := &UserRecord{
		ID:          "user-1",
		Email:       "dan@example.com",
		GitHubLogin: &gh,
		DisplayName: "Dan",
		AvatarURL:   "https://example.com/dan.png",
		CreatedAt:   now,
		LastLoginAt: now,
	}
	if err := us.WriteUser(rec); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}
	got, err := us.ReadUser("user-1")
	if err != nil {
		t.Fatalf("ReadUser: %v", err)
	}
	if got.ID != rec.ID || got.Email != rec.Email || got.DisplayName != rec.DisplayName {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, rec)
	}
	if got.GitHubLogin == nil || *got.GitHubLogin != gh {
		t.Errorf("GitHubLogin round-trip: got %v want %q", got.GitHubLogin, gh)
	}
	if got.GoogleSub != nil {
		t.Errorf("GoogleSub should be nil, got %v", got.GoogleSub)
	}
}

func TestUserStore_ReadMissingReturnsErrNotFound(t *testing.T) {
	us, err := NewUserStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	_, err = us.ReadUser("ghost")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUserStore_WalkSortedByFilename(t *testing.T) {
	us, err := NewUserStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	ids := []string{"c", "a", "b"}
	for _, id := range ids {
		if err := us.WriteUser(&UserRecord{
			ID:          id,
			Email:       id + "@example.com",
			DisplayName: id,
			CreatedAt:   time.Now(),
		}); err != nil {
			t.Fatalf("WriteUser %s: %v", id, err)
		}
	}
	var seen []string
	if err := us.WalkUsers(func(r *UserRecord) error {
		seen = append(seen, r.ID)
		return nil
	}); err != nil {
		t.Fatalf("WalkUsers: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(seen) != len(want) {
		t.Fatalf("seen %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("seen[%d]=%q want %q", i, seen[i], want[i])
		}
	}
}

func TestUserStore_FindByOAuthSubject(t *testing.T) {
	us, err := NewUserStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	gh := "boxsie"
	gsub := "google-sub-xyz"
	now := time.Now().UTC().Round(time.Second)
	if err := us.WriteUser(&UserRecord{
		ID:          "dan",
		Email:       "dan@example.com",
		GitHubLogin: &gh,
		DisplayName: "Dan",
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := us.WriteUser(&UserRecord{
		ID:          "alice",
		Email:       "alice@example.com",
		GoogleSub:   &gsub,
		DisplayName: "Alice",
		CreatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}

	gotGH, err := us.FindUserByOAuthSubject("github", "boxsie")
	if err != nil {
		t.Fatalf("github lookup: %v", err)
	}
	if gotGH.ID != "dan" {
		t.Errorf("github lookup id=%q want dan", gotGH.ID)
	}

	gotG, err := us.FindUserByOAuthSubject("google", "google-sub-xyz")
	if err != nil {
		t.Fatalf("google lookup: %v", err)
	}
	if gotG.ID != "alice" {
		t.Errorf("google lookup id=%q want alice", gotG.ID)
	}

	if _, err := us.FindUserByOAuthSubject("github", "nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing github subject: expected ErrNotFound got %v", err)
	}

	if _, err := us.FindUserByOAuthSubject("twitter", "x"); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestUserStore_WithGlobalLockSerialises(t *testing.T) {
	us, err := NewUserStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	ctx := context.Background()
	called := false
	if err := us.WithGlobalLock(ctx, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("WithGlobalLock: %v", err)
	}
	if !called {
		t.Error("callback not invoked")
	}
}
