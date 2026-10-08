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

// Meta line parts; the gap between spans is the separator.
export function Parts({ items, empty }: { items: (string | null | undefined | false)[]; empty: string }) {
  const xs = items.filter((x): x is string => !!x);
  return xs.length ? xs.map((x) => <span key={x}>{x}</span>) : <span>{empty}</span>;
}

/** Where the fix workflow stands for a commit or pull request, as its own table column. */
export function FixMark({ reviewed, findings, open, closed }: { reviewed: boolean; findings: number; open: number; closed: boolean }) {
  if (!reviewed) return <span className="muted">Not reviewed</span>;
  if (findings === 0) return <span className="muted">Nothing to fix</span>;
  if (closed) return <span className="mark good">Closed</span>;
  if (open > 0) return <span className="mark warn">{open} to fix</span>;
  return <span className="mark info">Ready to close</span>;
}
