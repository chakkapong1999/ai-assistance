import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, num, one } from "@/lib/format";
import { ScoreBadge } from "@/components/badges";
import { toggleReview } from "./actions";

export const dynamic = "force-dynamic";

export default async function Repositories({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const sp = await searchParams;
  const q = one(sp.q);
  const notice = one(sp.notice);
  const [[list, problem], [me]] = await Promise.all([guard(api.repositories({ q, limit: 200 })), guard(api.me())]);
  if (!list) return problem;
  const admin = me?.role === "admin";
  const enabled = list.items.filter((r) => r.review_enabled).length;

  return (
    <>
      <h1>Repositories</h1>
      <p className="sub">
        Repositories appear when Bitbucket first sends a push. Review is <strong>off</strong> until you turn it on, because enabling it sends that repository&apos;s code to an LLM.
      </p>
      {notice ? (
        <p className="banner" role="status">
          {notice}
        </p>
      ) : null}
      <form className="filters" action="/repositories">
        <label>
          Search
          <input name="q" defaultValue={q} placeholder="workspace/repo" />
        </label>
        <button className="primary">Search</button>
        {q ? <Link href="/repositories">Reset</Link> : null}
      </form>

      {list.items.length === 0 ? (
        <div className="empty">{q ? "No repository matches." : "No repositories yet. They are created from the first push webhook."}</div>
      ) : (
        <div className="tablewrap">
          <table>
            <thead>
              <tr>
                <th>Repository</th>
                <th>Project</th>
                <th className="num">Commits</th>
                <th className="num">Reviewed</th>
                <th className="num">Avg score</th>
                <th>Last commit</th>
                <th>Review</th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((r) => (
                <tr key={r.id}>
                  <td>
                    <Link href={`/commits?repo_id=${r.id}`}>{r.full_name}</Link>
                    <div className="muted">{[r.main_language, r.default_branch].filter(Boolean).join(" · ")}</div>
                  </td>
                  <td>{r.project_key === "NONE" ? <span className="muted">–</span> : r.project_name}</td>
                  <td className="num">{num(r.commits)}</td>
                  <td className="num">{num(r.reviewed)}</td>
                  <td className="num">
                    <ScoreBadge value={r.avg_score} />
                  </td>
                  <td>{ago(r.last_commit_at)}</td>
                  <td>
                    {admin ? (
                      <form action={toggleReview} className="row" style={{ gap: 8 }}>
                        <input type="hidden" name="id" value={r.id} />
                        <input type="hidden" name="enable" value={String(!r.review_enabled)} />
                        <span className={`badge ${r.review_enabled ? "good" : ""}`}>{r.review_enabled ? "on" : "off"}</span>
                        <button className={r.review_enabled ? "danger" : "primary"}>{r.review_enabled ? "Turn off" : "Turn on"}</button>
                      </form>
                    ) : (
                      <span className={`badge ${r.review_enabled ? "good" : ""}`}>{r.review_enabled ? "on" : "off"}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="muted">
        {enabled} of {list.total} repositories are reviewed. Turning review off keeps past reviews; commits pushed while it is off are marked skipped and are not reviewed later.
      </p>
    </>
  );
}
