import Link from "next/link";
import { notFound } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { dateTime, one } from "@/lib/format";
import ReviewPanel from "@/components/ReviewPanel";
import { DiffStat, StatusBadge } from "@/components/badges";
import { rereview } from "../actions";

type SP = Record<string, string | string[] | undefined>;

export async function generateMetadata({ params }: { params: Promise<{ id: string }> }) {
  return { title: `Commit ${(await params).id}` };
}

export default async function CommitPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: Promise<SP> }) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  const sp = await searchParams;
  const notice = one(sp.notice);
  const layout = one(sp.view) === "split" ? "split" : "unified";

  const [[c, problem], [me]] = await Promise.all([guard(api.commit(id)), guard(api.me())]);
  if (!c) return problem;
  const admin = me?.role === "admin";
  const r = c.review;
  const hrefFor = (l: "unified" | "split") => (l === "split" ? `/commits/${id}?view=split` : `/commits/${id}`);

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link href="/commits">Commits</Link> / <Link href={`/commits?repo_id=${c.repository.id}`}>{c.repository.full_name}</Link>
      </nav>
      {notice ? (
        <p className="banner" role="status">
          {notice}
        </p>
      ) : null}

      <header className="subject">
        <h1>{c.subject || "(no message)"}</h1>
        <div className="meta">
          <span className="hash">{c.hash.slice(0, 12)}</span>
          {c.branch ? <span>{c.branch}</span> : null}
          <span>{c.author.id ? <Link href={`/users/${c.author.id}`}>{c.author.name}</Link> : c.author.name || "Unknown author"}</span>
          <time dateTime={c.committed_at}>{dateTime(c.committed_at)}</time>
        </div>
        <div className="statline">
          <StatusBadge status={c.review_status} reason={c.review_skip_reason} />
          {c.review_skip_reason ? <span className="muted">{c.review_skip_reason}</span> : null}
          <DiffStat files={c.files_changed} additions={c.additions} deletions={c.deletions} />
          {admin && !c.is_merge ? (
            <form action={rereview}>
              <input type="hidden" name="id" value={c.id} />
              <button disabled={c.review_status === "running"}>Review again</button>
            </form>
          ) : null}
        </div>
      </header>

      {c.message.includes("\n") ? <div className="body-text">{c.message}</div> : null}

      {!r ? (
        <div className="empty" style={{ marginTop: 24 }}>
          <strong>{c.review_status === "pending" || c.review_status === "running" ? "Waiting for the review" : "No review for this commit"}</strong>
          {c.review_status === "pending" || c.review_status === "running"
            ? "Refresh in a minute. The review appears here when the reviewer is done."
            : c.review_skip_reason ?? (admin ? "Use Review again to queue one." : "Ask an admin to queue one.")}
        </div>
      ) : (
        <ReviewPanel review={r} reviewsCount={c.reviews_count} layout={layout} hrefFor={hrefFor} />
      )}
    </>
  );
}
