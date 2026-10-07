import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, dateTime, one } from "@/lib/format";
import Score from "@/components/Score";
import Segments from "@/components/Segments";
import { DiffStat, PRStateBadge, StatusBadge } from "@/components/badges";

export const metadata = { title: "Pull requests" };

type SP = Record<string, string | string[] | undefined>;
const keys = ["q", "state", "review_status", "repo_id", "author_id"] as const;

export default async function PullRequests({ searchParams }: { searchParams: Promise<SP> }) {
  const sp = await searchParams;
  const f = Object.fromEntries(keys.map((k) => [k, one(sp[k])])) as Record<(typeof keys)[number], string>;
  const cursor = one(sp.cursor);

  const [[list, p1], [repos, p2]] = await Promise.all([guard(api.pullRequests({ ...f, cursor, limit: 50 })), guard(api.repositories({ limit: 200 }))]);
  if (!list || !repos) return p1 ?? p2;

  const author = f.author_id ? (await guard(api.user(Number(f.author_id), 1)))[0] : null;
  const keep = Object.fromEntries(Object.entries(f).filter(([, v]) => v));
  const nextHref = list.next_cursor ? `/pull-requests?${new URLSearchParams({ ...keep, cursor: list.next_cursor })}` : null;
  const filtered = Object.keys(keep).length > 0;

  return (
    <>
      <div className="head">
        <h1>Pull requests</h1>
      </div>
      <p className="intro">Each pull request is reviewed as one diff, and again whenever its branch gets a new commit.</p>

      <form className="toolbar" action="/pull-requests">
        <Segments
          base="/pull-requests"
          param="state"
          current={f.state}
          keep={keep}
          label="Pull request state"
          options={[
            ["", "All"],
            ["OPEN", "Open"],
            ["MERGED", "Merged"],
            ["DECLINED", "Declined"],
          ]}
        />
        {f.state ? <input type="hidden" name="state" value={f.state} /> : null}
        {f.author_id ? <input type="hidden" name="author_id" value={f.author_id} /> : null}
        <label>
          Search
          <input type="search" name="q" defaultValue={f.q} placeholder="Title, branch or number" />
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
          Review
          <select name="review_status" defaultValue={f.review_status}>
            <option value="">Any</option>
            <option value="done">Reviewed</option>
            <option value="failed">Failed</option>
            <option value="pending">Waiting</option>
            <option value="running">Reviewing</option>
            <option value="skipped">Skipped</option>
          </select>
        </label>
        <button className="primary">Apply</button>
        {filtered ? <Link href="/pull-requests">Clear filters</Link> : null}
      </form>
      {f.author_id ? (
        <div className="filters-on">
          <span className="chip">
            Author: {author?.display_name ?? `#${f.author_id}`}
            <Link href={`/pull-requests?${new URLSearchParams(Object.entries(keep).filter(([k]) => k !== "author_id"))}`} aria-label="Remove author filter">
              ×
            </Link>
          </span>
        </div>
      ) : null}

      {list.items.length === 0 ? (
        <div className="empty">
          <strong>{filtered ? "No pull requests match these filters" : "No pull requests yet"}</strong>
          {filtered ? "Clear a filter to see more." : "They appear after the poller reads a repository. Set POLL_REPOS on the worker."}
        </div>
      ) : (
        <ul className="rows">
          <li className="item hd cols-review" aria-hidden="true">
            <span>Score</span>
            <span>Pull request</span>
            <span>Status</span>
            <span className="r">Findings</span>
            <span className="r">Updated</span>
          </li>
          {list.items.map((p) => (
            <li key={p.id} className="item cols-review">
              <Score value={p.score} />
              <div>
                <Link href={`/pull-requests/${p.id}`} className="title">
                  {p.title || "(no title)"}
                </Link>
                <div className="meta">
                  <span>#{p.number}</span>
                  <span>{p.repository.full_name}</span>
                  {p.source_branch ? (
                    <span>
                      {p.source_branch} into {p.destination_branch ?? "?"}
                    </span>
                  ) : null}
                  {p.author.id ? <Link href={`/users/${p.author.id}`}>{p.author.name}</Link> : <span>{p.author.name || "unknown author"}</span>}
                </div>
              </div>
              <div className="side">
                <PRStateBadge state={p.state} />
                <StatusBadge status={p.review_status} reason={p.review_skip_reason} />
                {p.review_outdated ? <div className="muted" style={{ fontSize: ".8rem" }}>New commits since review</div> : null}
              </div>
              <div className="side r">
                {p.score === null ? <DiffStat files={p.files_changed} additions={p.additions} deletions={p.deletions} /> : `${p.findings} ${p.findings === 1 ? "finding" : "findings"}`}
              </div>
              <time className="r" title={dateTime(p.updated_at)} dateTime={p.updated_at}>
                {ago(p.updated_at)}
              </time>
            </li>
          ))}
        </ul>
      )}
      <div className="pager">
        <span>{list.items.length} shown</span>
        {nextHref ? <Link href={nextHref}>Older pull requests</Link> : <span>End of list</span>}
      </div>
    </>
  );
}
