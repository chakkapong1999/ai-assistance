package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The audit log: who changed what, and when. Every change made through the API
// writes one row in the same transaction as the change, so the log can neither
// miss a change that happened nor show one that was rolled back.

// Audit actions. A filter on "finding" matches every "finding.*" action.
const (
	AuditFindingFixed        = "finding.fixed"
	AuditFindingReopened     = "finding.reopened"
	AuditFindingDismissed    = "finding.dismissed"
	AuditReviewClosed        = "review.closed"
	AuditReviewReopened      = "review.reopened"
	AuditRepoReviewOn        = "repository.review_enabled"
	AuditRepoReviewOff       = "repository.review_disabled"
	AuditCommitRereview      = "commit.rereview"
	AuditPullRequestRereview = "pull_request.rereview"
)

// record writes one audit row. detail is free-form context (a note, the ids a
// page link needs); nil is stored as an empty object.
func record(ctx context.Context, tx pgx.Tx, a Actor, action, subjectType string, subjectID int64, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("audit detail: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (actor_id, actor_name, actor_role, action, subject_type, subject_id, detail)
		VALUES ($1::bigint, (SELECT display_name FROM users WHERE id = $1::bigint), $2, $3, $4, $5, $6::jsonb)`,
		a.who(), a.Role, action, subjectType, subjectID, raw)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

type AuditActor struct {
	ID   *int64  `json:"id"`
	Name *string `json:"name"`
	Role string  `json:"role"`
}

type AuditSubject struct {
	Type string `json:"type"` // finding | review | commit | pull_request | repository
	ID   int64  `json:"id"`
}

type AuditEntry struct {
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	Actor   AuditActor     `json:"actor"`
	Action  string         `json:"action"`
	Subject AuditSubject   `json:"subject"`
	Detail  map[string]any `json:"detail"`
}

type AuditFilter struct {
	ActorID     int64
	Action      string // exact ("finding.fixed") or a group ("finding")
	SubjectType string
	SubjectID   int64
	Since       *time.Time
	Until       *time.Time
}

func auditConds(f AuditFilter, a *args) []string {
	var conds []string
	if f.ActorID != 0 {
		conds = append(conds, "actor_id = "+a.add(f.ActorID))
	}
	if f.Action != "" {
		p := a.add(f.Action)
		conds = append(conds, "(action = "+p+" OR action LIKE "+p+" || '.%')")
	}
	if f.SubjectType != "" {
		conds = append(conds, "subject_type = "+a.add(f.SubjectType))
	}
	if f.SubjectID != 0 {
		conds = append(conds, "subject_id = "+a.add(f.SubjectID))
	}
	if f.Since != nil {
		conds = append(conds, "at >= "+a.add(*f.Since))
	}
	if f.Until != nil {
		conds = append(conds, "at < "+a.add(*f.Until))
	}
	return conds
}

// Audit lists entries newest first.
func (d *Dashboard) Audit(ctx context.Context, f AuditFilter, limit, offset int) ([]AuditEntry, int, error) {
	var a args
	w := where(auditConds(f, &a))
	var total int
	if err := d.pool.QueryRow(ctx, "SELECT count(*)::int FROM audit_log"+w, a...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit: %w", err)
	}
	rows, err := d.pool.Query(ctx, `
		SELECT id, at, actor_id, actor_name, actor_role, action, subject_type, subject_id, detail
		FROM audit_log`+w+` ORDER BY at DESC, id DESC LIMIT `+a.add(limit)+` OFFSET `+a.add(offset), a...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.At, &e.Actor.ID, &e.Actor.Name, &e.Actor.Role, &e.Action, &e.Subject.Type, &e.Subject.ID, &raw); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(raw, &e.Detail); err != nil || e.Detail == nil {
			e.Detail = map[string]any{}
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
