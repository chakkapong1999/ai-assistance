import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { intParam, num, one } from "@/lib/format";
import { ScoreBadge } from "@/components/badges";
import Window from "@/components/Window";

export const dynamic = "force-dynamic";

const sorts = [
  ["commits", "Commits"],
  ["score", "Score"],
  ["name", "Name"],
] as const;

export default async function Users({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const sp = await searchParams;
  const days = intParam(sp.days, 30, 1, 365);
  const q = one(sp.q);
  const sort = sorts.some(([k]) => k === one(sp.sort)) ? one(sp.sort) : "commits";
  const [list, problem] = await guard(api.users({ days, q, sort, limit: 200 }));
  if (!list) return problem;
  const showEmail = list.items.some((u) => u.email);

  return (
    <>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <div>
          <h1>People</h1>
          <p className="sub">Commit authors, last {days} days. Score is the mean of each commit&apos;s latest review.</p>
        </div>
        <Window base="/users" days={days} extra={{ sort, ...(q ? { q } : {}) }} />
      </div>
      <form className="filters" action="/users">
        <input type="hidden" name="days" value={days} />
        <label>
          Search
          <input name="q" defaultValue={q} placeholder="name" />
        </label>
        <label>
          Sort by
          <select name="sort" defaultValue={sort}>
            {sorts.map(([k, l]) => (
              <option key={k} value={k}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <button className="primary">Apply</button>
      </form>
      {list.items.length === 0 ? (
        <div className="empty">No people yet. Authors are created from push webhooks.</div>
      ) : (
        <div className="tablewrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                {showEmail ? <th>Email</th> : null}
                <th className="num">Commits</th>
                <th className="num">Reviewed</th>
                <th className="num">Avg score</th>
                <th className="num">Findings</th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((u) => (
                <tr key={u.id}>
                  <td>
                    <Link href={`/users/${u.id}?days=${days}`}>{u.display_name}</Link>
                    {!u.linked ? <span className="muted"> · not linked to Bitbucket</span> : null}
                    {u.job_title ? <div className="muted">{u.job_title}</div> : null}
                  </td>
                  {showEmail ? <td>{u.email ?? ""}</td> : null}
                  <td className="num">{num(u.commits)}</td>
                  <td className="num">{num(u.reviewed)}</td>
                  <td className="num">
                    <ScoreBadge value={u.avg_score} />
                  </td>
                  <td className="num">{num(u.findings)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="muted">{list.total} people in total.</p>
    </>
  );
}
