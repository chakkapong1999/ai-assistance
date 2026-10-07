import { cookies } from "next/headers";

// The dashboard talks to the Go API only; it never connects to Postgres.
// API_BASE_URL and API_TOKEN are read on the server (see docker-compose.yml)
// and never reach the browser. API_TOKEN is the shared, read-only token. A
// person who signs in with their own token (see /signin) is remembered in an
// httpOnly cookie, and that token is used instead, so the API knows who acts.
// Real login (OAuth) can replace the cookie later without touching the rest.
export const TOKEN_COOKIE = "aicr_token";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(path: string, init: RequestInit = {}, tokenOverride?: string): Promise<T> {
  const base = process.env.API_BASE_URL ?? "http://localhost:8080";
  const token = tokenOverride ?? (await cookies()).get(TOKEN_COOKIE)?.value ?? process.env.API_TOKEN;
  if (!token) {
    throw new ApiError(0, "config", "API_TOKEN is not set for the dashboard (see README, REST API).");
  }
  let res: Response;
  try {
    res = await fetch(`${base}/api/v1${path}`, {
      ...init,
      cache: "no-store",
      headers: { ...init.headers, Authorization: `Bearer ${token}`, ...(init.body ? { "Content-Type": "application/json" } : {}) },
    });
  } catch {
    throw new ApiError(0, "unreachable", `The API at ${base} is not reachable.`);
  }
  if (!res.ok) {
    let code = "error";
    let message = `API answered ${res.status}`;
    try {
      const body = await res.json();
      code = body.error?.code ?? code;
      message = body.error?.message ?? message;
    } catch {}
    throw new ApiError(res.status, code, message);
  }
  return (await res.json()) as T;
}

export function qs(params: Record<string, string | number | undefined | null>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : "";
}

export type ReviewStatus = "pending" | "running" | "done" | "skipped" | "failed";
export type Severity = "critical" | "major" | "minor" | "info";

export type Overview = {
  days: number;
  since: string;
  commits: number;
  commits_by_status: Record<ReviewStatus, number>;
  reviewed: number;
  avg_score: number | null;
  findings_by_severity: Record<Severity, number>;
  usage: {
    runs: number;
    measured_runs: number;
    tokens_in: number;
    tokens_out: number;
    cost_usd: number;
    avg_cost_usd: number | null;
  };
  queue_waiting: number;
  queue_running: number;
  active_repositories: number;
  active_authors: number;
  enabled_repositories: number;
  total_repositories: number;
  series: { day: string; commits: number; reviewed: number; avg_score: number | null; cost_usd: number }[];
};

export type Repository = {
  id: number;
  workspace: string;
  project_key: string;
  project_name: string;
  slug: string;
  name: string;
  full_name: string;
  main_language: string | null;
  default_branch: string | null;
  review_enabled: boolean;
  commits: number;
  reviewed: number;
  avg_score: number | null;
  last_commit_at: string | null;
};

export type CommitSummary = {
  id: number;
  hash: string;
  subject: string;
  repository: { id: number; full_name: string };
  author: { id: number | null; name: string; avatar_url: string | null };
  branch: string | null;
  committed_at: string;
  review_status: ReviewStatus;
  review_skip_reason: string | null;
  files_changed: number | null;
  additions: number | null;
  deletions: number | null;
  is_merge: boolean;
  score: number | null;
  findings: number;
  reviewed_at: string | null;
};

export type Finding = {
  id: number;
  file_path: string;
  line_start: number;
  line_end: number;
  severity: Severity;
  category: string;
  title: string;
  explanation: string;
  /** The diff hunk the finding is about; null for reviews made before it was recorded. */
  code_context: string | null;
  suggestion: { id: number; original_snippet: string; suggested_snippet: string; unified_diff: string; status: string } | null;
  /** open -> fixed (the author says so) -> back to open, or dismissed by a reviewer. */
  status: FindingStatus;
  status_by: { id: number; name: string } | null;
  status_note: string | null;
  status_at: string | null;
  history: { action: "fixed" | "reopened" | "dismissed"; by: { id: number; name: string } | null; note: string | null; at: string }[];
};

export type FindingStatus = "open" | "fixed" | "dismissed";
export type Role = "viewer" | "author" | "senior" | "lead" | "admin";
export type Me = { role: Role; user: { id: number; name: string } | null };

export type CommitDetail = CommitSummary & {
  message: string;
  reviews_count: number;
  review: {
    id: number;
    model: string;
    prompt_version: string;
    score: number | null;
    summary: string;
    duration_ms: number | null;
    tokens_in: number | null;
    tokens_out: number | null;
    cost_usd: number | null;
    created_at: string;
    /** Set once a senior, lead or admin closed the review. */
    closed: { at: string; by: { id: number; name: string } | null; note: string | null } | null;
    findings: Finding[];
  } | null;
};

export type PullRequestState = "OPEN" | "MERGED" | "DECLINED" | "SUPERSEDED" | "DELETED";

export type PullRequestSummary = {
  id: number;
  number: number;
  title: string;
  repository: { id: number; full_name: string };
  author: { id: number | null; name: string; avatar_url: string | null };
  source_branch: string | null;
  destination_branch: string | null;
  state: PullRequestState;
  review_status: ReviewStatus;
  review_skip_reason: string | null;
  files_changed: number | null;
  additions: number | null;
  deletions: number | null;
  score: number | null;
  findings: number;
  reviewed_at: string | null;
  review_outdated: boolean;
  updated_at: string;
};

export type PullRequestDetail = PullRequestSummary & {
  description: string;
  reviews_count: number;
  review: CommitDetail["review"];
};

export type User = {
  id: number;
  display_name: string;
  nickname: string | null;
  avatar_url: string | null;
  email?: string;
  job_title: string | null;
  department: string | null;
  linked: boolean;
  commits: number;
  reviewed: number;
  avg_score: number | null;
  findings: number;
};

export type UserDetail = User & { days: number; trend: { week: string; commits: number; reviewed: number; avg_score: number | null }[] };

type Page<T> = { items: T[]; total: number; limit: number; offset: number };

export const api = {
  me: () => call<Me>("/me"),
  meWith: (token: string) => call<Me>("/me", {}, token),
  findingFixed: (id: number, note: string) => call<{ status: string }>(`/findings/${id}/fixed`, { method: "POST", body: JSON.stringify({ note }) }),
  findingReopen: (id: number, note: string) => call<{ status: string }>(`/findings/${id}/reopen`, { method: "POST", body: JSON.stringify({ note }) }),
  findingDismiss: (id: number, note: string) => call<{ status: string }>(`/findings/${id}/dismiss`, { method: "POST", body: JSON.stringify({ note }) }),
  closeReview: (id: number, note: string) => call<{ status: string }>(`/reviews/${id}/close`, { method: "POST", body: JSON.stringify({ note }) }),
  overview: (days: number) => call<Overview>(`/overview${qs({ days })}`),
  repositories: (p: Record<string, string | number | undefined> = {}) => call<Page<Repository>>(`/repositories${qs(p)}`),
  setReviewEnabled: (id: number, enabled: boolean) =>
    call<Repository>(`/repositories/${id}`, { method: "PATCH", body: JSON.stringify({ review_enabled: enabled }) }),
  commits: (p: Record<string, string | number | undefined>) =>
    call<{ items: CommitSummary[]; next_cursor: string | null }>(`/commits${qs(p)}`),
  commit: (id: number) => call<CommitDetail>(`/commits/${id}`),
  rereview: (id: number) => call<{ status: string }>(`/commits/${id}/rereview`, { method: "POST" }),
  pullRequests: (p: Record<string, string | number | undefined>) =>
    call<{ items: PullRequestSummary[]; next_cursor: string | null }>(`/pull-requests${qs(p)}`),
  pullRequest: (id: number) => call<PullRequestDetail>(`/pull-requests/${id}`),
  rereviewPullRequest: (id: number) => call<{ status: string }>(`/pull-requests/${id}/rereview`, { method: "POST" }),
  users: (p: Record<string, string | number | undefined>) => call<Page<User>>(`/users${qs(p)}`),
  user: (id: number, days: number) => call<UserDetail>(`/users/${id}${qs({ days })}`),
};
