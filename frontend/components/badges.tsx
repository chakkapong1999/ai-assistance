import type { PullRequestState, ReviewStatus, Severity } from "@/lib/api";

const status: Record<ReviewStatus, { tone: string; label: string }> = {
  done: { tone: "good", label: "Reviewed" },
  failed: { tone: "bad", label: "Failed" },
  pending: { tone: "warn", label: "Waiting" },
  running: { tone: "warn run", label: "Reviewing" },
  skipped: { tone: "idle", label: "Skipped" },
};

export function StatusBadge({ status: s, reason }: { status: ReviewStatus; reason?: string | null }) {
  const { tone, label } = status[s];
  return (
    <span className={`mark ${tone}`} title={reason ?? undefined}>
      {label}
    </span>
  );
}

const state: Record<PullRequestState, { tone: string; label: string }> = {
  OPEN: { tone: "good", label: "Open" },
  MERGED: { tone: "idle", label: "Merged" },
  DECLINED: { tone: "idle", label: "Declined" },
  SUPERSEDED: { tone: "idle", label: "Superseded" },
  DELETED: { tone: "idle", label: "Deleted" },
};

export function PRStateBadge({ state: s }: { state: PullRequestState }) {
  const { tone, label } = state[s];
  return <span className={`pill ${tone}`}>{label}</span>;
}

export function SeverityMark({ severity }: { severity: Severity }) {
  return <span className={`sev sev-${severity}`}>{severity[0].toUpperCase() + severity.slice(1)}</span>;
}

export function DiffStat({ files, additions, deletions }: { files: number | null; additions: number | null; deletions: number | null }) {
  if (files === null) return null;
  return (
    <span className="stat" title={`${files} files changed`}>
      <span className="a">+{additions ?? 0}</span> <span className="d">−{deletions ?? 0}</span> <span className="muted">in {files} {files === 1 ? "file" : "files"}</span>
    </span>
  );
}
