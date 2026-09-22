// Package gitlab implements forge.Forge for a GitLab instance. It exposes two
// backends:
//
//   - REST  (xanzy/go-gitlab): writes (approve/merge/comment) and diff payloads,
//     where the endpoints are stable and well documented.
//   - GraphQL: read/list-heavy views (MR list, MR detail), where a single query
//     replaces the REST N+1 round trips.
//
// Each view picks the cheaper backend; both share one token and host.
package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gogitlab "github.com/xanzy/go-gitlab"

	"github.com/hamkens/glx/internal/cache"
	"github.com/hamkens/glx/internal/forge"
)

// cacheTTL is how long read results stay fresh before a background refetch.
const cacheTTL = 30 * time.Second

// Client is the dual-backend GitLab client. It implements forge.Forge.
type Client struct {
	host     string
	token    string
	username string // authenticated user, set by CurrentUser
	rest     *gogitlab.Client
	http     *http.Client
	gqlURL   string
	restURL  string

	// Per-read-type TTL caches keep the TUI snappy on view re-entry.
	mrListCache   *cache.Cache[string, *forge.ChangePage]
	mrDetailCache *cache.Cache[string, *forge.ChangeDetail]
	mrDiffCache   *cache.Cache[string, *forge.Diff]
}

// Compile-time check that the client satisfies the provider-neutral contract.
var _ forge.Forge = (*Client)(nil)

// New constructs a Client for the given host and token.
func New(host, token string) (*Client, error) {
	baseURL := fmt.Sprintf("https://%s/api/v4", host)
	rest, err := gogitlab.NewClient(token, gogitlab.WithBaseURL(baseURL))
	if err != nil {
		return nil, fmt.Errorf("init REST client: %w", err)
	}
	return &Client{
		host:          host,
		token:         token,
		rest:          rest,
		http:          &http.Client{Timeout: 30 * time.Second},
		gqlURL:        fmt.Sprintf("https://%s/api/graphql", host),
		restURL:       baseURL,
		mrListCache:   cache.New[string, *forge.ChangePage](cacheTTL),
		mrDetailCache: cache.New[string, *forge.ChangeDetail](cacheTTL),
		mrDiffCache:   cache.New[string, *forge.Diff](cacheTTL),
	}, nil
}

// Provider identifies this backend.
func (c *Client) Provider() forge.Provider { return forge.ProviderGitLab }

// Capabilities reports what this backend supports. GitLab supports the full
// action set glx exposes.
func (c *Client) Capabilities() forge.Capabilities {
	return forge.Capabilities{
		AutoMerge:          true,
		Unapprove:          true,
		CancelJobIsRunWide: false,
		DraftToggle:        true,
	}
}

// CurrentUser identifies the authenticated account and the instance version.
// It uses GraphQL to validate that backend in one call during startup.
func (c *Client) CurrentUser(ctx context.Context) (username, version string, err error) {
	var resp struct {
		Metadata struct {
			Version string `json:"version"`
		} `json:"metadata"`
		CurrentUser struct {
			Username string `json:"username"`
		} `json:"currentUser"`
	}
	const q = `{ metadata { version } currentUser { username } }`
	if err := c.graphQL(ctx, q, nil, &resp); err != nil {
		return "", "", err
	}
	if resp.CurrentUser.Username == "" {
		return "", "", fmt.Errorf("authenticated request returned no user (token invalid or lacks api scope)")
	}
	c.username = resp.CurrentUser.Username
	return resp.CurrentUser.Username, "GitLab " + resp.Metadata.Version, nil
}

// Username returns the authenticated user's username, or "" if CurrentUser has
// not yet been called.
func (c *Client) Username() string { return c.username }

// Host returns the configured GitLab host.
func (c *Client) Host() string { return c.host }

// DiffURL is the web URL for a merge request's diffs tab.
func (c *Client) DiffURL(projectPath, iid string) string {
	return fmt.Sprintf("https://%s/%s/-/merge_requests/%s/diffs", c.host, projectPath, iid)
}
