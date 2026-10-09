package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// maxPages stops a server that keeps returning a "next" link from looping forever.
const maxPages = 100

// Commit has the same shape in webhook payloads and in the commits API.
type Commit = webhook.Commit

// PullRequest is a pull request as returned by the pullrequests API.
type PullRequest = webhook.PullRequest

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

// Repository is what Bitbucket returns for a repository; it is the same shape
// as the repository object inside a push webhook, so it feeds the same sync.
type Repository = webhook.Repository

// Branch is a branch and the commit it points at.
type Branch struct {
	Name string `json:"name"`
	Head struct {
		Hash string `json:"hash"`
	} `json:"target"`
}

// GetRepository returns a repository's identity: uuid, project, workspace and
// main branch.
func (c *Client) GetRepository(ctx context.Context, workspace, repo string) (Repository, error) {
	var r Repository
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return r, err
	}
	b, err := c.get(ctx, c.endpoint("repositories", workspace, repo))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("bitbucket: decoding repository: %w", err)
	}
	if r.UUID == "" {
		return r, fmt.Errorf("bitbucket: repository %s/%s has no uuid in the response", workspace, repo)
	}
	return r, nil
}

// ListWorkspaceRepositories returns every repository the token can see in a workspace.
func (c *Client) ListWorkspaceRepositories(ctx context.Context, workspace string) ([]Repository, error) {
	if err := need(map[string]string{"workspace": workspace}); err != nil {
		return nil, err
	}
	return getAll[Repository](ctx, c, c.endpoint("repositories", workspace)+"?pagelen=100")
}

// ListBranches returns all branches of a repository with their head commits.
func (c *Client) ListBranches(ctx context.Context, workspace, repo string) ([]Branch, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return nil, err
	}
	return getAll[Branch](ctx, c, c.endpoint("repositories", workspace, repo, "refs", "branches")+"?pagelen=100")
}

// ListRecentCommits is ListCommits for a repository whose history may be long:
// it reads newest-first pages only until it has `max` commits or reaches one
// committed before `since` (zero = no date limit), instead of the whole history.
func (c *Client) ListRecentCommits(ctx context.Context, workspace, repo, include, exclude string, since time.Time, max int) ([]Commit, error) {
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
	var out []Commit
	next := c.endpoint("repositories", workspace, repo, "commits") + "?" + q.Encode()
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
		var p page[Commit]
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("bitbucket: decoding commits page %d: %w", i+1, err)
		}
		for _, cm := range p.Values {
			if !since.IsZero() && !cm.Date.IsZero() && cm.Date.Before(since) {
				return out, nil
			}
			out = append(out, cm)
			if max > 0 && len(out) >= max {
				return out, nil
			}
		}
		next = p.Next
	}
	return out, nil
}

// ListWorkspacePermissions returns the role of every member of a workspace.
func (c *Client) ListWorkspacePermissions(ctx context.Context, workspace string) ([]WorkspacePermission, error) {
	if err := need(map[string]string{"workspace": workspace}); err != nil {
		return nil, err
	}
	return getAll[WorkspacePermission](ctx, c, c.endpoint("workspaces", workspace, "permissions")+"?pagelen=100")
}

// GetUser returns one account's profile. id is "{uuid}" (with the braces) or an account id.
func (c *Client) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	if err := need(map[string]string{"id": id}); err != nil {
		return u, err
	}
	body, err := c.get(ctx, c.endpoint("users", id))
	if err != nil {
		return u, err
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return u, fmt.Errorf("decode user: %w", err)
	}
	return u, nil
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

// ListOpenPullRequests returns the open pull requests of a repository, most
// recently updated first, at most max of them (0 = no limit). The caller can
// tell the list was cut short by len == max.
func (c *Client) ListOpenPullRequests(ctx context.Context, workspace, repo string, max int) ([]PullRequest, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return nil, err
	}
	q := url.Values{"pagelen": {"50"}, "state": {"OPEN"}, "sort": {"-updated_on"}}
	var out []PullRequest
	next := c.endpoint("repositories", workspace, repo, "pullrequests") + "?" + q.Encode()
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
		var p page[PullRequest]
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("bitbucket: decoding pull requests page %d: %w", i+1, err)
		}
		for _, pr := range p.Values {
			out = append(out, pr)
			if max > 0 && len(out) >= max {
				return out, nil
			}
		}
		next = p.Next
	}
	return out, nil
}

// GetPullRequest returns one pull request; ErrNotFound if it is gone.
func (c *Client) GetPullRequest(ctx context.Context, workspace, repo string, id int) (PullRequest, error) {
	var pr PullRequest
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return pr, err
	}
	if id < 1 {
		return pr, errors.New("bitbucket: invalid pull request id")
	}
	b, err := c.get(ctx, c.endpoint("repositories", workspace, repo, "pullrequests", strconv.Itoa(id)))
	if err != nil {
		return pr, err
	}
	if err := json.Unmarshal(b, &pr); err != nil {
		return pr, fmt.Errorf("bitbucket: decoding pull request: %w", err)
	}
	if pr.ID == 0 {
		return pr, fmt.Errorf("bitbucket: pull request %d has no id in the response", id)
	}
	return pr, nil
}

// GetPullRequestDiff returns the unified diff of the whole pull request: the
// changes its source branch would bring into the destination, all commits
// together.
func (c *Client) GetPullRequestDiff(ctx context.Context, workspace, repo string, id int) (string, error) {
	if err := need(map[string]string{"workspace": workspace, "repo": repo}); err != nil {
		return "", err
	}
	if id < 1 {
		return "", errors.New("bitbucket: invalid pull request id")
	}
	b, err := c.get(ctx, c.endpoint("repositories", workspace, repo, "pullrequests", strconv.Itoa(id), "diff"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
