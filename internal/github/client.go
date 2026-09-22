// Package github implements forge.Forge for GitHub.com and GitHub Enterprise
// Server. It mirrors the internal/gitlab split across two backends:
//
//   - GraphQL: read/list-heavy views (PR list, PR detail) plus the handful of
//     mutations REST does not expose (auto-merge, merge queue, draft toggle),
//     where one query replaces the REST N+1 round trips.
//   - REST (google/go-github): the remaining writes (approve, merge, comment),
//     diff payloads, and Actions workflow runs.
//
// Each view picks the cheaper backend; both share one token and host.
package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/hamkens/glx/internal/cache"
	"github.com/hamkens/glx/internal/forge"
)

// cacheTTL is how long read results stay fresh before a background refetch.
const cacheTTL = 30 * time.Second

// DotCom is the host name of the public GitHub instance.
const DotCom = "github.com"

// Client is the dual-backend GitHub client. It implements forge.Forge.
type Client struct {
	host     string
	token    string
	username string // authenticated user, set by CurrentUser
	rest     *gogithub.Client
	http     *http.Client
	gqlURL   string

	// Per-read-type TTL caches keep the TUI snappy on view re-entry.
	prListCache   *cache.Cache[string, *forge.ChangePage]
	prDetailCache *cache.Cache[string, *forge.ChangeDetail]
	prDiffCache   *cache.Cache[string, *forge.Diff]
}

// Compile-time check that the client satisfies the provider-neutral contract.
var _ forge.Forge = (*Client)(nil)

// New constructs a Client for the given host and token. host is "github.com"
// for the public instance, or the host name of a GitHub Enterprise Server
// install, whose API lives under /api/v3 rather than on a separate domain.
func New(host, token string) (*Client, error) {
	if host == "" {
		host = DotCom
	}
	httpClient := &http.Client{Timeout: 60 * time.Second}

	rest := gogithub.NewClient(nil).WithAuthToken(token)
	gqlURL := "https://api.github.com/graphql"
	if !isDotCom(host) {
		var err error
		rest, err = rest.WithEnterpriseURLs(
			fmt.Sprintf("https://%s/api/v3/", host),
			fmt.Sprintf("https://%s/api/uploads/", host),
		)
		if err != nil {
			return nil, fmt.Errorf("init REST client: %w", err)
		}
		gqlURL = fmt.Sprintf("https://%s/api/graphql", host)
	}

	return &Client{
		host:          host,
		token:         token,
		rest:          rest,
		http:          httpClient,
		gqlURL:        gqlURL,
		prListCache:   cache.New[string, *forge.ChangePage](cacheTTL),
		prDetailCache: cache.New[string, *forge.ChangeDetail](cacheTTL),
		prDiffCache:   cache.New[string, *forge.Diff](cacheTTL),
	}, nil
}

// isDotCom reports whether host is the public instance. "api.github.com" is
// accepted so a user who copies the API host into config still lands here.
func isDotCom(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == DotCom || h == "api."+DotCom || h == ""
}

// Provider identifies this backend.
func (c *Client) Provider() forge.Provider { return forge.ProviderGitHub }

// Capabilities reports what this backend supports. Unlike GitLab, GitHub has no
// per-job cancel: canceling a job cancels the whole workflow run.
func (c *Client) Capabilities() forge.Capabilities {
	return forge.Capabilities{
		AutoMerge:          true,
		Unapprove:          true,
		CancelJobIsRunWide: true,
		DraftToggle:        true,
	}
}

// CurrentUser identifies the authenticated account and the instance version.
// GitHub Enterprise reports its version in a response header; github.com does
// not version itself, so it reports a plain product name.
func (c *Client) CurrentUser(ctx context.Context) (username, version string, err error) {
	user, resp, err := c.rest.Users.Get(ctx, "")
	if err != nil {
		return "", "", fmt.Errorf("authenticate: %w", err)
	}
	if user.GetLogin() == "" {
		return "", "", fmt.Errorf("authenticated request returned no user (token invalid or lacks repo scope)")
	}
	c.username = user.GetLogin()

	version = "GitHub.com"
	if !isDotCom(c.host) {
		version = "GitHub Enterprise"
		if resp != nil && resp.Response != nil {
			if v := resp.Header.Get("X-GitHub-Enterprise-Version"); v != "" {
				version += " " + v
			}
		}
	}
	return c.username, version, nil
}

// Username returns the authenticated user's login, or "" if CurrentUser has not
// yet been called.
func (c *Client) Username() string { return c.username }

// Host returns the configured GitHub host.
func (c *Client) Host() string { return c.host }

// DiffURL is the web URL for a pull request's files tab.
func (c *Client) DiffURL(repo, number string) string {
	return fmt.Sprintf("https://%s/%s/pull/%s/files", c.host, repo, number)
}

// splitRepo splits an "owner/repo" path into its parts.
func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("invalid repository %q: want owner/repo", repo)
	}
	return owner, name, nil
}

// invalidatePR drops cached reads for a PR after a mutating action so the next
// read (and the auto-refetch the TUI triggers) returns fresh state.
func (c *Client) invalidatePR(repo, number string) {
	key := repo + "#" + number
	c.prDetailCache.Invalidate(key)
	c.prDiffCache.Invalidate(key)
	// PR list rows (review/check glyphs) may also change; clear all scopes.
	for _, s := range []forge.Scope{forge.ScopeAssigned, forge.ScopeReviewer, forge.ScopeAuthored} {
		c.prListCache.Invalidate(string(s))
	}
}
