package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cf-san/internal/buildinfo"
)

const (
	githubAPIBaseURL        = "https://api.github.com"
	githubRequestTimeout    = 15 * time.Second
	maxReleaseResponseBytes = 1 << 20
)

// Release is the stable version advertised by a release provider.
type Release struct {
	Version string
}

// ReleaseClient looks up the latest release for a repository.
type ReleaseClient interface {
	Latest(context.Context, string) (Release, error)
}

// GitHubClient queries GitHub's public Releases API without credentials.
// baseURL is intentionally private; tests in this package may replace it with
// an httptest server, while production construction always uses GitHub HTTPS.
type GitHubClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewGitHubClient returns a client configured for GitHub's HTTPS API.
func NewGitHubClient() *GitHubClient {
	return &GitHubClient{
		httpClient: &http.Client{Timeout: githubRequestTimeout},
		baseURL:    githubAPIBaseURL,
	}
}

// Latest returns the latest stable release for repository, an owner/repo slug.
func (client *GitHubClient) Latest(ctx context.Context, repository string) (Release, error) {
	owner, repo, err := repositoryParts(repository)
	if err != nil {
		return Release{}, err
	}
	if ctx == nil {
		return Release{}, errors.New("release lookup context must not be nil")
	}

	baseURL := githubAPIBaseURL
	httpClient := http.DefaultClient
	if client != nil {
		if client.baseURL != "" {
			baseURL = strings.TrimRight(client.baseURL, "/")
		}
		if client.httpClient != nil {
			httpClient = client.httpClient
		}
	}

	requestContext, cancel := context.WithTimeout(ctx, githubRequestTimeout)
	defer cancel()
	endpoint := baseURL + "/repos/" + owner + "/" + repo + "/releases/latest"
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "cf-san-agent")
	response, err := httpClient.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("GitHub release request failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Release{}, fmt.Errorf("GitHub release request returned HTTP %d", response.StatusCode)
	}
	body, err := readReleaseResponse(response.Body)
	if err != nil {
		return Release{}, err
	}

	var payload struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Release{}, fmt.Errorf("GitHub release response is invalid JSON: %w", err)
	}
	if payload.TagName == "" {
		return Release{}, errors.New("GitHub release response has an empty tag")
	}
	if payload.Draft {
		return Release{}, errors.New("GitHub release is a draft")
	}
	if payload.Prerelease {
		return Release{}, errors.New("GitHub release is a prerelease")
	}
	if _, err := buildinfo.CompareStableTags(payload.TagName, payload.TagName); err != nil {
		return Release{}, fmt.Errorf("GitHub release tag is invalid: %w", err)
	}
	return Release{Version: payload.TagName}, nil
}

func readReleaseResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxReleaseResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read GitHub release response: %w", err)
	}
	if len(body) > maxReleaseResponseBytes {
		return nil, fmt.Errorf("GitHub release response exceeds %d bytes", maxReleaseResponseBytes)
	}
	return body, nil
}

func validateRepository(repository string) error {
	if repository == "" || strings.TrimSpace(repository) != repository || strings.Count(repository, "/") != 1 {
		return errors.New("repository must be an owner/repo slug")
	}
	owner, repo, _ := strings.Cut(repository, "/")
	if !validRepositoryPart(owner) || !validRepositoryPart(repo) {
		return errors.New("repository must be an owner/repo slug")
	}
	return nil
}

func repositoryParts(repository string) (string, string, error) {
	if err := validateRepository(repository); err != nil {
		return "", "", err
	}
	owner, repo, _ := strings.Cut(repository, "/")
	return owner, repo, nil
}

func validRepositoryPart(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for _, character := range part {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
