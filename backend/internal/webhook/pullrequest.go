package webhook

import "time"

// PullRequest is the subset of Bitbucket's pull request object the platform
// reads. It comes from the pullrequests API (polling); every field is optional
// because Bitbucket omits what it does not know.
type PullRequest struct {
	ID          int       `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"` // OPEN, MERGED, DECLINED or SUPERSEDED
	CreatedOn   time.Time `json:"created_on"`
	UpdatedOn   time.Time `json:"updated_on"`
	Author      *Account  `json:"author"`
	Source      PRSide    `json:"source"`
	Destination PRSide    `json:"destination"`
}

// PRSide is one end of a pull request.
type PRSide struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		// Bitbucket gives a 12-character hash here. It is only used to notice
		// that the branch moved, never to look a commit up.
		Hash string `json:"hash"`
	} `json:"commit"`
}

// IsOpen reports whether the pull request can still change.
func (p PullRequest) IsOpen() bool { return p.State == "OPEN" }
