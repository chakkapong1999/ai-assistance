import Link from "next/link";
import { notFound } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { dateTime, one } from "@/lib/format";
import ReviewPanel from "@/components/ReviewPanel";
import { DiffStat, PRStateBadge, StatusBadge } from "@/components/badges";
import { rereviewPullRequest } from "../actions";

type SP = Record<string, string | string[] | undefined>;

export async function generateMetadata({ params }: { params: Promise<{ id: string }> }) {
  return { title: `Pull request ${(await params).id}` };
}

export default async function PullRequestPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: Promise<SP> }) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  const sp = await searchParams;
  const notice = one(sp.notice);
  const layout = one(sp.view) === "split" ? "split" : "unified";

  const [[p, problem], [me]] = await Promise.all([guard(api.pullRequest(id)), guard(api.me())]);
  if (!p) return problem;
  const admin = me?.role === "admin";
  const r = p.review;
  const hrefFor = (l: "unified" | "split") => (l === "split" ? `/pull-requests/${id}?view=split` : `/pull-requests/${id}`);

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link href="/pull-requests">Pull requests</Link> / <Link href={`/pull-requests?repo_id=${p.repository.id}`}>{p.repository.full_name}</Link>
      </nav>
      {notice ? (
        <p className="banner" role="status">
          {notice}
        </p>
      ) : null}

      <header className="subject">
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
          {p.review_skip_reason ? <span className="muted">{p.review_skip_reason}</span> : null}
          {p.review_outdated ? <span className="tag warn">The branch has new commits since this review</span> : null}
          <DiffStat files={p.files_changed} additions={p.additions} deletions={p.deletions} />
          {admin && p.state === "OPEN" ? (
            <form action={rereviewPullRequest}>
              <input type="hidden" name="id" value={p.id} />
              <button disabled={p.review_status === "running"}>Review again</button>
            </form>
          ) : null}
        </div>
      </header>

      {p.description ? <div className="body-text">{p.description}</div> : null}

      {!r ? (
        <div className="empty" style={{ marginTop: 24 }}>
          <strong>{p.review_status === "pending" || p.review_status === "running" ? "Waiting for the review" : "No review for this pull request"}</strong>
          {p.review_status === "pending" || p.review_status === "running"
            ? "Refresh in a minute. The review appears here when the reviewer is done."
            : p.review_skip_reason ?? (admin && p.state === "OPEN" ? "Use Review again to queue one." : "Only open pull requests can be reviewed.")}
        </div>
      ) : (
        <ReviewPanel review={r} reviewsCount={p.reviews_count} layout={layout} hrefFor={hrefFor} />
      )}
    </>
  );
}
