package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"tickets_please/internal/config"
	"tickets_please/internal/store"
)

// runCheck walks the data tree and reports integrity findings, exiting
// non-zero if any are fatal.
//
// The walk itself has existed (and been tested) since the store was written;
// this subcommand was a stub that logged "not implemented yet" — so the one
// command a human would reach for to ask "is anything wrong in my store?"
// answered nothing, no matter how much was wrong.
//
// Output is deliberately plain lines on stdout rather than the structured
// logger: this is a report a person reads, and burying findings in JSON log
// records is how you get a check nobody runs.
func runCheck(cfg config.Config, logger *slog.Logger) error {
	st, err := store.New(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	// Agent-ref validation needs the central agent store. Its absence isn't
	// fatal — the rest of the walk is still worth running — so degrade to a
	// note rather than refusing to check anything at all.
	var agentSt *store.AgentStore
	if cfg.DataRoot != "" {
		as, aerr := store.NewAgentStore(cfg.DataRoot, cfg.LockTimeoutSeconds)
		if aerr != nil {
			fmt.Printf("note: agent references not validated (%v)\n", aerr)
		} else {
			agentSt = as
		}
	}

	var warnings []store.Warning
	var fatal []store.FatalError
	if agentSt != nil {
		warnings, fatal, err = st.Integrity(context.Background(), agentSt)
	} else {
		warnings, fatal, err = st.Integrity(context.Background())
	}
	if err != nil {
		return fmt.Errorf("integrity walk: %w", err)
	}

	fmt.Printf("checked %s\n", st.Root)
	for _, w := range warnings {
		fmt.Printf("  WARN  %s\n", w.String())
	}
	for _, f := range fatal {
		fmt.Printf("  FATAL %s\n", f.String())
	}

	switch {
	case len(fatal) > 0:
		fmt.Printf("\n%d fatal, %d warning(s)\n", len(fatal), len(warnings))
		os.Exit(1)
	case len(warnings) > 0:
		fmt.Printf("\nno fatal errors, %d warning(s)\n", len(warnings))
	default:
		fmt.Println("\nno problems found")
	}
	_ = logger
	return nil
}
