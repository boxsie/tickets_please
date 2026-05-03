package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// freshService returns a Service rooted at t.TempDir() with auto-commit off
// and sane TTLs. DataRoot is set to a separate tempdir so the test never
// touches the user's real ~/.tickets_please.
func freshService(t *testing.T) *Service {
	t.Helper()
	cfg := config.Config{
		DataDir:                t.TempDir(),
		DataRoot:               t.TempDir(),
		AutoCommit:             false,
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	s, err := NewWithEmbed(cfg, newFakeEmbed())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// touchPlaceholder is a stand-in mutating method used to test requireSession
// in isolation. It returns the agent the middleware resolved, or whatever
// error the middleware returned.
func (s *Service) touchPlaceholder(ctx context.Context) (*domain.Agent, error) {
	_, a, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func TestRegisterAgent_WritesYAMLWithExpectedFields(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()

	id, expiresAt, err := s.RegisterAgent(ctx, "claude:run-a", "Claude A", map[string]string{"model": "sonnet-4.7"}, 0)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if id == "" {
		t.Fatal("empty session id")
	}
	if expiresAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("expiresAt too soon: %v", expiresAt)
	}

	path := filepath.Join(s.AgentStore.Root, "agents", id+".yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected agent yaml at %s: %v", path, err)
	}

	got, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		t.Fatalf("ReadAgent: %v", err)
	}
	if got.Key != "claude:run-a" || got.Name != "Claude A" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if got.Metadata["model"] != "sonnet-4.7" {
		t.Fatalf("metadata not persisted: %+v", got.Metadata)
	}
	if got.ExpiresAt.IsZero() || got.CreatedAt.IsZero() || got.LastSeenAt.IsZero() {
		t.Fatalf("missing timestamps: %+v", got)
	}
}

func TestRegisterAgent_RejectsBlank(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	if _, _, err := s.RegisterAgent(ctx, "", "name", nil, 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for empty key, got %v", err)
	}
	if _, _, err := s.RegisterAgent(ctx, "k", "  ", nil, 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for blank name, got %v", err)
	}
}

func TestRegisterAgent_TTLCappedByMax(t *testing.T) {
	s := freshService(t)
	s.Cfg.AgentSessionMaxMinutes = 60
	ctx := context.Background()
	_, expiresAt, err := s.RegisterAgent(ctx, "k", "n", nil, 10*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt.After(time.Now().Add(61 * time.Minute)) {
		t.Fatalf("TTL not capped: %v", expiresAt)
	}
}

func TestRegisterAgent_DuplicateKeyWhileActive(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()

	if _, _, err := s.RegisterAgent(ctx, "claude:run-1", "A", nil, 0); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.RegisterAgent(ctx, "claude:run-1", "B", nil, 0)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestRegisterAgent_ReusableAfterExpiry(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()

	id, _, err := s.RegisterAgent(ctx, "claude:run-1", "A", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Force-expire the first session by rewriting its yaml.
	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	rec.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RegisterAgent(ctx, "claude:run-1", "B", nil, 0); err != nil {
		t.Fatalf("expected re-registration after expiry to succeed, got %v", err)
	}
}

func TestRequireSession_NoID(t *testing.T) {
	s := freshService(t)
	if _, err := s.touchPlaceholder(context.Background()); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
	// Empty string also should not authenticate.
	ctx := WithSessionID(context.Background(), "")
	if _, err := s.touchPlaceholder(ctx); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for empty id, got %v", err)
	}
}

func TestRequireSession_UnknownID(t *testing.T) {
	s := freshService(t)
	ctx := WithSessionID(context.Background(), "no-such-agent")
	_, err := s.touchPlaceholder(ctx)
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for unknown id, got %v", err)
	}
}

func TestRequireSession_ExpiredID(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "k", "n", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Force-expire.
	rec, _ := s.AgentStore.ReadAgent(id)
	rec.ExpiresAt = time.Now().Add(-time.Second)
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatal(err)
	}

	if _, err := s.touchPlaceholder(WithSessionID(ctx, id)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for expired session, got %v", err)
	}
}

func TestRequireSession_AttachesAgentToContext(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "k", "Claude X", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := s.touchPlaceholder(WithSessionID(ctx, id))
	if err != nil {
		t.Fatalf("touchPlaceholder: %v", err)
	}
	if resolved == nil || resolved.ID != id || resolved.Name != "Claude X" {
		t.Fatalf("unexpected agent: %+v", resolved)
	}

	// AgentFrom inside a downstream handler should work too.
	called := false
	handler := func(ctx context.Context) error {
		ctx, a, err := s.requireSession(ctx)
		if err != nil {
			return err
		}
		fromCtx, ok := AgentFrom(ctx)
		if !ok {
			t.Fatal("AgentFrom returned !ok")
		}
		if fromCtx.ID != a.ID {
			t.Fatalf("AgentFrom mismatch: %s vs %s", fromCtx.ID, a.ID)
		}
		called = true
		return nil
	}
	if err := handler(WithSessionID(ctx, id)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler did not run")
	}
}

func TestHeartbeat_UpdatesLastSeenButNotExpiry(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, originalExpiry, err := s.RegisterAgent(ctx, "k", "n", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Backdate LastSeenAt and re-write so we have a clean delta to detect.
	rec, _ := s.AgentStore.ReadAgent(id)
	rec.LastSeenAt = time.Now().Add(-2 * time.Hour)
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatal(err)
	}

	gotExpiry, err := s.Heartbeat(ctx, id)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !gotExpiry.Equal(originalExpiry) {
		t.Fatalf("Heartbeat must NOT extend ExpiresAt: orig=%v got=%v", originalExpiry, gotExpiry)
	}

	after, _ := s.AgentStore.ReadAgent(id)
	if !after.LastSeenAt.After(rec.LastSeenAt) {
		t.Fatalf("LastSeenAt did not advance: was=%v now=%v", rec.LastSeenAt, after.LastSeenAt)
	}
	if !after.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("ExpiresAt drifted: was=%v now=%v", originalExpiry, after.ExpiresAt)
	}
}

func TestHeartbeat_ExpiredSession(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "k", "n", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := s.AgentStore.ReadAgent(id)
	rec.ExpiresAt = time.Now().Add(-time.Second)
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, id); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for expired heartbeat, got %v", err)
	}
}

func TestHeartbeat_UnknownSession(t *testing.T) {
	s := freshService(t)
	if _, err := s.Heartbeat(context.Background(), "no-such-id"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetAgent_ReturnsDomainAgent(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "k", "Claude Z", map[string]string{"x": "y"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.ID != id || a.Name != "Claude Z" || a.Metadata["x"] != "y" {
		t.Fatalf("unexpected agent: %+v", a)
	}
}

func TestGetAgent_NotFound(t *testing.T) {
	s := freshService(t)
	if _, err := s.GetAgent(context.Background(), "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTouchDebounce_SecondCallSkipsRewrite(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "k", "n", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	mtimeOf := func() time.Time {
		t.Helper()
		fi, err := os.Stat(filepath.Join(s.AgentStore.Root, "agents", id+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}

	// First mutating call: should bump LastSeenAt (no entry in touchOnce yet).
	if _, err := s.touchPlaceholder(WithSessionID(ctx, id)); err != nil {
		t.Fatal(err)
	}
	first := mtimeOf()

	// Sleep just enough to make a follow-up rewrite produce a strictly
	// newer mtime if it actually happened.
	time.Sleep(20 * time.Millisecond)

	// Second mutating call within the debounce window: must NOT rewrite.
	if _, err := s.touchPlaceholder(WithSessionID(ctx, id)); err != nil {
		t.Fatal(err)
	}
	second := mtimeOf()

	if !second.Equal(first) {
		t.Fatalf("debounce failed: mtime changed within window (%v -> %v)", first, second)
	}

	// Pretend the previous touch happened over a minute ago by rewinding
	// the in-memory debounce entry; the next call should rewrite.
	s.touchMu.Lock()
	s.touchOnce[id] = time.Now().Add(-2 * time.Minute)
	s.touchMu.Unlock()

	time.Sleep(20 * time.Millisecond)
	if _, err := s.touchPlaceholder(WithSessionID(ctx, id)); err != nil {
		t.Fatal(err)
	}
	third := mtimeOf()
	if !third.After(second) {
		t.Fatalf("expected rewrite after debounce window expired (mtime %v vs %v)", third, second)
	}
}

func TestSessionIDFrom_AbsentReturnsFalse(t *testing.T) {
	if _, ok := SessionIDFrom(context.Background()); ok {
		t.Fatal("expected ok=false on bare context")
	}
	if _, ok := SessionIDFrom(WithSessionID(context.Background(), "")); ok {
		t.Fatal("empty string should not authenticate")
	}
	id, ok := SessionIDFrom(WithSessionID(context.Background(), "abc"))
	if !ok || id != "abc" {
		t.Fatalf("round-trip failed: ok=%v id=%q", ok, id)
	}
}

// Sanity check: AgentRecord.ToDomain matches what GetAgent returns for the
// same record. Guards against accidental field drift between the record
// shape and the domain.Agent shape.
func TestGetAgent_MirrorsRecord(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "k", "n", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	expected := (*store.AgentRecord)(rec).ToDomain()
	if a.ID != expected.ID || a.Key != expected.Key || a.Name != expected.Name {
		t.Fatalf("ToDomain drift: %+v vs %+v", a, expected)
	}
}
