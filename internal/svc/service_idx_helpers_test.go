package svc

import "tickets_please/internal/vecindex"

// Test-only aggregate views over per-mount indexes + defaultIndexes. The
// production code (W2-T1 onward) reaches for mountProviderAndIndex or
// WalkProjectMounts; these wrappers exist purely so the legacy single-index
// tests can keep asserting on entries without each one stitching together
// the per-mount + fallback walk.

func (s *Service) testSummaryEntries() []vecindex.Entry  { return s.collectEntries(indexKindSummaries) }
func (s *Service) testTicketEntries() []vecindex.Entry   { return s.collectEntries(indexKindTickets) }
func (s *Service) testLearningEntries() []vecindex.Entry { return s.collectEntries(indexKindLearnings) }
func (s *Service) testCommentEntries() []vecindex.Entry  { return s.collectEntries(indexKindComments) }

func (s *Service) testSummaryLen() int  { return len(s.testSummaryEntries()) }
func (s *Service) testTicketLen() int   { return len(s.testTicketEntries()) }
func (s *Service) testLearningLen() int { return len(s.testLearningEntries()) }
func (s *Service) testCommentLen() int  { return len(s.testCommentEntries()) }

func (s *Service) collectEntries(kind indexKind) []vecindex.Entry {
	var out []vecindex.Entry
	collect := func(src *vecindex.Index) {
		if src == nil {
			return
		}
		out = append(out, src.Snapshot()...)
	}
	s.mountsMu.Lock()
	for _, m := range s.projectMounts {
		if m == nil {
			continue
		}
		switch kind {
		case indexKindSummaries:
			collect(m.SummaryIdx)
		case indexKindTickets:
			collect(m.TicketsIdx)
		case indexKindLearnings:
			collect(m.LearningsIdx)
		case indexKindComments:
			collect(m.CommentsIdx)
		}
	}
	s.mountsMu.Unlock()
	switch kind {
	case indexKindSummaries:
		collect(s.defaultIndexes.Summaries)
	case indexKindTickets:
		collect(s.defaultIndexes.Tickets)
	case indexKindLearnings:
		collect(s.defaultIndexes.Learnings)
	case indexKindComments:
		collect(s.defaultIndexes.Comments)
	}
	return out
}
