import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, dateTime, one } from "@/lib/format";
import { PRStateBadge, ScoreBadge, StatusBadge } from "@/components/badges";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;
const keys = ["q", "state", "review_status", "repo_id", "author_id"] as const;

export default async function PullRequests({ searchParams }: { searchParams: Promise<SP> }) {
  const sp = await searchParams;
  const f = Object.fromEntries(keys.map((k) => [k, one(sp[k])])) as Record<(typeof keys)[number], string>;
  const cursor = one(sp.cursor);

  const [[list, p1], [repos, p2]] = await Promise.all([
    guard(api.pullRequests({ ...f, cursor, limit: 50 })),
    guard(api.repositories({ limit: 200 })),
  ]);
  if (!list || !repos) return p1 ?? p2;

  const author = f.author_id ? (await guard(api.user(Number(f.author_id), 1)))[0] : null;
  const keep = Object.fromEntries(Object.entries(f).filter(([, v]) => v));
  const nextHref = list.next_cursor ? `/pull-requests?${new URLSearchParams({ ...keep, cursor: list.next_cursor })}` : null;

  return (
    <>
      <h1>Pull requests</h1>
      <p className="sub">
        Each pull request is reviewed as one diff, all its commits together, and again whenever its branch gets a new commit.
        Commits keep their own reviews on the Commits page.
      </p>

      <form className="filters" action="/pull-requests">
        <label>
          Search
          <input name="q" defaultValue={f.q} placeholder="title, branch or number" />
        </label>
        <label>
          Repository
          <select name="repo_id" defaultValue={f.repo_id}>
            <option value="">All</option>
            {repos.items.map((r) => (
              <option key={r.id} value={r.id}>
                {r.full_name}
              </option>
            ))}
          </select>
        </label>
        <label>
          State
          <select name="state" defaultValue={f.state}>
            <option value="">Any</option>
            {["OPEN", "MERGED", "DECLINED", "SUPERSEDED", "DELETED"].map((s) => (
              <option key={s} value={s}>
                {s.toLowerCase()}
              </option>
            ))}
          </select>
        </label>
        <label>
          Review
          <select name="review_status" defaultValue={f.review_status}>
            <option value="">Any</option>
            {["done", "pending", "running", "skipped", "failed"].map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        </label>
        {f.author_id ? <input type="hidden" name="author_id" value={f.author_id} /> : null}
        <button className="primary">Filter</button>
        <Link href="/pull-requests">Reset</Link>
      </form>
      {f.author_id ? (
        <p>
          <span className="chip">
            Author: {author?.display_name ?? `#${f.author_id}`}{" "}
            <Link
              href={`/pull-requests?${new URLSearchParams(Object.fromEntries(Object.entries(keep).filter(([k]) => k !== "author_id")))}`}
              aria-label="Remove author filter"
            >
              ×
            </Link>
          </span>
        </p>
      ) : null}

      {list.items.length === 0 ? (
        <div className="empty">
          No pull requests match. They appear after the poller reads a repository (set <code>POLL_REPOS</code> on the worker).
        </div>
      ) : (
        <div className="tablewrap">
          <table>
            <thead>
              <tr>
                <th>Pull request</th>
                <th>Repository</th>
                <th>Author</th>
                <th>Updated</th>
                <th>State</th>
                <th>Review</th>
                <th className="num">Score</th>
                <th className="num">Findings</th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((p) => (
                <tr key={p.id}>
                  <td>
                    <Link href={`/pull-requests/${p.id}`}>{p.title || <em>(no title)</em>}</Link>
                    <div className="muted">
                      #{p.number}
                      {p.source_branch ? ` · ${p.source_branch} → ${p.destination_branch ?? "?"}` : ""}
                    </div>
                  </td>
                  <td>{p.repository.full_name}</td>
                  <td>{p.author.id ? <Link href={`/users/${p.author.id}`}>{p.author.name}</Link> : p.author.name || "–"}</td>
                  <td title={dateTime(p.updated_at)}>{ago(p.updated_at)}</td>
                  <td>
                    <PRStateBadge state={p.state} />
                  </td>
                  <td>
                    <StatusBadge status={p.review_status} reason={p.review_skip_reason} />
                    {p.review_outdated ? <div className="muted">new commits since</div> : null}
                    {p.review_status === "skipped" && p.review_skip_reason ? <div className="muted">{p.review_skip_reason}</div> : null}
                  </td>
                  <td className="num">
                    <ScoreBadge value={p.score} />
                  </td>
                  <td className="num">{p.score === null ? "–" : p.findings}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="pager">
        <span className="muted">{list.items.length} shown</span>
        {nextHref ? <Link href={nextHref}>Older pull requests →</Link> : <span className="muted">End of list</span>}
      </div>
    </>
  );
}
