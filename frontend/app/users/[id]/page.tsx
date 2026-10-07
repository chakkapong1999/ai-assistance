import Link from "next/link";
import { notFound } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { intParam, num, score } from "@/lib/format";
import BarChart from "@/components/BarChart";
import Window from "@/components/Window";

export const dynamic = "force-dynamic";

export default async function UserPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  const days = intParam((await searchParams).days, 30, 1, 365);
  const [u, problem] = await guard(api.user(id, days));
  if (!u) return problem;

  return (
    <>
      <p>
        <Link href="/users">← People</Link>
      </p>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <div>
          <h1>{u.display_name}</h1>
          <p className="sub">
            {[u.job_title, u.department, u.email].filter(Boolean).join(" · ") || (u.linked ? "Bitbucket account" : "Known only from commit author names")}
          </p>
        </div>
        <Window base={`/users/${id}`} days={days} />
      </div>
      <section className="cards">
        <div className="card">
          <div className="label">Commits</div>
          <div className="value">{num(u.commits)}</div>
        </div>
        <div className="card">
          <div className="label">Reviewed</div>
          <div className="value">{num(u.reviewed)}</div>
        </div>
        <div className="card">
          <div className="label">Average score</div>
          <div className="value">{score(u.avg_score)}</div>
        </div>
        <div className="card">
          <div className="label">Findings</div>
          <div className="value">{num(u.findings)}</div>
        </div>
      </section>
      <p>
        <Link href={`/commits?author_id=${u.id}`}>See all commits by {u.display_name} →</Link>
      </p>
      <h2>Commits per week</h2>
      {u.trend.length === 0 ? (
        <div className="empty">No commits in this window.</div>
      ) : (
        <section className="panel">
          <p className="muted" style={{ margin: "0 0 6px", fontSize: ".8rem" }}>
            Week starting Monday (UTC). Dark = reviewed, light = all commits. Weeks without commits are left out.
          </p>
          <BarChart
            title="Commits per week"
            bars={u.trend.map((w) => ({ label: w.week.slice(5), value: w.commits, value2: w.reviewed, tip: `Week of ${w.week}: ${w.commits} commits, ${w.reviewed} reviewed${w.avg_score !== null ? `, avg score ${w.avg_score}` : ""}` }))}
          />
        </section>
      )}
    </>
  );
}
