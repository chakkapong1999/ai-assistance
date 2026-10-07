import Link from "next/link";
import { notFound } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { intParam, num } from "@/lib/format";
import BarChart from "@/components/BarChart";
import Score from "@/components/Score";
import { Parts } from "@/components/badges";
import Window from "@/components/Window";

export async function generateMetadata({ params }: { params: Promise<{ id: string }> }) {
  return { title: `Person ${(await params).id}` };
}

export default async function UserPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  const days = intParam((await searchParams).days, 30, 1, 365);
  const [u, problem] = await guard(api.user(id, days));
  if (!u) return problem;

  return (
    <>
      <div className="head">
        <nav className="crumbs" aria-label="Breadcrumb">
          <Link href="/users">People</Link>
        </nav>
        <Window base={`/users/${id}`} days={days} />
      </div>
      <header className="subject">
        <h1>{u.display_name}</h1>
        <div className="meta"><Parts items={[u.job_title, u.department, u.email]} empty={u.linked ? "Bitbucket account" : "Known only from commit author names"} /></div>
      </header>

      <section className="verdict" aria-label={`Last ${days} days`}>
        <Score value={u.avg_score} large />
        <div className="kpis">
          <div className="kpi">
            <Link href={`/commits?author_id=${u.id}`} className="label">
              Commits
            </Link>
            <div className="big">{num(u.commits)}</div>
          </div>
          <div className="kpi">
            <div className="label">Reviewed</div>
            <div className="big">{num(u.reviewed)}</div>
          </div>
          <div className="kpi">
            <div className="label">Findings</div>
            <div className="big">{num(u.findings)}</div>
          </div>
        </div>
      </section>

      <section className="section" aria-labelledby="weekly">
        <header>
          <h2 id="weekly">Commits per week</h2>
          <p>Grey is every commit. The coloured part was reviewed: green for a week averaging 90 and up, amber 70 to 89, red below 70. Weeks start on Monday (UTC); empty weeks are left out.</p>
        </header>
        {u.trend.length === 0 ? (
          <div className="empty">No commits in this window.</div>
        ) : (
          <div className="chartbox">
            <BarChart
              title="Commits per week"
              bars={u.trend.map((w) => ({ label: w.week.slice(5), value: w.commits, value2: w.reviewed, score: w.avg_score, tip: `Week of ${w.week}: ${w.commits} commits, ${w.reviewed} reviewed${w.avg_score !== null ? `, average score ${w.avg_score}` : ""}` }))}
            />
          </div>
        )}
      </section>
    </>
  );
}
