package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FailureMarkerSuffix replaces the `.json` of a sidecar path to name the
// marker written when an entry could not be embedded. Kept as an exported
// constant because `tickets_please check` scans for it — the marker is a
// contract between the worker and the integrity walk, not a private detail.
const FailureMarkerSuffix = ".failed"

// embedRetries is how many attempts a single job gets. Deliberately small:
// this covers a backend blipping (connection refused mid-restart, a timeout
// under load), not a bad input. A model that emits NaN for a given string
// emits it every time, so retrying that is pure latency — which is exactly
// why the durable marker below matters more than the retry does.
const embedRetries = 3

// embedRetryBackoff is the pause before attempt N+1.
var embedRetryBackoff = []time.Duration{200 * time.Millisecond, 800 * time.Millisecond}

// FailureRecord is the content of a `.failed` marker: enough to tell a human
// (or `check`) what went wrong and whether it's worth retrying.
type FailureRecord struct {
	EntryID  string `json:"entry_id"`
	Kind     string `json:"kind"`
	Source   string `json:"source,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
	FailedAt string `json:"failed_at"`
}

// embedWithRetry calls the provider, retrying a bounded number of times with
// a short backoff. Context cancellation short-circuits immediately — a
// shutdown shouldn't spend seconds sleeping between doomed attempts.
func (w *Worker) embedWithRetry(ctx context.Context, j Job) ([]float32, error) {
	var lastErr error
	for attempt := 0; attempt < embedRetries; attempt++ {
		if attempt > 0 {
			d := embedRetryBackoff[min(attempt-1, len(embedRetryBackoff)-1)]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		vec, err := w.provider.Embed(ctx, j.Text)
		if err == nil {
			if attempt > 0 {
				w.log.Info("embed succeeded on retry",
					"attempt", attempt+1, "kind", j.Kind, "entry_id", j.EntryID)
			}
			return vec, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", embedRetries, lastErr)
}

// markerPathFor derives the marker path from the job's sidecar path. Returns
// "" when the job has no sidecar (nothing on disk to annotate).
func markerPathFor(sidecarPath string) string {
	if sidecarPath == "" {
		return ""
	}
	return strings.TrimSuffix(sidecarPath, ".json") + FailureMarkerSuffix
}

// writeFailureMarker records a permanent-looking embed failure next to where
// the sidecar would have gone. Best-effort: if we can't even write the marker
// there's nothing further to escalate to, so it warn-logs and moves on.
func (w *Worker) writeFailureMarker(j Job, cause error) {
	path := markerPathFor(j.SidecarPath)
	if path == "" {
		return
	}
	rec := FailureRecord{
		EntryID:  j.EntryID,
		Kind:     jobKindName(j.Kind),
		Source:   j.SourcePath,
		Provider: w.provider.Name(),
		Model:    w.model,
		Error:    cause.Error(),
		Attempts: embedRetries,
		FailedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		w.log.Warn("marshal embed failure marker", "err", err, "entry_id", j.EntryID)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.log.Warn("mkdir for embed failure marker", "err", err, "path", path)
		return
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		w.log.Warn("write embed failure marker", "err", err, "path", path)
	}
}

// clearFailureMarker removes a stale marker after a later attempt succeeded.
func (w *Worker) clearFailureMarker(j Job) {
	path := markerPathFor(j.SidecarPath)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		w.log.Warn("remove stale embed failure marker", "err", err, "path", path)
	}
}

// jobKindName renders a JobKind for the marker file. Unknown kinds stringify
// numerically rather than silently reading as a valid kind.
func jobKindName(k JobKind) string {
	switch k {
	case JobProjectSummary:
		return "project_summary"
	case JobTicketBody:
		return "ticket_body"
	case JobTicketLearnings:
		return "ticket_learnings"
	case JobComment:
		return "comment"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}
