import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { dateTime, one } from "@/lib/format";
import FailedAttemptsNote from "@/components/FailedAttemptsNote";
import AutoRefresh from "@/components/AutoRefresh";
import ReviewPanel from "@/components/ReviewPanel";
import { DiffStat, PRStateBadge, StatusBadge } from "@/components/badges";
import { rereview } from "@/app/commits/actions";
import { rereviewPullRequest } from "@/app/pull-requests/actions";

type SP = Record<string, string | string[] | undefined>;

// The body of a commit or pull request page. `base` is the page it is shown on: its own page, or
// the inbox's detail pane. Every link and form on it comes back to `base`.
type Props = { id: number; sp: SP; base: string; crumbs?: boolean };

const views = (base: string, sp: SP) => {
  const layout = one(sp.view) === "split" ? ("split" as const) : ("unified" as const);
  const hrefFor = (l: "unified" | "split") => (l === "split" ? `${base}?view=split` : base);
  return { layout, hrefFor, back: hrefFor(layout), filter: { q: one(sp.fq), status: one(sp.fs), severity: one(sp.fsev) } };
};

function Notice({ sp }: { sp: SP }) {
  const notice = one(sp.notice);
  return notice ? (
    <p className="banner" role="status">
      {notice}
    </p>
  ) : null;
}

export async function CommitDetail({ id, sp, base, crumbs = true }: Props) {
  const { layout, hrefFor, back, filter } = views(base, sp);
  const [[c, problem], [me]] = await Promise.all([guard(api.commit(id)), guard(api.me())]);
  if (!c) return problem;
  const admin = me?.role === "admin";
  const r = c.review;

  return (
    <>
      {crumbs ? (
        <div className="head">
          <nav className="crumbs" aria-label="Breadcrumb">
            <Link href="/commits">Commits</Link> / <Link href={`/commits?repo_id=${c.repository.id}`}>{c.repository.full_name}</Link>
          </nav>
        </div>
      ) : null}
      <Notice sp={sp} />

      <header className="subject">
        {crumbs ? null : <p className="kicker">{c.repository.full_name} · Commit</p>}
        <h1>{c.subject || "(no message)"}</h1>
        <div className="meta">
          <span className="hash">{c.hash.slice(0, 12)}</span>
          {c.branch ? <span>{c.branch}</span> : null}
          <span>{c.author.id ? <Link href={`/users/${c.author.id}`}>{c.author.name}</Link> : c.author.name || "Unknown author"}</span>
          <time dateTime={c.committed_at}>{dateTime(c.committed_at)}</time>
        </div>
        <div className="statline">
          <StatusBadge status={c.review_status} reason={c.review_skip_reason} />
          {c.review_status === "pending" || c.review_status === "running" ? <AutoRefresh /> : null}
          {c.review_skip_reason ? <span className="muted">{c.review_skip_reason}</span> : null}
          <DiffStat files={c.files_changed} additions={c.additions} deletions={c.deletions} />
          {admin && !c.is_merge ? (
            <form action={rereview}>
              <input type="hidden" name="id" value={c.id} />
              <input type="hidden" name="back" value={base} />
              <button disabled={c.review_status === "running"}>Review again</button>
            </form>
          ) : null}
        </div>
      </header>

      {c.message.trim().includes("\n") ? <div className="body-text">{c.message}</div> : null}

      <FailedAttemptsNote attempts={c.failed_attempts} />

      {!r ? (
        <div className="empty" style={{ marginTop: 24 }}>
          <strong>{c.review_status === "pending" || c.review_status === "running" ? "Waiting for the review" : "No review for this commit"}</strong>
          {c.review_status === "pending" || c.review_status === "running"
            ? "This page updates by itself. The review appears here when the reviewer is done."
            : c.review_skip_reason ?? (admin ? "Use Review again to queue one." : "Ask an admin to queue one.")}
        </div>
      ) : (
        <ReviewPanel review={r} reviewsCount={c.reviews_count} layout={layout} hrefFor={hrefFor} me={me} filter={filter} path={base} authorId={c.author.id} back={back} />
      )}
    </>
  );
}

export async function PullRequestDetail({ id, sp, base, crumbs = true }: Props) {
  const { layout, hrefFor, back, filter } = views(base, sp);
  const [[p, problem], [me]] = await Promise.all([guard(api.pullRequest(id)), guard(api.me())]);
  if (!p) return problem;
  const admin = me?.role === "admin";
  const r = p.review;

  return (
    <>
      {crumbs ? (
        <div className="head">
          <nav className="crumbs" aria-label="Breadcrumb">
            <Link href="/pull-requests">Pull requests</Link> / <Link href={`/pull-requests?repo_id=${p.repository.id}`}>{p.repository.full_name}</Link>
          </nav>
        </div>
      ) : null}
      <Notice sp={sp} />

      <header className="subject">
        {crumbs ? null : <p className="kicker">{p.repository.full_name} · Pull request</p>}
        <h1>{p.title || "(no title)"}</h1>
        <div className="meta">
          <span className="hash">#{p.number}</span>
          {p.source_branch ? (
            <span>
              {p.source_branch} into {p.destination_branch ?? "?"}
            </span>
          ) : null}
          <span>{p.author.id ? <Link href={`/users/${p.author.id}`}>{p.author.name}</Link> : p.author.name || "Unknown author"}</span>
          <span>Updated {dateTime(p.updated_at)}</span>
        </div>
        <div className="statline">
          <PRStateBadge state={p.state} />
          <StatusBadge status={p.review_status} reason={p.review_skip_reason} />
          {p.review_status === "pending" || p.review_status === "running" ? <AutoRefresh /> : null}
          {p.review_skip_reason ? <span className="muted">{p.review_skip_reason}</span> : null}
          {p.review_outdated ? <span className="tag warn">The branch has new commits since this review</span> : null}
          <DiffStat files={p.files_changed} additions={p.additions} deletions={p.deletions} />
          {admin && p.state === "OPEN" ? (
            <form action={rereviewPullRequest}>
              <input type="hidden" name="id" value={p.id} />
              <input type="hidden" name="back" value={base} />
              <button disabled={p.review_status === "running"}>Review again</button>
            </form>
          ) : null}
        </div>
      </header>

      {p.description ? <div className="body-text">{p.description}</div> : null}

      <FailedAttemptsNote attempts={p.failed_attempts} />

      {!r ? (
        <div className="empty" style={{ marginTop: 24 }}>
          <strong>{p.review_status === "pending" || p.review_status === "running" ? "Waiting for the review" : "No review for this pull request"}</strong>
          {p.review_status === "pending" || p.review_status === "running"
            ? "This page updates by itself. The review appears here when the reviewer is done."
            : p.review_skip_reason ?? (admin && p.state === "OPEN" ? "Use Review again to queue one." : "Only open pull requests can be reviewed.")}
        </div>
      ) : (
        <ReviewPanel review={r} reviewsCount={p.reviews_count} layout={layout} hrefFor={hrefFor} me={me} filter={filter} path={base} authorId={p.author.id} back={back} />
      )}
    </>
  );
}
