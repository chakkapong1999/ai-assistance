import type { AuditEntry } from "./api";

export type AuditLine = { text: string; href: string | null };

const id = (v: unknown): number | null => (typeof v === "number" && Number.isInteger(v) && v > 0 ? v : null);

// The page a subject lives on: the commit or pull request a finding or review
// belongs to (the log stores those ids in detail), with the finding in view.
function target(e: AuditEntry): string | null {
  const commit = id(e.detail.commit_id);
  const pr = id(e.detail.pull_request_id);
  const base = commit ? `/commits/${commit}` : pr ? `/pull-requests/${pr}` : null;
  switch (e.subject.type) {
    case "finding":
      return base ? `${base}#f-${e.subject.id}` : null;
    case "review":
      return base;
    case "commit":
      return `/commits/${e.subject.id}`;
    case "pull_request":
      return `/pull-requests/${e.subject.id}`;
    case "repository":
      return `/commits?repo_id=${e.subject.id}`;
  }
  return null;
}

/** What happened, in words a person reads, and where to look at it. */
export function describe(e: AuditEntry): AuditLine {
  const repo = typeof e.detail.repository === "string" ? e.detail.repository : `repository ${e.subject.id}`;
  const text: Record<string, string> = {
    "finding.fixed": "marked a finding as fixed",
    "finding.reopened": "sent a finding back",
    "finding.dismissed": "dismissed a finding",
    "review.closed": "closed a review",
    "review.reopened": "reopened a closed review",
    "repository.review_enabled": `switched review on for ${repo}`,
    "repository.review_disabled": `switched review off for ${repo}`,
    "commit.rereview": "asked for a new review of a commit",
    "pull_request.rereview": "asked for a new review of a pull request",
  };
  return { text: text[e.action] ?? e.action, href: target(e) };
}

/** The actor's name, or the role when the token belongs to nobody in particular. */
export function actorName(e: AuditEntry): string {
  if (e.actor.name) return e.actor.name;
  return e.actor.id ? `User ${e.actor.id}` : `${e.actor.role} token`;
}

export const groups: [value: string, label: string][] = [
  ["", "Everything"],
  ["finding", "Findings"],
  ["review", "Reviews"],
  ["repository", "Repositories"],
  ["commit", "Commit re-reviews"],
  ["pull_request", "Pull request re-reviews"],
];
