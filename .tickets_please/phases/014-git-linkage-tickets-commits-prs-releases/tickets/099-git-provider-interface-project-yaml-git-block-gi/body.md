Foundation. Defines the abstraction every other ticket in this phase calls into. Without this, the indexers couldn't work for both local and remote deployments.

## Acceptance

- `internal/git/provider.go` defines:
  ```go
  type Provider interface {
      WalkCommits(ctx, since *time.Time) iter.Seq2[*Commit, error]
      ListBranches(ctx) ([]Branch, error)
      ListPullRequests(ctx, state PRState) ([]PullRequest, error)
      ListReleases(ctx) ([]Release, error)
      CommitsBetween(ctx, fromSHA, toSHA string) ([]*Commit, error)
  }
  ```
- `internal/git/providers/local.go`: shells out to `git` against a working tree path. Used when `serve` mode runs in the same dir as the project.
- `internal/git/providers/github.go`: uses `github.com/google/go-github/v68` against the GitHub API. Takes an OAuth-supplied access token (from the acting user's `User.GitHubToken`).
- `project.yaml` gains a `git` block:
  ```yaml
  git:
    provider: github          # or "local"; "auto" picks local if .git visible, else github
    remote: owner/repo        # required for github provider
    default_branch: main
  ```
- `git.ProviderFor(project, user)` factory picks the right impl based on config + available context.
- Tests cover both impls with a fixture repo (local) and a recorded-cassette (github via go-vcr or similar).

## Hints

- `git` package independent of `web` / `svc` so it's reusable from CLI subcommands too.
- Use `iter.Seq2` for `WalkCommits` since large repos have millions of commits — never materialise the whole list.
