import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, num, one } from "@/lib/format";
import Segments from "@/components/Segments";
import Score from "@/components/Score";
import { Parts } from "@/components/badges";
import { toggleReview } from "./actions";

export const metadata = { title: "Repositories" };

export default async function Repositories({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const sp = await searchParams;
  const q = one(sp.q);
  const notice = one(sp.notice);
  const on = one(sp.review_enabled);
  const keep = Object.fromEntries(Object.entries({ q, review_enabled: on }).filter(([, v]) => v)) as Record<string, string>;
  const [[list, problem], [me]] = await Promise.all([guard(api.repositories({ q, review_enabled: on, limit: 200 })), guard(api.me())]);
  if (!list) return problem;
  const admin = me?.role === "admin";
  const enabled = list.items.filter((r) => r.review_enabled).length;

  return (
    <>
      <div className="head">
        <h1>Repositories</h1>
      </div>
      <p className="intro">
            {enabled} of {list.total} are reviewed. Turning review on sends that repository&apos;s code to the reviewer. Turning it off keeps past reviews, and commits pushed while it is off are
            skipped for good.
      </p>
      {notice ? (
        <p className="banner" role="status">
          {notice}
        </p>
      ) : null}
      <form className="toolbar" action="/repositories">
        <Segments
          base="/repositories"
          param="review_enabled"
          current={on}
          keep={keep}
          label="Review on or off"
          options={[
            ["", "All"],
            ["true", "Review on"],
            ["false", "Review off"],
          ]}
        />
        {on ? <input type="hidden" name="review_enabled" value={on} /> : null}
        <label>
          Search
          <input type="search" name="q" defaultValue={q} placeholder="workspace/repo" />
        </label>
        <button className="primary">Search</button>
        {q || on ? <Link href="/repositories">Clear filters</Link> : null}
      </form>

      {list.items.length === 0 ? (
        <div className="empty">
          <strong>{q || on ? "No repository matches" : "No repositories yet"}</strong>
          {q || on ? "Clear a filter to see more." : "Repositories are created when Bitbucket first sends a push or the poller finds them."}
        </div>
      ) : (
        <ul className="rows">
          <li className="item hd cols-repo" aria-hidden="true">
            <span>Score</span>
            <span>Repository</span>
            <span className="r">Reviewed</span>
            <span className="r">Last commit</span>
            <span className="r">Review</span>
          </li>
          {list.items.map((r) => (
            <li key={r.id} className="item cols-repo">
              <Score value={r.avg_score} />
              <div>
                <Link href={`/commits?repo_id=${r.id}`} className="title">
                  {r.full_name}
                </Link>
                <div className="meta"><Parts items={[r.project_key === "NONE" ? null : r.project_name, r.main_language, r.default_branch]} empty="No details yet" /></div>
              </div>
              <div className="side r">
                {num(r.reviewed)} of {num(r.commits)}
                <div className="muted" style={{ fontSize: ".8rem" }}>
                  commits reviewed
                </div>
              </div>
              <time className="r">{r.last_commit_at ? ago(r.last_commit_at) : "No commits"}</time>
              <div className="r">
                {admin ? (
                  <form action={toggleReview} style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
                    <input type="hidden" name="id" value={r.id} />
                    <input type="hidden" name="enable" value={String(!r.review_enabled)} />
                    <span className={`mark ${r.review_enabled ? "good" : "idle"}`}>{r.review_enabled ? "Review on" : "Review off"}</span>
                    <button className={r.review_enabled ? "quiet-danger" : "primary"}>{r.review_enabled ? "Turn off" : "Turn on"}</button>
                  </form>
                ) : (
                  <span className={`mark ${r.review_enabled ? "good" : "idle"}`}>{r.review_enabled ? "Review on" : "Review off"}</span>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
