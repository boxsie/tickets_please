package domain

import (
	"strings"
	"time"
)

// EntryKind discriminates the three searchable kinds a feedback record can
// reference. The kind lives in the entry key (`<kind>:<id>`), so a learning
// and its parent ticket — which share the same UUID — don't collide.
type EntryKind string

const (
	EntryKindTicket   EntryKind = "ticket"
	EntryKindLearning EntryKind = "learning"
	EntryKindComment  EntryKind = "comment"
)

// EntryKey is the stable string form `<kind>:<id>` used to key every per-
// result feedback aggregate. Kept as a defined string type (not a struct) so
// it round-trips through YAML map keys and JSON payloads without any custom
// marshaling.
type EntryKey string

// TicketEntryKey returns the canonical entry key for a ticket id.
func TicketEntryKey(ticketID string) EntryKey {
	return EntryKey(string(EntryKindTicket) + ":" + ticketID)
}

// LearningEntryKey returns the canonical entry key for the completion-
// learnings entry of a ticket. Learnings are 1:1 with the parent ticket, so
// the id is the parent ticket id; the kind discriminator distinguishes the
// two records in the feedback store.
func LearningEntryKey(ticketID string) EntryKey {
	return EntryKey(string(EntryKindLearning) + ":" + ticketID)
}

// CommentEntryKey returns the canonical entry key for a comment id.
func CommentEntryKey(commentID string) EntryKey {
	return EntryKey(string(EntryKindComment) + ":" + commentID)
}

// ParseEntryKey splits a key into its kind and id components. Returns ok=false
// for any malformed key, including one with an unknown kind.
func ParseEntryKey(s string) (kind EntryKind, id string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	k := EntryKind(s[:i])
	switch k {
	case EntryKindTicket, EntryKindLearning, EntryKindComment:
		return k, s[i+1:], true
	}
	return "", "", false
}

// Rating is the polarity of a single feedback call. Only two values are
// accepted; anything else fails validation at the MCP boundary.
type Rating string

const (
	RatingLike    Rating = "like"
	RatingDislike Rating = "dislike"
)

// FeedbackRecord is the per-entry aggregate the store persists. Counters are
// monotonically increasing under normal operation; the `Reasons` slice is
// append-only with the oldest evicted when capacity is hit.
type FeedbackRecord struct {
	Likes          int
	Dislikes       int
	LastFeedbackAt time.Time
	LastUsedAt     time.Time
	Retrievals     int
	Reasons        []string
}
