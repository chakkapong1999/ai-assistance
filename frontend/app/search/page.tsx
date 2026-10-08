import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, one } from "@/lib/format";
import Score from "@/components/Score";
import { StatusBadge } from "@/components/badges";

export const metadata = { title: "Search" };

const SHOW = 5;
const enc = (q: string) => encodeURIComponent(q);

export default async function Search({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const q = one((await searchParams).q).trim().slice(0, 200);

  if (!q) {
    return (
      <>
        <div className="head">
          <h1>Search</h1>
        </div>
        <div className="empty">
          <strong>Type something to search</strong>
          Looks through commit messages and hashes, pull request titles, branches and numbers, repository names and people.
        </div>
      </>
    );
  }

  const [[commits, p1], [prs, p2], [repos, p3], [people, p4]] = await Promise.all([
    guard(api.commits({ q, limit: SHOW })),
    guard(api.pullRequests({ q, limit: SHOW })),
    guard(api.repositories({ q, limit: SHOW })),
    guard(api.users({ q, days: 365, limit: SHOW })),
  ]);
  if (!commits || !prs || !repos || !people) return p1 ?? p2 ?? p3 ?? p4;

  const none = commits.items.length + prs.items.length + repos.items.length + people.items.length === 0;

  return (
    <>
      <div className="head">
        <h1>Search</h1>
      </div>
      <p className="intro">
        Results for <b>{q}</b>. Each list shows the first {SHOW}; open the full list to see the rest.
      </p>
      {none ? (
        <div className="empty">
          <strong>Nothing found</strong>
          Try a shorter word, a commit hash, a branch name or a repository name.
        </div>
      ) : null}

      {commits.items.length > 0 ? (
        <section className="results" aria-label="Commits">
          <h2>
            Commits
            <Link href={`/commits?q=${enc(q)}`}>All matching commits</Link>
          </h2>
          <ul className="rows">
            {commits.items.map((c) => (
              <li key={c.id} className="item cols-hit">
                <Score value={c.score} />
                <div>
                  <Link href={`/commits/${c.id}`} className="title">
                    {c.subject || "(no message)"}
                  </Link>
                  <div className="meta">
                    <span className="mono">{c.hash.slice(0, 8)}</span>
                    <span>{c.repository.full_name}</span>
                    <span>{c.author.name || "unknown author"}</span>
                  </div>
                </div>
                <div className="side">
                  <StatusBadge status={c.review_status} reason={c.review_skip_reason} />
                </div>
                <time className="r" dateTime={c.committed_at}>
                  {ago(c.committed_at)}
                </time>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {prs.items.length > 0 ? (
        <section className="results" aria-label="Pull requests">
          <h2>
            Pull requests
            <Link href={`/pull-requests?q=${enc(q)}`}>All matching pull requests</Link>
          </h2>
          <ul className="rows">
            {prs.items.map((p) => (
              <li key={p.id} className="item cols-hit">
                <Score value={p.score} />
                <div>
                  <Link href={`/pull-requests/${p.id}`} className="title">
                    {p.title}
                  </Link>
                  <div className="meta">
                    <span>#{p.number}</span>
                    <span>{p.repository.full_name}</span>
                    <span>{p.author.name || "unknown author"}</span>
                  </div>
                </div>
                <div className="side">
                  <StatusBadge status={p.review_status} reason={p.review_skip_reason} />
                </div>
                <time className="r" dateTime={p.updated_at}>
                  {ago(p.updated_at)}
                </time>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {repos.items.length > 0 ? (
        <section className="results" aria-label="Repositories">
          <h2>
            Repositories
            <Link href={`/repositories?q=${enc(q)}`}>All matching repositories</Link>
          </h2>
          <ul className="rows">
            {repos.items.map((r) => (
              <li key={r.id} className="item cols-hit">
                <Score value={r.avg_score} />
                <div>
                  <Link href={`/commits?repo_id=${r.id}`} className="title">
                    {r.full_name}
                  </Link>
                </div>
                <div className="side">
                  <span className={`mark ${r.review_enabled ? "good" : "idle"}`}>{r.review_enabled ? "Review on" : "Review off"}</span>
                </div>
                <time className="r">{r.last_commit_at ? ago(r.last_commit_at) : "No commits"}</time>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {people.items.length > 0 ? (
        <section className="results" aria-label="People">
          <h2>
            People
            <Link href={`/users?q=${enc(q)}`}>All matching people</Link>
          </h2>
          <ul className="rows">
            {people.items.map((u) => (
              <li key={u.id} className="item cols-hit">
                <Score value={u.avg_score} />
                <div>
                  <Link href={`/users/${u.id}`} className="title">
                    {u.display_name}
                  </Link>
                </div>
                <div className="side">{u.commits} commits</div>
                <span />
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}
