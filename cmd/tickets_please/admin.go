package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// adminStores builds the central UserStore + MembershipStore the CLI recovery
// commands write to. It mirrors svc.newServiceCore's data-root resolution so
// the CLI touches exactly the same files the running server does — DataRoot if
// set, otherwise the "<DataDir>-central" sibling fallback tests use.
func adminStores(cfg config.Config) (*store.UserStore, *store.MembershipStore, error) {
	dataRoot := cfg.DataRoot
	if dataRoot == "" {
		dataRoot = cfg.DataDir + "-central"
	}
	us, err := store.NewUserStore(dataRoot, cfg.LockTimeoutSeconds)
	if err != nil {
		return nil, nil, fmt.Errorf("open user store: %w", err)
	}
	ms, err := store.NewMembershipStore(dataRoot, cfg.LockTimeoutSeconds)
	if err != nil {
		return nil, nil, fmt.Errorf("open membership store: %w", err)
	}
	return us, ms, nil
}

// resolveProject finds a project by id or slug in the central store. Returns
// the canonical record so callers can grant by the stable ID regardless of
// which form the operator typed. Building store.New is cheap — it never probes
// the embedding provider, so the recovery CLI works offline.
func resolveProject(cfg config.Config, idOrSlug string) (*store.ProjectRecord, error) {
	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("open project store: %w", err)
	}
	var found *store.ProjectRecord
	err = st.WalkProjects(func(_ string, rec *store.ProjectRecord) error {
		if rec.ID == idOrSlug || rec.Slug == idOrSlug {
			r := *rec
			found = &r
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk projects: %w", err)
	}
	if found == nil {
		return nil, fmt.Errorf("no project matching %q (try `tickets_please list-memberships` after a `list-users` to find ids)", idOrSlug)
	}
	return found, nil
}

// runGrantOwner is the lock-out recovery path: grant a user a role (owner by
// default) on a project directly via the membership store, bypassing HTTP +
// OAuth. The use case is "I locked myself out / the bootstrap never fired".
func runGrantOwner(args []string, cfg config.Config, log *slog.Logger) error {
	fs := flag.NewFlagSet("grant-owner", flag.ContinueOnError)
	userID := fs.String("user-id", "", "user id to grant (see `list-users`)")
	project := fs.String("project", "", "project id or slug to grant on")
	roleStr := fs.String("role", string(domain.RoleOwner), "role to grant: owner|member|viewer")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse grant-owner flags: %w", err)
	}
	if *userID == "" || *project == "" {
		return errors.New("grant-owner requires --user-id and --project")
	}
	role := domain.Role(*roleStr)
	switch role {
	case domain.RoleOwner, domain.RoleMember, domain.RoleViewer:
	default:
		return fmt.Errorf("invalid --role %q (want owner|member|viewer)", *roleStr)
	}

	us, ms, err := adminStores(cfg)
	if err != nil {
		return err
	}
	if _, err := us.ReadUser(*userID); err != nil {
		return fmt.Errorf("user %s not found: %w", *userID, err)
	}
	proj, err := resolveProject(cfg, *project)
	if err != nil {
		return err
	}

	rec, err := ms.GrantMembership(context.Background(), &store.MembershipRecord{
		UserID:    *userID,
		ProjectID: proj.ID,
		Role:      role,
		GrantedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("grant membership: %w", err)
	}
	log.Info("granted membership",
		"user_id", *userID, "project", proj.Slug, "project_id", proj.ID, "role", rec.Role)
	fmt.Printf("granted %s to user %s on project %s (%s)\n", rec.Role, *userID, proj.Slug, proj.ID)
	return nil
}

// runListUsers prints the central user registry as a table — the audit
// companion to grant-owner (you need the user id before you can grant).
func runListUsers(_ []string, cfg config.Config, _ *slog.Logger) error {
	us, _, err := adminStores(cfg)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDISPLAY\tEMAIL\tGITHUB\tGOOGLE\tLAST_LOGIN")
	n := 0
	err = us.WalkUsers(func(rec *store.UserRecord) error {
		n++
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			rec.ID, dash(rec.DisplayName), dash(rec.Email),
			derefDash(rec.GitHubLogin), derefDash(rec.GoogleSub),
			rec.LastLoginAt.Format(time.RFC3339))
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk users: %w", err)
	}
	_ = tw.Flush()
	if n == 0 {
		fmt.Println("(no users yet)")
	}
	return nil
}

// runListMemberships prints every membership on a project, resolving each user
// id to a display name for readability.
func runListMemberships(args []string, cfg config.Config, _ *slog.Logger) error {
	fs := flag.NewFlagSet("list-memberships", flag.ContinueOnError)
	project := fs.String("project", "", "project id or slug")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse list-memberships flags: %w", err)
	}
	if *project == "" {
		return errors.New("list-memberships requires --project")
	}
	us, ms, err := adminStores(cfg)
	if err != nil {
		return err
	}
	proj, err := resolveProject(cfg, *project)
	if err != nil {
		return err
	}
	members, err := ms.ListMembersOfProject(proj.ID)
	if err != nil {
		return fmt.Errorf("list members: %w", err)
	}

	fmt.Printf("project %s (%s)\n", proj.Slug, proj.ID)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "USER_ID\tDISPLAY\tROLE\tGRANTED_BY\tGRANTED_AT")
	for _, m := range members {
		display := "?"
		if u, err := us.ReadUser(m.UserID); err == nil {
			display = dash(u.DisplayName)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			m.UserID, display, m.Role, dash(m.GrantedBy)+grantedBySuffix(m.GrantedBy),
			m.GrantedAt.Format(time.RFC3339))
	}
	_ = tw.Flush()
	if len(members) == 0 {
		fmt.Println("(no members)")
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func derefDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

// grantedBySuffix annotates a system grant (empty GrantedBy) so the audit
// output makes clear it wasn't a human action.
func grantedBySuffix(grantedBy string) string {
	if grantedBy == "" {
		return " (system)"
	}
	return ""
}
