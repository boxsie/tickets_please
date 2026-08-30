package worker

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/embed"
)

// erroringProvider always fails, like ollama 500ing on an input bge-m3 can't
// embed. failN>0 makes it fail only the first N calls, to exercise retry.
type erroringProvider struct {
	inner  *fakeProvider
	failN  int
	calls  int
	errMsg string
}

func (p *erroringProvider) Name() string                  { return "erroring" }
func (p *erroringProvider) Dim() int                      { return 768 }
func (p *erroringProvider) Probe(_ context.Context) error { return nil }
func (p *erroringProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	p.calls++
	if p.failN == 0 || p.calls <= p.failN {
		msg := p.errMsg
		if msg == "" {
			msg = "ollama: status 500: failed to encode response: json: unsupported value: NaN"
		}
		return nil, errors.New(msg)
	}
	return p.inner.Embed(ctx, text)
}

// nanProvider returns a well-formed-looking vector with a NaN buried in it —
// a provider that answers 200 but hands back poison.
type nanProvider struct{ inner *fakeProvider }

func (p *nanProvider) Name() string                  { return "nan" }
func (p *nanProvider) Dim() int                      { return 768 }
func (p *nanProvider) Probe(_ context.Context) error { return nil }
func (p *nanProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, err := p.inner.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	vec[42] = float32(math.NaN())
	return vec, nil
}

// seedSource writes a source file and returns (sourcePath, sidecarPath).
func seedSource(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, name+".md")
	if err := os.WriteFile(src, []byte("some text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, filepath.Join(dir, name+".embedding.json")
}

// TestEmbedFailure_LeavesDiscoverableMarker is the ticket-150 regression pin:
// a failed embed used to log a warning and vanish, leaving the entry absent
// from search with nothing on disk to say so.
func TestEmbedFailure_LeavesDiscoverableMarker(t *testing.T) {
	src, sidecar := seedSource(t, "learnings")
	idx := freshIndexes()
	p := &erroringProvider{inner: newFake()}
	w := New(context.Background(), p, "fake-model", idx, 16, silentLogger())
	defer w.Stop(context.Background())

	w.Enqueue(Job{
		Kind:        JobTicketLearnings,
		SourcePath:  src,
		SidecarPath: sidecar,
		EntryID:     "t-1",
		Owner:       "alpha",
		Text:        "the text that cannot be embedded",
	})

	marker := markerPathFor(sidecar)
	if !waitFor(3*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatalf("no failure marker at %s after a failed embed", marker)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var rec FailureRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("marker is not valid json: %v\n%s", err, data)
	}
	if rec.EntryID != "t-1" {
		t.Errorf("marker entry_id = %q, want t-1", rec.EntryID)
	}
	if rec.Kind != "ticket_learnings" {
		t.Errorf("marker kind = %q, want ticket_learnings", rec.Kind)
	}
	if rec.Error == "" {
		t.Error("marker records no error message")
	}
	if rec.Attempts != embedRetries {
		t.Errorf("marker attempts = %d, want %d", rec.Attempts, embedRetries)
	}

	// No sidecar, and nothing in the index — the hole is real, just visible.
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Errorf("sidecar written despite failure: %v", err)
	}
	if n := idx.Learnings.Len(); n != 0 {
		t.Errorf("learnings index has %d entries; want 0", n)
	}
}

// TestEmbedFailure_RetriesThenSucceeds: a transient backend blip shouldn't
// cost the entry its embedding, and shouldn't leave a marker behind.
func TestEmbedFailure_RetriesThenSucceeds(t *testing.T) {
	src, sidecar := seedSource(t, "body")
	idx := freshIndexes()
	p := &erroringProvider{inner: newFake(), failN: 1}
	w := New(context.Background(), p, "fake-model", idx, 16, silentLogger())
	defer w.Stop(context.Background())

	w.Enqueue(Job{
		Kind:        JobTicketBody,
		SourcePath:  src,
		SidecarPath: sidecar,
		EntryID:     "t-2",
		Owner:       "alpha",
		Text:        "recoverable",
	})

	if !waitFor(3*time.Second, func() bool { return idx.Tickets.Len() == 1 }) {
		t.Fatalf("entry never indexed after a transient failure (calls=%d)", p.calls)
	}
	if p.calls < 2 {
		t.Errorf("provider called %d times; expected a retry", p.calls)
	}
	if _, err := os.Stat(markerPathFor(sidecar)); !os.IsNotExist(err) {
		t.Errorf("marker left behind after eventual success: %v", err)
	}
}

// TestEmbedNaN_NeverReachesTheIndex covers the provider that answers 200 with
// poison in the payload. A NaN in the index doesn't merely fail to match —
// cosine against it returns NaN, which loses every ordering comparison and
// can distort ranking for entries that are perfectly fine.
func TestEmbedNaN_NeverReachesTheIndex(t *testing.T) {
	src, sidecar := seedSource(t, "learnings")
	idx := freshIndexes()
	w := New(context.Background(), &nanProvider{inner: newFake()}, "fake-model", idx, 16, silentLogger())
	defer w.Stop(context.Background())

	w.Enqueue(Job{
		Kind:        JobTicketLearnings,
		SourcePath:  src,
		SidecarPath: sidecar,
		EntryID:     "t-3",
		Owner:       "alpha",
		Text:        "poisoned",
	})

	marker := markerPathFor(sidecar)
	if !waitFor(3*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatalf("NaN vector produced no failure marker at %s", marker)
	}
	if n := idx.Learnings.Len(); n != 0 {
		t.Errorf("NaN vector reached the index (%d entries)", n)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Error("NaN vector was written to a sidecar")
	}
}

// TestValidateVector covers the boundary helper directly.
func TestValidateVector(t *testing.T) {
	good := []float32{0.1, -0.2, 0.3}
	if err := embed.ValidateVector("p", good); err != nil {
		t.Errorf("good vector rejected: %v", err)
	}
	for name, vec := range map[string][]float32{
		"empty":      {},
		"nil":        nil,
		"nan":        {0.1, float32(math.NaN()), 0.3},
		"+inf":       {0.1, float32(math.Inf(1))},
		"-inf":       {float32(math.Inf(-1))},
		"nan at end": {0.1, 0.2, float32(math.NaN())},
	} {
		if err := embed.ValidateVector("p", vec); err == nil {
			t.Errorf("%s vector accepted", name)
		}
	}
}
