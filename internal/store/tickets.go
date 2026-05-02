package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// TicketWalkFunc is the per-ticket callback used by WalkTickets. ticketDir is
// the absolute path of the ticket's directory; phaseDirName is the on-disk
// `<NNN>-<phase-slug>` folder name when the ticket lives inside a phase
// (empty string for phase-less tickets).
type TicketWalkFunc func(ticketDir, phaseDirName string, rec *TicketRecord) error

// WalkTickets iterates every ticket inside a project — both phase-less
// (`projects/<slug>/tickets/*`) and phased (`projects/<slug>/phases/*/tickets/*`).
// Within each tree iteration is by directory name (which encodes the
// number-prefix and so corresponds to creation order). Phase-less tickets
// come first, then each phase's tickets in phase-dir-name order.
func (s *Store) WalkTickets(slug string, fn TicketWalkFunc) error {
	// 1. Phase-less tickets.
	root := filepath.Join(s.projectDir(slug), dirTickets)
	if err := walkTicketsInDir(root, "", fn); err != nil {
		return err
	}

	// 2. Phased tickets.
	phasesRoot := filepath.Join(s.projectDir(slug), dirPhases)
	phaseEntries, err := os.ReadDir(phasesRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read phases dir: %w", err)
	}
	phaseNames := make([]string, 0, len(phaseEntries))
	for _, e := range phaseEntries {
		if !e.IsDir() {
			continue
		}
		phaseNames = append(phaseNames, e.Name())
	}
	sort.Strings(phaseNames)
	for _, name := range phaseNames {
		dir := filepath.Join(phasesRoot, name, dirTickets)
		if err := walkTicketsInDir(dir, name, fn); err != nil {
			return err
		}
	}
	return nil
}

// walkTicketsInDir iterates ticket subdirs at the given root, calling fn for
// each `<root>/<NNN>-<slug>/ticket.yaml` it finds.
func walkTicketsInDir(root, phaseDirName string, fn TicketWalkFunc) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read tickets dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		ticketDir := filepath.Join(root, name)
		path := filepath.Join(ticketDir, fileTicket)
		rec := &TicketRecord{}
		if err := ReadYAML(path, rec); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if err := fn(ticketDir, phaseDirName, rec); err != nil {
			return err
		}
	}
	return nil
}
