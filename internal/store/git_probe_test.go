package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
)

func TestRepositoryProbeMatchesCommitOpener(t *testing.T) {
	for _, shape := range []string{"repository", "empty-directory", "malformed-file"} {
		t.Run(shape, func(t *testing.T) {
			outer := t.TempDir()
			if _, err := git.PlainInit(outer, false); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(outer, "nested")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "repository":
				if _, err := git.PlainInit(root, false); err != nil {
					t.Fatal(err)
				}
			case "empty-directory":
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "malformed-file":
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir pointer\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			data := filepath.Join(root, "data")
			if err := os.Mkdir(data, 0o700); err != nil {
				t.Fatal(err)
			}
			want := shape == "repository"
			if got := isInGitRepo(data); got != want {
				t.Fatalf("probe=%v, want %v", got, want)
			}
			_, found, err := openRepoFor(data)
			if (err == nil) != want {
				t.Fatalf("opener root=%s err=%v, want repository=%v", found, err, want)
			}
			if want && found != root {
				t.Fatalf("opened %s, want nearest %s", found, root)
			}
		})
	}
}
