package webhook

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event keys the platform cares about. Everything else is stored but not
// turned into work (for example diagnostics:ping from the "Test connection"
// button).
const (
	EventRepoPush = "repo:push"
)

// ShouldProcess reports whether a delivery with this X-Event-Key starts a review.
func ShouldProcess(eventKey string) bool {
	return eventKey == EventRepoPush
}

// PushEvent is the subset of a repo:push payload the sync step needs. The raw
// payload is always stored as well, so more fields can be read later without
// re-delivery. Every field is optional on purpose: Bitbucket omits fields
// depending on the change.
type PushEvent struct {
	Repository Repository `json:"repository"`
	Actor      *Account   `json:"actor"`
	Push       struct {
		Changes []Change `json:"changes"`
	} `json:"push"`
}

type Repository struct {
	UUID      string     `json:"uuid"`
	Name      string     `json:"name"`
	FullName  string     `json:"full_name"`
	Project   *Project   `json:"project"`
	Workspace *Workspace `json:"workspace"`
}

type Project struct {
	UUID string `json:"uuid"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type Workspace struct {
	UUID string `json:"uuid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type Account struct {
	UUID        string `json:"uuid"`
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
}

type Change struct {
	New       *Ref     `json:"new"` // nil when the branch was deleted
	Old       *Ref     `json:"old"` // nil when the branch was created
	Created   bool     `json:"created"`
	Closed    bool     `json:"closed"`
	Forced    bool     `json:"forced"`
	Truncated bool     `json:"truncated"` // Commits holds only the first part
	Commits   []Commit `json:"commits"`
}

type Ref struct {
	Type   string `json:"type"` // "branch" or "tag"
	Name   string `json:"name"`
	Target struct {
		Hash string `json:"hash"`
	} `json:"target"`
}

type Commit struct {
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	Date    time.Time `json:"date"`
	Parents []struct {
		Hash string `json:"hash"`
	} `json:"parents"`
	Author struct {
		Raw  string   `json:"raw"` // "Name <email>"
		User *Account `json:"user"`
	} `json:"author"`
}

// IsMerge reports whether the commit has more than one parent.
func (c Commit) IsMerge() bool { return len(c.Parents) > 1 }

// ParsePush decodes a repo:push payload and checks the fields every later step
// relies on.
func ParsePush(raw []byte) (PushEvent, error) {
	var e PushEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return PushEvent{}, fmt.Errorf("decode repo:push: %w", err)
	}
	if strings.TrimSpace(e.Repository.UUID) == "" {
		return PushEvent{}, fmt.Errorf("repo:push has no repository.uuid")
	}
	return e, nil
}

// Branch returns the branch the change was pushed to, or "" for tags and for
// deleted refs.
func (c Change) Branch() string {
	if c.New != nil && c.New.Type == "branch" {
		return c.New.Name
	}
	return ""
}
