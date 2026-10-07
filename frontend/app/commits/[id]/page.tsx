import Link from "next/link";
import { notFound } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { dateTime, num, one, usd } from "@/lib/format";
import { ScoreBadge, SeverityBadge, StatusBadge } from "@/components/badges";
import Diff from "@/components/Diff";
import { rereview } from "../actions";

export const dynamic = "force-dynamic";

export default async function CommitPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  const notice = one((await searchParams).notice);

  const [[c, problem], [me]] = await Promise.all([guard(api.commit(id)), guard(api.me())]);
  if (!c) return problem;
  const admin = me?.role === "admin";
  const r = c.review;

  return (
    <>
      <p>
        <Link href="/commits">← Commits</Link>
      </p>
      {notice ? (
        <p className="banner" role="status">
          {notice}
        </p>
      ) : null}
      <h1>{c.subject || "(no message)"}</h1>
      <p className="sub">
        <span className="hash">{c.hash}</span> · {c.repository.full_name}
        {c.branch ? ` · ${c.branch}` : ""} · {dateTime(c.committed_at)}
        <br />
        {c.author.id ? <Link href={`/users/${c.author.id}`}>{c.author.name}</Link> : c.author.name || "unknown author"}
        {c.files_changed !== null ? ` · ${c.files_changed} files, +${c.additions ?? 0} −${c.deletions ?? 0}` : ""}
      </p>

      <div className="row" style={{ marginBottom: 12 }}>
        <StatusBadge status={c.review_status} reason={c.review_skip_reason} />
        {r ? <ScoreBadge value={r.score} /> : null}
        {c.review_skip_reason ? <span className="muted">{c.review_skip_reason}</span> : null}
        {admin && !c.is_merge ? (
          <form action={rereview} style={{ marginLeft: "auto" }}>
            <input type="hidden" name="id" value={c.id} />
            <button disabled={c.review_status === "running"}>Review again</button>
          </form>
        ) : null}
      </div>

      {c.message.includes("\n") ? <div className="msg">{c.message}</div> : null}

      {!r ? (
        <div className="empty" style={{ marginTop: 16 }}>
          {c.review_status === "pending" || c.review_status === "running" ? "Waiting for the review." : "This commit has no review."}
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
            {c.reviews_count > 1 ? ` · latest of ${c.reviews_count} reviews` : ""}
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
