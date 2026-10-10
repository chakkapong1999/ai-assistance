package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The fix workflow: the author of a commit or pull request marks each finding
// as fixed; a senior, lead or admin then looks again, sends a finding back or
// dismisses it, and finally closes the review. Nobody reviews their own work.

// Actor is who is acting. UserID is 0 for a token that is not linked to a user.
type Actor struct {
	Role     string // the token's role, recorded in the audit log
	UserID   int64
	Reviewer bool // senior, lead or admin
	// Admin may do everything in the workflow, with or without a user: fix on
	// the author's behalf, send back, dismiss and close. An admin token that is
	// not linked to a user is recorded without a name and, because nobody is
	// known, cannot be stopped from reviewing work it is also the author of.
	Admin bool
}

// who is the user to record; nil for an admin token with no user.
func (a Actor) who() any {
	if a.UserID == 0 {
		return nil
	}
	return a.UserID
}

// Denied means the actor may not do this; Conflict means the current state does not allow it.
type Denied struct{ Msg string }
type Conflict struct{ Msg string }

func (e *Denied) Error() string   { return e.Msg }
func (e *Conflict) Error() string { return e.Msg }

const maxNoteLen = 2000

type UserRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type FindingEvent struct {
	Action string    `json:"action"` // fixed | reopened | dismissed
	By     *UserRef  `json:"by"`
	Note   *string   `json:"note"`
	At     time.Time `json:"at"`
}

type ReviewClosed struct {
	At   time.Time `json:"at"`
	By   *UserRef  `json:"by"`
	Note *string   `json:"note"`
}

// target is a finding together with what the rules need to know about it.
type target struct {
	findingID, reviewID int64
	status              string
	closed              bool
	author              *int64
	commitID, prID      *int64 // what the review is of; for the audit log's links
}

// ids are the links an audit entry carries.
func (t target) detail(extra map[string]any) map[string]any {
	m := map[string]any{"review_id": t.reviewID}
	if t.commitID != nil {
		m["commit_id"] = *t.commitID
	}
	if t.prID != nil {
		m["pull_request_id"] = *t.prID
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (d *Dashboard) lockFinding(ctx context.Context, tx pgx.Tx, findingID int64) (target, error) {
	t := target{findingID: findingID}
	var closedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT f.review_id, f.status, rv.closed_at, COALESCE(c.author_user_id, p.author_user_id), rv.commit_id, rv.pr_id
		FROM review_findings f
		JOIN reviews rv ON rv.id = f.review_id
		LEFT JOIN commits c ON c.id = rv.commit_id
		LEFT JOIN pull_requests p ON p.id = rv.pr_id
		WHERE f.id = $1 FOR UPDATE OF f, rv`, findingID).Scan(&t.reviewID, &t.status, &closedAt, &t.author, &t.commitID, &t.prID)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	t.closed = closedAt != nil
	return t, err
}

func (d *Dashboard) lockReview(ctx context.Context, tx pgx.Tx, reviewID int64) (target, error) {
	t := target{reviewID: reviewID}
	var closedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT rv.closed_at, COALESCE(c.author_user_id, p.author_user_id), rv.commit_id, rv.pr_id
		FROM reviews rv
		LEFT JOIN commits c ON c.id = rv.commit_id
		LEFT JOIN pull_requests p ON p.id = rv.pr_id
		WHERE rv.id = $1 FOR UPDATE OF rv`, reviewID).Scan(&closedAt, &t.author, &t.commitID, &t.prID)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	t.closed = closedAt != nil
	return t, err
}

func cleanNote(note string, required bool) (*string, error) {
	note = strings.TrimSpace(note)
	switch {
	case note == "" && required:
		return nil, &Conflict{"a note is required: say what is still wrong, or why this is not a problem"}
	case len(note) > maxNoteLen:
		return nil, &Conflict{fmt.Sprintf("the note is too long (max %d characters)", maxNoteLen)}
	case note == "":
		return nil, nil
	}
	return &note, nil
}

func needsUser(a Actor) error {
	if a.UserID == 0 && !a.Admin {
		return &Denied{"this token is not linked to a user, so it cannot take part in reviews"}
	}
	return nil
}

func reviewerRules(a Actor, t target) error {
	if err := needsUser(a); err != nil {
		return err
	}
	if !a.Reviewer && !a.Admin {
		return &Denied{"only a senior, lead or admin can do this"}
	}
	if a.UserID != 0 && t.author != nil && *t.author == a.UserID {
		return &Denied{"you cannot review your own work; ask someone else"}
	}
	return nil
}

func (d *Dashboard) act(ctx context.Context, a Actor, fn func(tx pgx.Tx) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if a.UserID != 0 {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, a.UserID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return &Denied{"the user this token belongs to no longer exists"}
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setStatus(ctx context.Context, tx pgx.Tx, t target, a Actor, status, action string, note *string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE review_findings SET status = $2, status_by = $3, status_note = $4, status_at = now() WHERE id = $1`,
		t.findingID, status, a.who(), note); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO finding_events (finding_id, actor_id, action, note) VALUES ($1, $2, $3, $4)`,
		t.findingID, a.who(), action, note); err != nil {
		return err
	}
	extra := map[string]any{"from": t.status, "to": status}
	if note != nil {
		extra["note"] = *note
	}
	return record(ctx, tx, a, "finding."+action, "finding", t.findingID, t.detail(extra))
}

// MarkFixed is the author's "I fixed this". When the commit has no linked author
// (a raw name and e-mail only), a reviewer may stand in.
func (d *Dashboard) MarkFixed(ctx context.Context, a Actor, findingID int64, note string) error {
	n, err := cleanNote(note, false)
	if err != nil {
		return err
	}
	return d.act(ctx, a, func(tx pgx.Tx) error {
		t, err := d.lockFinding(ctx, tx, findingID)
		if err != nil {
			return err
		}
		if err := needsUser(a); err != nil {
			return err
		}
		if !a.Admin && (t.author == nil && !a.Reviewer || t.author != nil && *t.author != a.UserID) {
			return &Denied{"only the author of this change can mark a finding as fixed"}
		}
		switch {
		case t.closed:
			return &Conflict{"this review is closed"}
		case t.status != "open":
			return &Conflict{"this finding is already " + t.status}
		}
		return setStatus(ctx, tx, t, a, "fixed", "fixed", n)
	})
}

// Reopen sends a fixed (or dismissed) finding back to the author. On a closed
// review it also reopens the review.
func (d *Dashboard) Reopen(ctx context.Context, a Actor, findingID int64, note string) error {
	n, err := cleanNote(note, true)
	if err != nil {
		return err
	}
	return d.act(ctx, a, func(tx pgx.Tx) error {
		t, err := d.lockFinding(ctx, tx, findingID)
		if err != nil {
			return err
		}
		if err := reviewerRules(a, t); err != nil {
			return err
		}
		if t.status == "open" {
			return &Conflict{"this finding is already open"}
		}
		if t.closed {
			if _, err := tx.Exec(ctx, `UPDATE reviews SET closed_at = NULL, closed_by = NULL, close_note = NULL WHERE id = $1`, t.reviewID); err != nil {
				return err
			}
			if err := record(ctx, tx, a, AuditReviewReopened, "review", t.reviewID, t.detail(nil)); err != nil {
				return err
			}
		}
		return setStatus(ctx, tx, t, a, "open", "reopened", n)
	})
}

// Dismiss records that a finding is not a real problem.
func (d *Dashboard) Dismiss(ctx context.Context, a Actor, findingID int64, note string) error {
	n, err := cleanNote(note, true)
	if err != nil {
		return err
	}
	return d.act(ctx, a, func(tx pgx.Tx) error {
		t, err := d.lockFinding(ctx, tx, findingID)
		if err != nil {
			return err
		}
		if err := reviewerRules(a, t); err != nil {
			return err
		}
		switch {
		case t.closed:
			return &Conflict{"this review is closed; reopen a finding first"}
		case t.status == "dismissed":
			return &Conflict{"this finding is already dismissed"}
		}
		return setStatus(ctx, tx, t, a, "dismissed", "dismissed", n)
	})
}

// CloseReview ends the review once no finding is open.
func (d *Dashboard) CloseReview(ctx context.Context, a Actor, reviewID int64, note string) error {
	n, err := cleanNote(note, false)
	if err != nil {
		return err
	}
	return d.act(ctx, a, func(tx pgx.Tx) error {
		t, err := d.lockReview(ctx, tx, reviewID)
		if err != nil {
			return err
		}
		if err := reviewerRules(a, t); err != nil {
			return err
		}
		if t.closed {
			return &Conflict{"this review is already closed"}
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM review_findings WHERE review_id = $1 AND status = 'open'`, reviewID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return &Conflict{fmt.Sprintf("%d findings are still open: the author fixes them, or you dismiss them with a reason", open)}
		}
		if _, err = tx.Exec(ctx, `UPDATE reviews SET closed_at = now(), closed_by = $2, close_note = $3 WHERE id = $1`, reviewID, a.who(), n); err != nil {
			return err
		}
		extra := map[string]any{}
		if n != nil {
			extra["note"] = *n
		}
		return record(ctx, tx, a, AuditReviewClosed, "review", reviewID, t.detail(extra))
	})
}
