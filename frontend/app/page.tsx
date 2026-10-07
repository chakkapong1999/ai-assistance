import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { compact, intParam, num, score, usd } from "@/lib/format";
import BarChart from "@/components/BarChart";
import Window from "@/components/Window";
import { ScoreBadge } from "@/components/badges";

export const dynamic = "force-dynamic";

export default async function Overview({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const days = intParam((await searchParams).days, 30, 1, 365);
  const [o, problem] = await guard(api.overview(days));
  if (!o) return problem;

  const u = o.usage;
  const sev = o.findings_by_severity;
  const unmeasured = u.runs - u.measured_runs;
  const label = (d: string) => d.slice(5);

  return (
    <>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <div>
          <h1>Overview</h1>
          <p className="sub">Last {days} days (UTC), every branch.</p>
        </div>
        <Window base="/" days={days} />
      </div>

      <section className="cards" aria-label="Key numbers">
        <div className="card">
          <div className="label">Commits</div>
          <div className="value">{num(o.commits)}</div>
          <div className="note">
            {o.commits_by_status.done} reviewed · {o.commits_by_status.skipped} skipped
          </div>
        </div>
        <div className="card">
          <div className="label">Average score</div>
          <div className="value">{score(o.avg_score)}</div>
          <div className="note">over {num(o.reviewed)} reviewed commits</div>
        </div>
        <div className="card">
          <div className="label">Review cost</div>
          <div className="value">{usd(u.cost_usd)}</div>
          <div className="note">
            {u.avg_cost_usd !== null ? `${usd(u.avg_cost_usd)} per run` : "no usage measured"}
          </div>
        </div>
        <div className="card">
          <div className="label">Tokens in / out</div>
          <div className="value">
            {compact(u.tokens_in)} / {compact(u.tokens_out)}
          </div>
          <div className="note">
            {num(u.runs)} runs{unmeasured > 0 ? `, ${num(unmeasured)} without usage` : ""}
          </div>
        </div>
        <div className="card">
          <div className="label">Queue</div>
          <div className="value">{o.queue_waiting + o.queue_running}</div>
          <div className="note">
            {o.queue_running} running · {o.queue_waiting} waiting
          </div>
        </div>
        <div className="card">
          <div className="label">Repositories reviewed</div>
          <div className="value">
            {o.enabled_repositories} / {o.total_repositories}
          </div>
          <div className="note">{o.active_authors} active authors</div>
        </div>
      </section>

      {o.commits_by_status.failed > 0 || o.commits_by_status.pending > 0 ? (
        <p className="banner">
          {o.commits_by_status.failed > 0 ? <><a href="/commits?status=failed">{o.commits_by_status.failed} failed</a> </> : null}
          {o.commits_by_status.pending > 0 ? <><a href="/commits?status=pending">{o.commits_by_status.pending} pending</a></> : null}
        </p>
      ) : null}

      <div className="grid2">
        <section className="panel">
          <h2 style={{ marginTop: 0 }}>Commits per day</h2>
          <p className="muted" style={{ margin: "0 0 6px", fontSize: ".8rem" }}>
            Dark = reviewed, light = all commits
          </p>
          <BarChart
            title={`Commits per day, last ${days} days`}
            bars={o.series.map((p) => ({ label: label(p.day), value: p.commits, value2: p.reviewed, tip: `${p.day}: ${p.commits} commits, ${p.reviewed} reviewed${p.avg_score !== null ? `, avg score ${p.avg_score}` : ""}` }))}
          />
        </section>
        <section className="panel">
          <h2 style={{ marginTop: 0 }}>Review cost per day (USD)</h2>
          <p className="muted" style={{ margin: "0 0 6px", fontSize: ".8rem" }}>
            By when the review ran; re-reviews included
          </p>
          <BarChart
            title={`Review cost per day, last ${days} days`}
            bars={o.series.map((p) => ({ label: label(p.day), value: p.cost_usd, tip: `${p.day}: ${usd(p.cost_usd)}` }))}
          />
        </section>
      </div>

      <h2>Findings by severity</h2>
      <p className="muted" style={{ marginTop: 0 }}>From the latest review of each commit.</p>
      <div className="cards">
        {(["critical", "major", "minor", "info"] as const).map((s) => (
          <a key={s} className="card" href="/commits?status=done" style={{ color: "inherit" }}>
            <div className="label">{s}</div>
            <div className="value">{num(sev[s])}</div>
          </a>
        ))}
      </div>
      <p className="muted">
        Score of a commit is 100 − 15 per critical − 7 per major − 2 per minor finding (minimum 0). <ScoreBadge value={o.avg_score} /> is the mean over the window.
      </p>
    </>
  );
}
