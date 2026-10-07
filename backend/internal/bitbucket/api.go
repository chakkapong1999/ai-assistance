package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// maxPages stops a server that keeps returning a "next" link from looping forever.
const maxPages = 100

// Commit has the same shape in webhook payloads and in the commits API.
type Commit = webhook.Commit

// User is an account as returned by the permission endpoints.
type User struct {
	UUID        string `json:"uuid"`
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
	Links       struct {
		Avatar struct {
			Href string `json:"href"`
		} `json:"avatar"`
	} `json:"links"`
}

// WorkspacePermission is a member's role in a workspace: owner, collaborator or member.
type WorkspacePermission struct {
	Permission string `json:"permission"`
	User       User   `json:"user"`
}

// RepoPermission is a user's explicit permission on a repository: admin, write or read.
type RepoPermission struct {
	Permission string `json:"permission"`
	User       User   `json:"user"`
}

// GetDiff returns the unified diff of one commit against its parent.
func (c *Client) GetDiff(ctx context.Context, workspace, repo, hash string) (string, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo, "hash": hash}); err != nil {
		return "", err
	}
	b, err := c.get(ctx, c.endpoint("repositories", workspace, repo, "diff", hash))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// GetFile returns a file as it was at a commit. ErrNotFound means the file did
// not exist there, which is normal for files a commit adds or deletes.
func (c *Client) GetFile(ctx context.Context, workspace, repo, hash, path string) ([]byte, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo, "hash": hash, "path": path}); err != nil {
		return nil, err
	}
	dirs := strings.Split(strings.Trim(path, "/"), "/")
	for _, d := range dirs {
		if d == "" || d == "." || d == ".." {
			return nil, fmt.Errorf("bitbucket: invalid file path %q", path)
		}
	}
	segs := append([]string{"repositories", workspace, repo, "src", hash}, dirs...)
	return c.get(ctx, c.endpoint(segs...))
}

// ListCommits returns every commit reachable from include and not from
// exclude (either may be empty), newest first. This is how a truncated push
// payload is completed.
func (c *Client) ListCommits(ctx context.Context, workspace, repo, include, exclude string) ([]Commit, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return nil, err
	}
	q := url.Values{"pagelen": {"100"}}
	if include != "" {
		q.Set("include", include)
	}
	if exclude != "" {
		q.Set("exclude", exclude)
	}
	return getAll[Commit](ctx, c, c.endpoint("repositories", workspace, repo, "commits")+"?"+q.Encode())
}

// ListWorkspacePermissions returns the role of every member of a workspace.
func (c *Client) ListWorkspacePermissions(ctx context.Context, workspace string) ([]WorkspacePermission, error) {
	if err := need(map[string]string{"workspace": workspace}); err != nil {
		return nil, err
	}
	return getAll[WorkspacePermission](ctx, c, c.endpoint("workspaces", workspace, "permissions")+"?pagelen=100")
}

// ListRepoUserPermissions returns the users with an explicit permission on a repository.
func (c *Client) ListRepoUserPermissions(ctx context.Context, workspace, repo string) ([]RepoPermission, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return nil, err
	}
	return getAll[RepoPermission](ctx, c, c.endpoint("repositories", workspace, repo, "permissions-config", "users")+"?pagelen=100")
}

type page[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

// getAll follows "next" links until the list ends. A next link that points at
// another host is refused: it would otherwise receive the access token.
func getAll[T any](ctx context.Context, c *Client, first string) ([]T, error) {
	var all []T
	next := first
	for i := 0; next != ""; i++ {
		if i >= maxPages {
			return nil, fmt.Errorf("bitbucket: more than %d pages, giving up", maxPages)
		}
		if err := c.sameOrigin(next); err != nil {
			return nil, err
		}
		b, err := c.get(ctx, next)
		if err != nil {
			return nil, err
		}
		var p page[T]
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("bitbucket: decoding page %d: %w", i+1, err)
		}
		all = append(all, p.Values...)
		next = p.Next
	}
	return all, nil
}

func (c *Client) sameOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return fmt.Errorf("bitbucket: refusing to follow %q: not on %s", raw, c.base.Host)
	}
	return nil
}

func need(fields map[string]string) error {
	var missing []string
	for k, v := range fields {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return errors.New("bitbucket: missing " + strings.Join(missing, ", "))
	}
	return nil
}
