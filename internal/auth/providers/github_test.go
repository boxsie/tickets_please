package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// stubGitHub stands up a fake token + API server and returns a GitHub provider
// wired to it.
func stubGitHub(t *testing.T, userJSON string, emailsJSON string) *GitHub {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userJSON))
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(emailsJSON))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return NewGitHub("client-id", "client-secret",
		WithEndpoint(oauth2.Endpoint{
			AuthURL:  srv.URL + "/login/oauth/authorize",
			TokenURL: srv.URL + "/login/oauth/access_token",
		}),
		WithAPIBase(srv.URL),
		WithHTTPClient(srv.Client()),
	)
}

func TestGitHub_AuthorizeURL(t *testing.T) {
	g := NewGitHub("my-client", "secret")
	got := g.AuthorizeURL("state-xyz", "https://tp.example/auth/github/callback")
	for _, want := range []string{
		"https://github.com/login/oauth/authorize",
		"client_id=my-client",
		"state=state-xyz",
		"redirect_uri=https%3A%2F%2Ftp.example%2Fauth%2Fgithub%2Fcallback",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("AuthorizeURL missing %q\n got: %s", want, got)
		}
	}
}

func TestGitHub_Exchange_PublicEmail(t *testing.T) {
	g := stubGitHub(t,
		`{"login":"boxsie","name":"Dan","email":"dan@example.com","avatar_url":"https://a/b.png"}`,
		`[]`,
	)
	claims, err := g.Exchange(context.Background(), "code", "https://tp.example/auth/github/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if claims.Provider != "github" || claims.Subject != "boxsie" {
		t.Errorf("provider/subject = %q/%q", claims.Provider, claims.Subject)
	}
	if claims.Email != "dan@example.com" || claims.DisplayName != "Dan" || claims.AvatarURL != "https://a/b.png" {
		t.Errorf("claims = %+v", claims)
	}
}

func TestGitHub_Exchange_PrivateEmailFallsBackToEmailsEndpoint(t *testing.T) {
	g := stubGitHub(t,
		`{"login":"boxsie","name":"","email":"","avatar_url":""}`,
		`[{"email":"secondary@example.com","primary":false,"verified":true},
		  {"email":"primary@example.com","primary":true,"verified":true}]`,
	)
	claims, err := g.Exchange(context.Background(), "code", "https://tp.example/auth/github/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if claims.Email != "primary@example.com" {
		t.Errorf("email = %q, want primary@example.com", claims.Email)
	}
	// name empty → falls back to login.
	if claims.DisplayName != "boxsie" {
		t.Errorf("display name = %q, want login fallback boxsie", claims.DisplayName)
	}
}
