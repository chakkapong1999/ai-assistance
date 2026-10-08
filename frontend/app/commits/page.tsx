import Link from "next/link";
import { redirect } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, dateTime, one } from "@/lib/format";
import Score from "@/components/Score";
import Pager, { paging } from "@/components/Pager";
import Segments from "@/components/Segments";
import { DiffStat, FixMark, StatusBadge } from "@/components/badges";

export const metadata = { title: "Commits" };

type SP = Record<string, string | string[] | undefined>;
const keys = ["q", "status", "repo_id", "author_id", "branch", "fix"] as const;

export default async function Commits({ searchParams }: { searchParams: Promise<SP> }) {
  const sp = await searchParams;
  const f = Object.fromEntries(keys.map((k) => [k, one(sp[k])])) as Record<(typeof keys)[number], string>;
  const { page, size } = paging(one(sp.page), one(sp.size));

  const [[list, p1], [repos, p2]] = await Promise.all([guard(api.commits({ ...f, offset: (page - 1) * size, limit: size })), guard(api.repositories({ limit: 200 }))]);
  if (!list || !repos) return p1 ?? p2;

  const author = f.author_id ? (await guard(api.user(Number(f.author_id), 1)))[0] : null;
  const keep = Object.fromEntries(Object.entries(f).filter(([, v]) => v));
  if (list.items.length === 0 && page > 1 && list.total > 0) redirect(`/commits?${new URLSearchParams({ ...keep, page: String(Math.ceil(list.total / size)), ...(size !== 50 ? { size: String(size) } : {}) })}`);
  const filtered = Object.keys(keep).length > 0;

  return (
    <>
      <div className="head">
        <h1>Commits</h1>
      </div>
      <p className="intro">Every branch, newest first. Open a commit to read the review next to the code.</p>

      <form className="toolbar" action="/commits">
        {size !== 50 ? <input type="hidden" name="size" value={size} /> : null}
        <Segments
          base="/commits"
          param="status"
          current={f.status}
          keep={keep}
          label="Review status"
          options={[
            ["", "All"],
            ["done", "Reviewed"],
            ["failed", "Failed"],
            ["pending", "Waiting"],
            ["skipped", "Skipped"],
          ]}
        />
        {f.status ? <input type="hidden" name="status" value={f.status} /> : null}
        {f.author_id ? <input type="hidden" name="author_id" value={f.author_id} /> : null}
        <label>
          Search
          <input type="search" name="q" defaultValue={f.q} placeholder="Message or hash" />
        </label>
        <label>
          Repository
          <select name="repo_id" defaultValue={f.repo_id}>
            <option value="">All repositories</option>
            {repos.items.map((r) => (
              <option key={r.id} value={r.id}>
                {r.full_name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Branch
          <input type="text" name="branch" defaultValue={f.branch} placeholder="Exact name" />
        </label>
        <label>
          Fix
          <select name="fix" defaultValue={f.fix}>
            <option value="">Any</option>
            <option value="open">Findings to fix</option>
            <option value="ready">Ready to close</option>
            <option value="closed">Closed</option>
          </select>
        </label>
        <button className="primary">Apply</button>
        {filtered ? <Link href="/commits">Clear filters</Link> : null}
      </form>
      {f.author_id ? (
        <div className="filters-on">
          <span className="chip">
            Author: {author?.display_name ?? `#${f.author_id}`}
            <Link href={`/commits?${new URLSearchParams(Object.entries(keep).filter(([k]) => k !== "author_id"))}`} aria-label="Remove author filter">
              ×
            </Link>
          </span>
        </div>
      ) : null}

      {list.items.length === 0 ? (
        <div className="empty">
          <strong>{filtered ? "No commits match these filters" : "No commits yet"}</strong>
          {filtered ? "Clear a filter to see more." : "Commits appear after Bitbucket sends a push webhook or the poller reads a repository."}
        </div>
      ) : (
        <ul className="rows">
          <li className="item hd cols-review" aria-hidden="true">
            <span>Score</span>
            <span>Commit</span>
            <span>Review</span>
            <span>Fix</span>
            <span className="r">Findings</span>
            <span className="r">When</span>
          </li>
          {list.items.map((c) => (
            <li key={c.id} className="item cols-review">
              <Score value={c.score} />
              <div>
                <Link href={`/commits/${c.id}`} className="title">
                  {c.subject || "(no message)"}
                </Link>
                <div className="meta">
                  <span className="mono">{c.hash.slice(0, 8)}</span>
                  <span>{c.repository.full_name}</span>
                  {c.branch ? <span>{c.branch}</span> : null}
                  {c.is_merge ? <span>Merge</span> : null}
                  {c.author.id ? <Link href={`/users/${c.author.id}`}>{c.author.name}</Link> : <span>{c.author.name || "unknown author"}</span>}
                </div>
              </div>
              <div className="side">
                <StatusBadge status={c.review_status} reason={c.review_skip_reason} />
                {c.review_status === "skipped" && c.review_skip_reason ? <div className="muted" style={{ fontSize: ".8rem" }}>{c.review_skip_reason}</div> : null}
              </div>
              <div className="side">
                <FixMark reviewed={c.reviewed_at !== null} findings={c.findings} open={c.open_findings} closed={c.review_closed} />
              </div>
              <div className="side r">
                {c.score === null ? <DiffStat files={c.files_changed} additions={c.additions} deletions={c.deletions} /> : `${c.findings} ${c.findings === 1 ? "finding" : "findings"}`}
              </div>
              <time className="r" title={dateTime(c.committed_at)} dateTime={c.committed_at}>
                {ago(c.committed_at)}
              </time>
            </li>
          ))}
        </ul>
      )}
      <Pager base="/commits" keep={keep} page={page} size={size} total={list.total} noun="commits" />
    </>
  );
}
