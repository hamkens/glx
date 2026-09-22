package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/hamkens/glx/internal/forge"
)

// graphQL executes a GraphQL query against the instance and unmarshals the
// "data" field into out. variables may be nil.
func (c *Client) graphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gqlURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	// The merge-queue mutation is still behind an accept header on some hosts.
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("graphql request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql HTTP %d: %s", resp.StatusCode, truncate(raw, 300))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode graphql envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", envelope.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// prNodeID resolves a PR's GraphQL node id, which the mutations for auto-merge,
// merge queue and draft toggling take instead of an owner/repo/number triple.
func (c *Client) prNodeID(ctx context.Context, repo, number string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	n, err := numberInt(number)
	if err != nil {
		return "", err
	}

	const query = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) { id }
  }
}`
	var resp struct {
		Repository struct {
			PullRequest *struct {
				ID string `json:"id"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "number": n}
	if err := c.graphQL(ctx, query, vars, &resp); err != nil {
		return "", err
	}
	if resp.Repository.PullRequest == nil || resp.Repository.PullRequest.ID == "" {
		return "", notFound(repo, number)
	}
	return resp.Repository.PullRequest.ID, nil
}

// numberInt parses a user-facing PR number.
func numberInt(number string) (int, error) {
	n, err := strconv.Atoi(number)
	if err != nil {
		return 0, fmt.Errorf("invalid PR number %q: %w", number, err)
	}
	return n, nil
}

// notFound builds the forge-level not-found error for a PR.
func notFound(repo, number string) error {
	return &forge.NotFoundError{Resource: "pull request", ID: repo + "#" + number}
}
