import Link from "next/link";
import type { CommitDetail, Finding, Severity } from "@/lib/api";
import { dateTime, num, usd } from "@/lib/format";
import CopyButton from "./CopyButton";
import DiffView from "./DiffView";
import ScoreMeter from "./ScoreMeter";
import { SeverityMark } from "./badges";

type Review = NonNullable<CommitDetail["review"]>;

const order: Severity[] = ["critical", "major", "minor", "info"];
const rank = (s: Severity) => order.indexOf(s);

type Group = { path: string; findings: Finding[] };

// Files with the worst finding first; inside a file, top to bottom.
function byFile(findings: Finding[]): Group[] {
  const m = new Map<string, Finding[]>();
  for (const f of findings) m.set(f.file_path, [...(m.get(f.file_path) ?? []), f]);
  return [...m.entries()]
    .map(([path, fs]) => ({ path, findings: fs.sort((a, b) => a.line_start - b.line_start || rank(a.severity) - rank(b.severity)) }))
    .sort((a, b) => Math.min(...a.findings.map((f) => rank(f.severity))) - Math.min(...b.findings.map((f) => rank(f.severity))) || a.path.localeCompare(b.path));
}

const lines = (f: Finding) => (f.line_end > f.line_start ? `${f.line_start}–${f.line_end}` : String(f.line_start));

function Path({ path }: { path: string }) {
  const i = path.lastIndexOf("/");
  return (
    <>
      {i >= 0 ? path.slice(0, i + 1) : ""}
      <b>{path.slice(i + 1)}</b>
    </>
  );
}

export default function ReviewPanel({
  review: r,
  reviewsCount,
  layout,
  hrefFor,
}: {
  review: Review;
  reviewsCount: number;
  layout: "unified" | "split";
  /** Link to this page with the given diff layout. */
  hrefFor: (layout: "unified" | "split") => string;
}) {
  const groups = byFile(r.findings);
  const counts = order.map((s) => [s, r.findings.filter((f) => f.severity === s).length] as const).filter(([, n]) => n > 0);

  return (
    <>
      <section className="verdict" aria-label="Verdict">
        <ScoreMeter value={r.score} large />
        <div>
          <p className="summary">{r.summary || "The reviewer left no summary."}</p>
          <p className="facts">
            {r.model === "mock" ? <span className="tag warn">Mock review, not a real model</span> : <span>Model {r.model}</span>}
            <span>Prompt {r.prompt_version}</span>
            <span>{dateTime(r.created_at)}</span>
            {r.duration_ms !== null ? <span>{(r.duration_ms / 1000).toFixed(1)} s</span> : null}
            {r.tokens_in !== null ? (
              <span>
                {num(r.tokens_in)} in / {num(r.tokens_out ?? 0)} out tokens
              </span>
            ) : null}
            {r.cost_usd !== null ? <span>{usd(r.cost_usd)}</span> : null}
            {reviewsCount > 1 ? <span>Latest of {reviewsCount} reviews</span> : null}
          </p>
        </div>
      </section>

      {r.findings.length === 0 ? (
        <div className="empty">
          <strong>No findings</strong>
          The reviewer found nothing to flag in this diff.
        </div>
      ) : (
        <div className="review">
          <div>
            <div className="viewtoggle">
              <h2>
                {r.findings.length} {r.findings.length === 1 ? "finding" : "findings"} in {groups.length} {groups.length === 1 ? "file" : "files"}
              </h2>
              <div className="seg" role="group" aria-label="Code layout">
                {(["unified", "split"] as const).map((l) =>
                  l === layout ? (
                    <span key={l} aria-current="true">
                      {l === "unified" ? "Unified" : "Split"}
                    </span>
                  ) : (
                    <Link key={l} href={hrefFor(l)} scroll={false}>
                      {l === "unified" ? "Unified" : "Split"}
                    </Link>
                  ),
                )}
              </div>
            </div>
            {groups.map((g) => (
              <section key={g.path} className="file">
                <h3>
                  <Path path={g.path} />
                </h3>
                {g.findings.map((f) => (
                  <article key={f.id} id={`f-${f.id}`} className={`thread tone-${f.severity}`}>
                    <header>
                      <div className="where">
                        <SeverityMark severity={f.severity} />
                        <span>{f.category}</span>
                        <span className="mono">
                          line {lines(f)}
                        </span>
                      </div>
                      <h4 style={{ margin: 0, fontSize: "1.02rem", fontWeight: 620 }}>{f.title}</h4>
                    </header>
                    {f.code_context ? (
                      <DiffView text={f.code_context} layout={layout} mark={[f.line_start, f.line_end]} label={`Code for: ${f.title}`} />
                    ) : f.suggestion ? (
                      <p className="nocode">The surrounding code was not recorded for this older review.</p>
                    ) : null}
                    {f.explanation ? <p className="explain">{f.explanation}</p> : null}
                    {f.suggestion ? (
                      <div className="suggest">
                        <header>
                          <span>Suggested change</span>
                          <CopyButton text={f.suggestion.unified_diff} />
                        </header>
                        <DiffView text={f.suggestion.unified_diff} layout={layout} label={`Suggested change for: ${f.title}`} />
                      </div>
                    ) : null}
                  </article>
                ))}
              </section>
            ))}
          </div>

          <nav className="outline" aria-label="Findings in this review">
            <h2>Outline</h2>
            <div className="counts">
              {counts.map(([s, n]) => (
                <span key={s} className={`sev sev-${s}`}>
                  {n} {s}
                </span>
              ))}
            </div>
            <ul>
              {groups.map((g) => (
                <li key={g.path}>
                  <div>
                    <Path path={g.path} />
                  </div>
                  <ul>
                    {g.findings.map((f) => (
                      <li key={f.id}>
                        <a href={`#f-${f.id}`}>
                          <span className={`sev sev-${f.severity}`}>
                            <span className="sr">{f.severity}: </span>
                          </span>
                          <span className="l">{f.line_start}</span>
                          <span>{f.title}</span>
                        </a>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          </nav>
        </div>
      )}
    </>
  );
}
