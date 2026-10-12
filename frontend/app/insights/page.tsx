import Link from "next/link";
import { api } from "@/lib/api";
import type { Severity } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, compact, intParam, num, usd, plural } from "@/lib/format";
import BarChart from "@/components/BarChart";
import Score from "@/components/Score";
import Window from "@/components/Window";

export const metadata = { title: "Insights" };

const sevs: Severity[] = ["critical", "major", "minor", "info"];

// Trends and cost. The Overview keeps only what needs someone.
export default async function Insights({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const days = intParam((await searchParams).days, 30, 1, 365);
  const [[o, problem], [recent], [health]] = await Promise.all([guard(api.overview(days)), guard(api.commits({ status: "done", limit: 6 })), guard(api.health())]);
  if (!o) return problem;

  const u = o.usage;
  const sev = o.findings_by_severity;
  const totalFindings = sevs.reduce((n, s) => n + sev[s], 0);
  const label = (d: string) => d.slice(5);
  // ?? : an API older than the dashboard
  const cats = Object.entries(o.findings_by_category ?? {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));

  return (
    <>
      <div className="head">
        <h1>Insights</h1>
        <Window base="/insights" days={days} />
      </div>

      <div className="split">
        <section className="block" aria-labelledby="perday">
          <header style={{ display: "block" }}>
            <h2 id="perday">Commits per day</h2>
            <p>Grey is every commit. The coloured part was reviewed: green for a day averaging 90 and up, amber 70 to 89, red below 70.</p>
          </header>
          <div className="chartbox">
            <BarChart
              title={`Commits per day, last ${days} days`}
              bars={o.series.map((p) => ({ label: label(p.day), value: p.commits, value2: p.reviewed, score: p.avg_score, tip: `${p.day}: ${p.commits} commits, ${p.reviewed} reviewed${p.avg_score !== null ? `, average score ${p.avg_score}` : ""}` }))}
            />
          </div>
        </section>

        <section className="block" aria-labelledby="sev">
          <header style={{ display: "block" }}>
            <h2 id="sev">Findings by severity</h2>
            <p>From the latest review of each commit.</p>
          </header>
          <div>
            {totalFindings > 0 ? (
              <div className="stack" role="img" aria-label={sevs.map((s) => `${sev[s]} ${s}`).join(", ")}>
                {sevs.filter((s) => sev[s] > 0).map((s) => (
                  <i key={s} className={`tone-${s}`} style={{ flexGrow: sev[s] }} />
                ))}
              </div>
            ) : (
              <div className="empty">No findings in this window.</div>
            )}
            <ul className="legend">
              {sevs.map((s) => (
                <li key={s}>
                  <span className={`sev sev-${s}`}>{s[0].toUpperCase() + s.slice(1)}</span>
                  <span className="num">{num(sev[s])}</span>
                </li>
              ))}
            </ul>
          </div>
        </section>
      </div>

      {totalFindings > 0 ? (
        <div className="split section">
          <section className="block" aria-labelledby="byrepo">
            <header style={{ display: "block" }}>
              <h2 id="byrepo">Where the findings are</h2>
              <p>Repositories with the most critical, then major findings.</p>
            </header>
            <ul className="legend">
              {(o.top_repositories ?? []).map((r) => (
                <li key={r.id}>
                  <Link href={`/commits?repo_id=${r.id}`}>{r.full_name}</Link>
                  <span className="num">
                    {r.critical > 0 ? <strong className="crit">{num(r.critical)} critical · </strong> : null}
                    {r.major > 0 ? `${num(r.major)} major · ` : null}
                    {plural(r.findings, "finding")}
                  </span>
                </li>
              ))}
            </ul>
          </section>

          <section className="block" aria-labelledby="bycat">
            <header style={{ display: "block" }}>
              <h2 id="bycat">Findings by kind</h2>
              <p>What the reviewer flagged, from the same reviews.</p>
            </header>
            <ul className="legend">
              {cats.map(([c, n]) => (
                <li key={c}>
                  <span>{c[0].toUpperCase() + c.slice(1)}</span>
                  <span className="num">{num(n)}</span>
                </li>
              ))}
            </ul>
          </section>
        </div>
      ) : null}

      <div className="split section">
        <section className="block" aria-labelledby="recent">
          <header>
            <h2 id="recent">Recent reviews</h2>
            <Link href="/commits?status=done">All reviewed commits</Link>
          </header>
          {!recent || recent.items.length === 0 ? (
            <div className="empty">
              <strong>Nothing reviewed yet</strong>
              Reviews appear here after a commit is pushed to a repository with review turned on.
            </div>
          ) : (
            <ul className="rows">
              <li className="item hd cols-recent" aria-hidden="true">
                <span>Score</span>
                <span>Commit</span>
                <span>When</span>
              </li>
              {recent.items.map((c) => (
                <li key={c.id} className="item cols-recent">
                  <Score value={c.score} />
                  <div>
                    <Link href={`/commits/${c.id}`} className="title">
                      {c.subject || "(no message)"}
                    </Link>
                    <div className="meta">
                      <span>{c.repository.full_name}</span>
                      <span>{c.author.name || "unknown author"}</span>
                      <span>{plural(c.findings, "finding")}</span>
                    </div>
                  </div>
                  <time>{ago(c.committed_at)}</time>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="block" aria-labelledby="ledger">
          <header>
            <h2 id="ledger">Cost and capacity</h2>
          </header>
          <div>
            <dl className="ledger">
              <dt>Review cost</dt>
              <dd>{usd(u.cost_usd)}</dd>
              {u.wasted.runs > 0 ? (
                <>
                  <dt>Failed attempts</dt>
                  <dd>
                    {u.wasted.measured_runs > 0 ? `${usd(u.wasted.cost_usd)} in ` : ""}
                    {plural(u.wasted.runs, "attempt")}, included above
                  </dd>
                </>
              ) : null}
              <dt>Cost per run</dt>
              <dd>{u.avg_cost_usd !== null ? usd(u.avg_cost_usd) : "not measured"}</dd>
              <dt>Tokens in / out</dt>
              <dd>
                {compact(u.tokens_in)} / {compact(u.tokens_out)}
              </dd>
              <dt>Review runs</dt>
              <dd>
                {num(u.runs)}
                {u.runs > u.measured_runs ? ` (${num(u.runs - u.measured_runs)} without usage)` : ""}
              </dd>
              <dt>Queue</dt>
              <dd>
                {o.queue_running} running, {o.queue_waiting} waiting
              </dd>
              <dt>Repositories reviewed</dt>
              <dd>
                <Link href="/repositories">
                  {o.enabled_repositories} of {o.total_repositories}
                </Link>
              </dd>
              <dt>Active authors</dt>
              <dd>{num(o.active_authors)}</dd>
              {health ? (
                <>
                  <dt>Pipeline</dt>
                  <dd>{health.status === "ok" ? "Healthy" : "Needs a look"}</dd>
                  <dt>Worker last checked</dt>
                  <dd>{health.last_reconcile ? ago(health.last_reconcile.at) : "not yet"}</dd>
                  <dt>Last poll</dt>
                  <dd>{health.last_poll ? `${ago(health.last_poll.at)}${health.last_poll.ok ? "" : ", failed"}` : "polling is off"}</dd>
                </>
              ) : null}
            </dl>
            <p className="muted" style={{ marginTop: 16, fontSize: 12.5, maxWidth: "46ch" }}>
              A commit starts at 100 and loses 15 for each critical, 7 for each major and 2 for each minor finding. Scores of 90 and up show green, 70 to 89 amber, below 70 red.
            </p>
          </div>
        </section>
      </div>
    </>
  );
}
