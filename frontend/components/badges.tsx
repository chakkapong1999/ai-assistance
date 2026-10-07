import type { PullRequestState, ReviewStatus, Severity } from "@/lib/api";
import { score as fmt } from "@/lib/format";

export function ScoreBadge({ value }: { value: number | null | undefined }) {
  if (value === null || value === undefined) return <span className="muted">–</span>;
  const cls = value >= 90 ? "good" : value >= 70 ? "warn" : "bad";
  return <span className={`badge ${cls}`}>{fmt(value)}</span>;
}

const statusClass: Record<ReviewStatus, string> = { done: "good", failed: "bad", pending: "warn", running: "warn", skipped: "" };

export function StatusBadge({ status, reason }: { status: ReviewStatus; reason?: string | null }) {
  return (
    <span className={`badge ${statusClass[status]}`} title={reason ?? undefined}>
      {status}
    </span>
  );
}

const sevClass: Record<Severity, string> = { critical: "bad", major: "warn", minor: "", info: "" };

export function SeverityBadge({ severity }: { severity: Severity }) {
  return <span className={`badge ${sevClass[severity]}`}>{severity}</span>;
}

const stateClass: Record<PullRequestState, string> = { OPEN: "good", MERGED: "", DECLINED: "", SUPERSEDED: "", DELETED: "" };

export function PRStateBadge({ state }: { state: PullRequestState }) {
  return <span className={`badge ${stateClass[state]}`}>{state.toLowerCase()}</span>;
}
