package autoupdate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubClientLatest(t *testing.T) {
	var gotPath string
	var gotAccept string
	var gotUserAgent string
	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotUserAgent = r.Header.Get("User-Agent")
		gotAuthorization = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.3","draft":false,"prerelease":false}`)
	}))
	defer server.Close()

	client := newTestGitHubClient(server)
	release, err := client.Latest(context.Background(), "octo-org/cf-san")
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if release.Version != "v1.2.3" {
		t.Fatalf("Latest() version = %q, want %q", release.Version, "v1.2.3")
	}
	if gotPath != "/repos/octo-org/cf-san/releases/latest" {
		t.Fatalf("request path = %q, want %q", gotPath, "/repos/octo-org/cf-san/releases/latest")
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q, want %q", gotAccept, "application/vnd.github+json")
	}
	if gotUserAgent != "cf-san-agent" {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, "cf-san-agent")
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization = %q, want no Authorization header", gotAuthorization)
	}
}

func TestGitHubClientLatestDoesNotSendAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("release lookup unexpectedly sent Authorization")
		}
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.3","draft":false,"prerelease":false}`)
	}))
	defer server.Close()

	if _, err := newTestGitHubClient(server).Latest(context.Background(), "octo-org/cf-san"); err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestGitHubClientLatestRejectsNon2xx(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"not available"}`)
			}))
			defer server.Close()

			if _, err := newTestGitHubClient(server).Latest(context.Background(), "octo-org/cf-san"); err == nil {
				t.Fatalf("Latest() error = nil, want HTTP %d error", status)
			}
		})
	}
}

func TestGitHubClientLatestRejectsInvalidReleasePayload(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"tag_name":"v1.2.3"`},
		{name: "empty tag", body: `{"tag_name":"","draft":false,"prerelease":false}`},
		{name: "draft", body: `{"tag_name":"v1.2.3","draft":true,"prerelease":false}`},
		{name: "prerelease", body: `{"tag_name":"v1.2.3-rc.1","draft":false,"prerelease":true}`},
		{name: "invalid version", body: `{"tag_name":"1.2.3","draft":false,"prerelease":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			if _, err := newTestGitHubClient(server).Latest(context.Background(), "octo-org/cf-san"); err == nil {
				t.Fatal("Latest() error = nil, want invalid release error")
			}
		})
	}
}

func TestGitHubClientLatestRejectsOversizedResponse(t *testing.T) {
	body := `{"tag_name":"v1.2.3","draft":false,"prerelease":false,"body":"` + strings.Repeat("x", 2<<20) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	if _, err := newTestGitHubClient(server).Latest(context.Background(), "octo-org/cf-san"); err == nil {
		t.Fatal("Latest() error = nil, want oversized response error")
	}
}

func TestValidateRepository(t *testing.T) {
	for _, repository := range []string{
		"",
		"octo-org",
		"/cf-san",
		"octo-org/",
		"octo-org/cf-san/extra",
		"octo-org//cf-san",
		" octo-org/cf-san",
		"octo-org/cf-san ",
		"octo?org/cf-san",
		"octo-org/cf?san",
		"octo-org\\cf-san",
		"../cf-san",
	} {
		t.Run("rejects "+repository, func(t *testing.T) {
			if err := validateRepository(repository); err == nil {
				t.Fatalf("validateRepository(%q) error = nil, want error", repository)
			}
		})
	}

	for _, repository := range []string{
		"octo-org/cf-san",
		"owner.name/repo_name",
		"123/repo-1",
	} {
		t.Run("accepts "+repository, func(t *testing.T) {
			if err := validateRepository(repository); err != nil {
				t.Fatalf("validateRepository(%q) error = %v, want nil", repository, err)
			}
		})
	}
}

func TestNewGitHubClientUsesFixedAPIAndFiniteTimeout(t *testing.T) {
	client := NewGitHubClient()
	if client == nil {
		t.Fatal("NewGitHubClient() = nil")
	}
	if client.baseURL != githubAPIBaseURL {
		t.Fatalf("GitHub API base URL = %q, want %q", client.baseURL, githubAPIBaseURL)
	}
	if client.httpClient == nil || client.httpClient.Timeout <= 0 {
		t.Fatal("GitHub client must use a finite HTTP timeout")
	}
}

func newTestGitHubClient(server *httptest.Server) *GitHubClient {
	client := NewGitHubClient()
	client.baseURL = server.URL
	client.httpClient = server.Client()
	return client
}
