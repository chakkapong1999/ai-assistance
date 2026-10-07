import Link from "next/link";
import { notFound } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { dateTime, num, one, usd } from "@/lib/format";
import { PRStateBadge, ScoreBadge, SeverityBadge, StatusBadge } from "@/components/badges";
import Diff from "@/components/Diff";
import { rereviewPullRequest } from "../actions";

export const dynamic = "force-dynamic";

export default async function PullRequestPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  const notice = one((await searchParams).notice);

  const [[p, problem], [me]] = await Promise.all([guard(api.pullRequest(id)), guard(api.me())]);
  if (!p) return problem;
  const admin = me?.role === "admin";
  const r = p.review;

  return (
    <>
      <p>
        <Link href="/pull-requests">← Pull requests</Link>
      </p>
      {notice ? (
        <p className="banner" role="status">
          {notice}
        </p>
      ) : null}
      <h1>{p.title || "(no title)"}</h1>
      <p className="sub">
        #{p.number} · {p.repository.full_name}
        {p.source_branch ? ` · ${p.source_branch} → ${p.destination_branch ?? "?"}` : ""}
        <br />
        {p.author.id ? <Link href={`/users/${p.author.id}`}>{p.author.name}</Link> : p.author.name || "unknown author"}
        {p.files_changed !== null ? ` · ${p.files_changed} files, +${p.additions ?? 0} −${p.deletions ?? 0}` : ""}
        {` · updated ${dateTime(p.updated_at)}`}
      </p>

      <div className="row" style={{ marginBottom: 12 }}>
        <PRStateBadge state={p.state} />
        <StatusBadge status={p.review_status} reason={p.review_skip_reason} />
        {r ? <ScoreBadge value={r.score} /> : null}
        {p.review_skip_reason ? <span className="muted">{p.review_skip_reason}</span> : null}
        {p.review_outdated ? <span className="badge warn">branch has new commits since this review</span> : null}
        {admin && p.state === "OPEN" ? (
          <form action={rereviewPullRequest} style={{ marginLeft: "auto" }}>
            <input type="hidden" name="id" value={p.id} />
            <button disabled={p.review_status === "running"}>Review again</button>
          </form>
        ) : null}
      </div>

      {p.description ? <div className="msg">{p.description}</div> : null}

      {!r ? (
        <div className="empty" style={{ marginTop: 16 }}>
          {p.review_status === "pending" || p.review_status === "running" ? "Waiting for the review." : "This pull request has no review."}
        </div>
      ) : (
        <>
          <h2>Summary</h2>
          <p style={{ whiteSpace: "pre-wrap", marginTop: 0 }}>{r.summary || "–"}</p>
          <p className="muted">
            {r.model === "mock" ? <span className="badge warn">mock review, not a real model</span> : <>model {r.model}</>} · prompt {r.prompt_version} ·{" "}
            {dateTime(r.created_at)}
            {r.duration_ms !== null ? ` · ${(r.duration_ms / 1000).toFixed(1)} s` : ""}
            {r.tokens_in !== null ? ` · ${num(r.tokens_in)} in / ${num(r.tokens_out ?? 0)} out tokens` : ""}
            {r.cost_usd !== null ? ` · ${usd(r.cost_usd)}` : ""}
            {p.reviews_count > 1 ? ` · latest of ${p.reviews_count} reviews` : ""}
          </p>

          <h2>Findings ({r.findings.length})</h2>
          {r.findings.length === 0 ? <div className="empty">No findings.</div> : null}
          {r.findings.map((f) => (
            <article key={f.id} className={`finding ${f.severity}`}>
              <h3>
                <SeverityBadge severity={f.severity} /> {f.title}
              </h3>
              <div className="muted">
                <span className="hash">
                  {f.file_path}:{f.line_start}
                  {f.line_end > f.line_start ? `–${f.line_end}` : ""}
                </span>{" "}
                · {f.category}
              </div>
              {f.explanation ? <p>{f.explanation}</p> : null}
              {f.suggestion ? (
                <details open>
                  <summary>Suggested change</summary>
                  <Diff text={f.suggestion.unified_diff} />
                </details>
              ) : null}
            </article>
          ))}
        </>
      )}
    </>
  );
}
