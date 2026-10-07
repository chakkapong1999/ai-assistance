import Link from "next/link";
import { api } from "@/lib/api";
import type { Severity } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, compact, intParam, num, usd } from "@/lib/format";
import BarChart from "@/components/BarChart";
import Score from "@/components/Score";
import Window from "@/components/Window";

export const metadata = { title: "Overview" };

const sevs: Severity[] = ["critical", "major", "minor", "info"];
const plural = (n: number, word: string) => `${num(n)} ${n === 1 ? word : `${word}s`}`;

export default async function Overview({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const days = intParam((await searchParams).days, 30, 1, 365);
  const [[o, problem], [failedCommits], [failedPRs], [recent]] = await Promise.all([
    guard(api.overview(days)),
    guard(api.commits({ status: "failed", limit: 5 })),
    guard(api.pullRequests({ review_status: "failed", limit: 5 })),
    guard(api.commits({ status: "done", limit: 6 })),
  ]);
  if (!o) return problem;

  const u = o.usage;
  const sev = o.findings_by_severity;
  const totalFindings = sevs.reduce((n, s) => n + sev[s], 0);
  const failed = o.commits_by_status.failed;
  const waiting = o.commits_by_status.pending + o.commits_by_status.running;
  const label = (d: string) => d.slice(5);
  const attention = [
    ...(failedCommits?.items ?? []).map((c) => ({ key: `c${c.id}`, href: `/commits/${c.id}`, title: c.subject || "(no message)", where: c.repository.full_name, kind: "Commit", when: c.committed_at })),
    ...(failedPRs?.items ?? []).map((p) => ({ key: `p${p.id}`, href: `/pull-requests/${p.id}`, title: p.title || "(no title)", where: `${p.repository.full_name} #${p.number}`, kind: "Pull request", when: p.updated_at })),
  ];

  return (
    <>
      <div className="head">
        <h1>Overview</h1>
        <Window base="/" days={days} />
      </div>

      <section className="hero" aria-label={`Last ${days} days`}>
        {o.commits === 0 ? (
          <p>No commits in the last {days} days.</p>
        ) : (
          <p>
            <Link href="/commits">{plural(o.commits, "commit")}</Link> in the last {days} days,{" "}
            {o.reviewed === 0 ? (
              "none reviewed yet."
            ) : (
              <>
                <strong>{o.reviewed === o.commits ? "all" : num(o.reviewed)} reviewed</strong>.
                {o.avg_score !== null ? (
                  <>
                    {" "}They average <Score value={o.avg_score} /> with{" "}
                  </>
                ) : (
                  " They have "
                )}
                <strong>{totalFindings === 0 ? "no findings" : plural(totalFindings, "finding")}</strong>
                {sev.critical > 0 ? (
                  <>
                    , and <strong className="crit">{num(sev.critical)} critical</strong> {sev.critical === 1 ? "needs" : "need"} fixing.
                  </>
                ) : totalFindings > 0 ? (
                  ", none critical."
                ) : (
                  "."
                )}
              </>
            )}
          </p>
        )}
      </section>

      {failed > 0 || waiting > 0 || o.queue_waiting + o.queue_running > 0 ? (
        <section className="section" aria-labelledby="attn">
          <header>
            <h2 id="attn">{failed > 0 ? "Needs attention" : "In progress"}</h2>
            <p>
              {failed > 0 ? <Link href="/commits?status=failed">{num(failed)} failed commits</Link> : null}
              {failed > 0 && waiting > 0 ? ", " : null}
              {waiting > 0 ? <Link href="/commits?status=pending">{num(waiting)} waiting or running</Link> : null}
            </p>
          </header>
          {attention.length > 0 ? (
            <ul className="rows">
              <li className="item hd cols-attn" aria-hidden="true">
                <span>Status</span>
                <span>Item</span>
                <span>When</span>
              </li>
              {attention.map((a) => (
                <li key={a.key} className="item cols-attn">
                  <span className="mark bad">{a.kind} failed</span>
                  <div>
                    <Link href={a.href} className="title">
                      {a.title}
                    </Link>
                    <div className="meta">{a.where}</div>
                  </div>
                  <time>{ago(a.when)}</time>
                </li>
              ))}
            </ul>
          ) : null}
        </section>
      ) : null}

      <div className="split section">
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
