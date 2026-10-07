import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, dateTime, one } from "@/lib/format";
import { ScoreBadge, StatusBadge } from "@/components/badges";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;
const keys = ["q", "status", "repo_id", "author_id", "branch"] as const;

export default async function Commits({ searchParams }: { searchParams: Promise<SP> }) {
  const sp = await searchParams;
  const f = Object.fromEntries(keys.map((k) => [k, one(sp[k])])) as Record<(typeof keys)[number], string>;
  const cursor = one(sp.cursor);

  const [[list, p1], [repos, p2]] = await Promise.all([
    guard(api.commits({ ...f, cursor, limit: 50 })),
    guard(api.repositories({ limit: 200 })),
  ]);
  if (!list || !repos) return p1 ?? p2;

  const author = f.author_id ? (await guard(api.user(Number(f.author_id), 1)))[0] : null;
  const nextHref = list.next_cursor ? `/commits?${new URLSearchParams({ ...Object.fromEntries(Object.entries(f).filter(([, v]) => v)), cursor: list.next_cursor })}` : null;

  return (
    <>
      <h1>Commits</h1>
      <p className="sub">Newest first, every branch.</p>

      <form className="filters" action="/commits">
        <label>
          Search
          <input name="q" defaultValue={f.q} placeholder="message or hash" />
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
          Status
          <select name="status" defaultValue={f.status}>
            <option value="">Any</option>
            {["done", "pending", "running", "skipped", "failed"].map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        </label>
        <label>
          Branch
          <input name="branch" defaultValue={f.branch} placeholder="exact name" />
        </label>
        {f.author_id ? <input type="hidden" name="author_id" value={f.author_id} /> : null}
        <button className="primary">Filter</button>
        <Link href="/commits">Reset</Link>
      </form>
      {f.author_id ? (
        <p>
          <span className="chip">
            Author: {author?.display_name ?? `#${f.author_id}`}{" "}
            <Link href={`/commits?${new URLSearchParams(Object.fromEntries(Object.entries(f).filter(([k, v]) => v && k !== "author_id")))}`} aria-label="Remove author filter">
              ×
            </Link>
          </span>
        </p>
      ) : null}

      {list.items.length === 0 ? (
        <div className="empty">No commits match. Commits appear here after Bitbucket sends a push webhook.</div>
      ) : (
        <div className="tablewrap">
          <table>
            <thead>
              <tr>
                <th>Commit</th>
                <th>Repository</th>
                <th>Author</th>
                <th>When</th>
                <th>Status</th>
                <th className="num">Score</th>
                <th className="num">Findings</th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((c) => (
                <tr key={c.id}>
                  <td>
                    <Link href={`/commits/${c.id}`}>{c.subject || <em>(no message)</em>}</Link>
                    <div className="muted">
                      <span className="hash">{c.hash.slice(0, 8)}</span>
                      {c.branch ? ` · ${c.branch}` : ""}
                      {c.is_merge ? " · merge" : ""}
                    </div>
                  </td>
                  <td>{c.repository.full_name}</td>
                  <td>{c.author.id ? <Link href={`/users/${c.author.id}`}>{c.author.name}</Link> : c.author.name || "–"}</td>
                  <td title={dateTime(c.committed_at)}>{ago(c.committed_at)}</td>
                  <td>
                    <StatusBadge status={c.review_status} reason={c.review_skip_reason} />
                    {c.review_status === "skipped" && c.review_skip_reason ? <div className="muted">{c.review_skip_reason}</div> : null}
                  </td>
                  <td className="num">
                    <ScoreBadge value={c.score} />
                  </td>
                  <td className="num">{c.score === null ? "–" : c.findings}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="pager">
        <span className="muted">{list.items.length} shown</span>
        {nextHref ? <Link href={nextHref}>Older commits →</Link> : <span className="muted">End of list</span>}
      </div>
    </>
  );
}
