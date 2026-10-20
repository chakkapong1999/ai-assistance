import Link from "next/link";
import type { ReactNode } from "react";
import { api } from "@/lib/api";
import type { Severity } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, intParam, num, usd, plural } from "@/lib/format";
import Score from "@/components/Score";
import Window from "@/components/Window";
import AutoRefresh from "@/components/AutoRefresh";

export const metadata = { title: "Overview" };

const sevs: Severity[] = ["critical", "major", "minor", "info"];

type Entry = { key: string; href: string; title: string; where: string; when: string };

// One queue: who it waits for, how many, and the latest few.
function Queue({ id, title, count, caption, entries, all, tone }: { id: string; title: string; count: number; caption: ReactNode; entries: Entry[]; all?: ReactNode; tone?: string }) {
  return (
    <section className="queue" aria-labelledby={id}>
      <header>
        <h2 id={id}>{title}</h2>
        <span className={`n${tone ? ` ${tone}` : ""}`}>{num(count)}</span>
      </header>
      <p className="cap">{caption}</p>
      {entries.length === 0 ? (
        <p className="none">Nothing here.</p>
      ) : (
        <ul>
          {entries.map((e) => (
            <li key={e.key}>
              <Link href={e.href}>
                <span className="t">{e.title}</span>
                <span className="sub">
                  {e.where} · {e.when}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {all ? <p className="all">{all}</p> : null}
    </section>
  );
}

// What needs someone right now. Trends and cost are on Insights.
export default async function Overview({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const days = intParam((await searchParams).days, 30, 1, 365);
  // Same window as /overview: whole UTC days, today included.
  const now = new Date();
  const since = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - (days - 1))).toISOString();
  const [[o, problem], [fc], [fp], [oc], [op], [rc], [rp], [health]] = await Promise.all([
    guard(api.overview(days)),
    guard(api.commits({ status: "failed", since, limit: 3 })),
    guard(api.pullRequests({ review_status: "failed", limit: 3 })),
    guard(api.commits({ fix: "open", since, limit: 3 })),
    guard(api.pullRequests({ fix: "open", limit: 3 })),
    guard(api.commits({ fix: "ready", since, limit: 3 })),
    guard(api.pullRequests({ fix: "ready", limit: 3 })),
    guard(api.health()),
  ]);
  if (!o) return problem;

  const sev = o.findings_by_severity;
  const totalFindings = sevs.reduce((n, s) => n + sev[s], 0);
  const commit = (c: NonNullable<typeof fc>["items"][number]): Entry => ({ key: `c${c.id}`, href: `/commits/${c.id}`, title: c.subject || "(no message)", where: c.repository.full_name, when: ago(c.committed_at) });
  const pr = (p: NonNullable<typeof fp>["items"][number]): Entry => ({ key: `p${p.id}`, href: `/pull-requests/${p.id}`, title: p.title || "(no title)", where: `${p.repository.full_name} #${p.number}`, when: ago(p.updated_at) });
  const both = (a: typeof fc, b: typeof fp) => [...(a?.items ?? []).map(commit), ...(b?.items ?? []).map(pr)];
  const failedCount = o.commits_by_status.failed + (fp?.total ?? 0);
  const busy = o.queue_waiting + o.queue_running > 0;

  return (
    <>
      <div className="head">
        <div>
          <h1>Overview</h1>
          <p className="pulse">
            <span className={`mark ${health?.status === "degraded" ? "bad" : "good"}`}>{health ? (health.status === "ok" ? "Pipeline healthy" : "Pipeline needs a look") : "Pipeline unknown"}</span>
            <span>
              {o.queue_running} reviewing, {o.queue_waiting} waiting
            </span>
            {busy ? <AutoRefresh seconds={15} /> : null}
          </p>
        </div>
        <Window base="/" days={days} />
      </div>

      {health && health.status === "degraded" ? (
        <section className="banner bad" role="alert" aria-labelledby="pipe">
          <strong id="pipe">The review pipeline needs a look</strong>
          <ul className="problems">
            {health.problems.map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        </section>
      ) : null}

      <div className="queues">
        <Queue
          id="q-fix"
          title="Waiting for the author"
          count={o.fix.open_findings}
          caption={`${o.fix.open_findings === 1 ? "Finding" : "Findings"} still open, from the latest review of each commit.`}
          entries={both(oc, op)}
          all={
            <>
              <Link href="/commits?fix=open">Commits</Link> · <Link href="/pull-requests?fix=open">Pull requests</Link>
            </>
          }
        />
        <Queue
          id="q-close"
          title="Waiting for a reviewer"
          count={o.fix.reviews_ready_to_close}
          caption={`Every finding is fixed or dismissed (${plural(o.fix.fixed_findings, "fixed finding")}). Someone has to close ${o.fix.reviews_ready_to_close === 1 ? "it" : "them"}.`}
          entries={both(rc, rp)}
          all={
            <>
              <Link href="/commits?fix=ready">Commits</Link> · <Link href="/pull-requests?fix=ready">Pull requests</Link>
            </>
          }
        />
        <Queue
          id="q-fail"
          title="Review failed"
          count={failedCount}
          tone={failedCount > 0 ? "bad" : undefined}
          caption="An admin can review these again."
          entries={both(fc, fp)}
          all={
            <>
              <Link href="/commits?status=failed">Commits</Link> · <Link href="/pull-requests?review_status=failed">Pull requests</Link>
            </>
          }
        />
      </div>

      <section className="section" aria-labelledby="window">
        <header className="row-head">
          <h2 id="window">Last {days} days</h2>
          <Link href={`/insights${days !== 30 ? `?days=${days}` : ""}`}>Charts and cost</Link>
        </header>
        <dl className="strip">
          <div>
            <dt>Commits reviewed</dt>
            <dd>
              <Link href="/commits?status=done">{num(o.reviewed)}</Link> <span className="muted">of {num(o.commits)}</span>
            </dd>
          </div>
          <div>
            <dt>Average score</dt>
            <dd>{o.avg_score !== null ? <Score value={o.avg_score} /> : <span className="muted">none yet</span>}</dd>
          </div>
          <div>
            <dt>Findings</dt>
            <dd>
              {num(totalFindings)}
              {sev.critical > 0 ? <span className="crit"> · {num(sev.critical)} critical</span> : null}
            </dd>
          </div>
          <div>
            <dt>Reviews closed</dt>
            <dd>{num(o.fix.reviews_closed)}</dd>
          </div>
          <div>
            <dt>Review cost</dt>
            <dd>{usd(o.usage.cost_usd)}</dd>
          </div>
        </dl>
      </section>
    </>
  );
}
