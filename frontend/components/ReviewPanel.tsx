import Link from "next/link";
import type { CommitDetail, Finding, Me, Severity } from "@/lib/api";
import { matches, type FindingFilter } from "@/lib/findings";
import { dateTime, num, usd } from "@/lib/format";
import { closeReview, dismiss, markFixed, sendBack } from "@/app/workflow/actions";
import CopyButton from "./CopyButton";
import DiffView from "./DiffView";
import Score from "./Score";
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

const lines = (f: Finding) => (f.line_end > f.line_start ? `${f.line_start}-${f.line_end}` : String(f.line_start));

function Path({ path }: { path: string }) {
  const i = path.lastIndexOf("/");
  return (
    <>
      {i >= 0 ? path.slice(0, i + 1) : ""}
      <b>{path.slice(i + 1)}</b>
    </>
  );
}

export type { FindingFilter };

const REVIEWERS = ["senior", "lead", "admin"];

function Note({ id, back, anchor, action, label, required, placeholder, primary }: {
  id: number;
  back: string;
  anchor?: string;
  action: (f: FormData) => Promise<void>;
  label: string;
  required?: boolean;
  placeholder: string;
  primary?: boolean;
}) {
  return (
    <details className="noteform">
      <summary>{label}</summary>
      <form action={action}>
        <input type="hidden" name="id" value={id} />
        <input type="hidden" name="back" value={back} />
        {anchor ? <input type="hidden" name="anchor" value={anchor} /> : null}
        <label>
          {required ? "Note (required)" : "Note (optional)"}
          <textarea name="note" rows={3} maxLength={2000} required={required} placeholder={placeholder} />
        </label>
        <button className={primary ? "primary" : undefined}>{label}</button>
      </form>
    </details>
  );
}

const when = (s: string | null) => (s ? dateTime(s) : "");

function FindingState({ f }: { f: Finding }) {
  if (f.status === "open" && f.history.length === 0) return null;
  return (
    <div className="fixstate">
      <p>
        <span className={`fix fix-${f.status}`}>{f.status === "open" ? "Open again" : f.status === "fixed" ? "Marked as fixed" : "Dismissed"}</span>
        {f.status_by ? (
          <span className="muted">
            {" "}
            by {f.status_by.name} · {when(f.status_at)}
          </span>
        ) : null}
      </p>
      {f.status_note ? <p className="note">{f.status_note}</p> : null}
      {f.history.length > 1 ? (
        <details>
          <summary>History ({f.history.length})</summary>
          <ol>
            {f.history.map((h, i) => (
              <li key={i}>
                <b>{h.action === "fixed" ? "Fixed" : h.action === "reopened" ? "Sent back" : "Dismissed"}</b> by {h.by?.name ?? "an admin"} · {when(h.at)}
                {h.note ? <span className="note"> {h.note}</span> : null}
              </li>
            ))}
          </ol>
        </details>
      ) : null}
    </div>
  );
}

export default function ReviewPanel({
  review: r,
  reviewsCount,
  layout,
  hrefFor,
  me,
  authorId,
  back,
  filter,
  path,
}: {
  review: Review;
  reviewsCount: number;
  /** Who is looking; null when the API did not answer. */
  me: Me | null;
  /** The linked author of the commit or pull request, if any. */
  authorId: number | null;
  /** Page to return to after an action. */
  back: string;
  /** Narrows the findings shown; the page reads it from the address. */
  filter: FindingFilter;
  /** This page's path, where the filter form sends its search. */
  path: string;
  layout: "unified" | "split";
  /** Link to this page with the given diff layout. */
  hrefFor: (layout: "unified" | "split") => string;
}) {
  const shown = r.findings.filter((f) => matches(f, filter));
  const groups = byFile(shown);
  const filtered = !!(filter.q || filter.status || filter.severity);
  const withFilter = (href: string) => {
    const [base, query = ""] = href.split("?");
    const p = new URLSearchParams(query);
    if (filter.q) p.set("fq", filter.q);
    if (filter.status) p.set("fs", filter.status);
    if (filter.severity) p.set("fsev", filter.severity);
    const out = p.toString();
    return out ? `${base}?${out}` : base;
  };
  const uid = me?.user?.id ?? null;
  const isAuthor = uid !== null && uid === authorId;
  const isAdmin = me?.role === "admin"; // an admin needs no user: it may fix, send back, dismiss and close
  const reviewer = !!me && REVIEWERS.includes(me.role) && (uid !== null || isAdmin);
  const canReview = reviewer && !isAuthor; // nobody reviews their own work
  const closed = r.closed;
  const open = r.findings.filter((f) => f.status === "open").length;
  const counts = order.map((s) => [s, r.findings.filter((f) => f.severity === s).length] as const).filter(([, n]) => n > 0);

  return (
    <>
      <section className="verdict" aria-label="Verdict">
        <Score value={r.score} large />
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

      <section className="workflow" aria-label="Fix and review status">
        {closed ? (
          <p>
            <span className="fix fix-closed">Review closed</span>{" "}
            <span className="muted">
              by {closed.by?.name ?? "an admin"} · {when(closed.at)}
              {closed.note ? ` · ${closed.note}` : ""}
            </span>
          </p>
        ) : r.findings.length === 0 ? (
          <p className="muted">Nothing to fix.</p>
        ) : (
          <p>
            <b>{open === 0 ? "Every finding is fixed or dismissed." : `${open} of ${r.findings.length} findings still open.`}</b>{" "}
            <span className="muted">
              {open > 0
                ? isAuthor
                  ? "Fix them, then mark each one as fixed."
                  : isAdmin
                    ? "The author marks each one as fixed, or you can."
                  : "The author marks each one as fixed."
                : canReview
                  ? "Look at the fixes, then close the review."
                  : "A senior, lead or admin closes the review."}
            </span>
          </p>
        )}
        {!closed && canReview ? (
          open === 0 ? (
            <Note id={r.id} back={back} action={closeReview} label="Close review" placeholder="Anything the author should know" primary />
          ) : null
        ) : null}
        {reviewer && isAuthor && !closed ? <p className="muted">You wrote this, so someone else has to review it.</p> : null}
        {!me?.user && !isAdmin ? (
          <p className="muted">
            <Link href={`/signin?next=${encodeURIComponent(back)}`}>Sign in</Link> with your personal token to take part.
          </p>
        ) : null}
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
                {filtered ? `${shown.length} of ${r.findings.length}` : r.findings.length} {r.findings.length === 1 ? "finding" : "findings"} in {groups.length} {groups.length === 1 ? "file" : "files"}
              </h2>
              <div className="seg" role="group" aria-label="Code layout">
                {(["unified", "split"] as const).map((l) =>
                  l === layout ? (
                    <span key={l} aria-current="true">
                      {l === "unified" ? "Unified" : "Split"}
                    </span>
                  ) : (
                    <Link key={l} href={withFilter(hrefFor(l))} scroll={false}>
                      {l === "unified" ? "Unified" : "Split"}
                    </Link>
                  ),
                )}
              </div>
            </div>
            <form className="toolbar findfilter" action={path} role="search" aria-label="Filter findings">
              {layout === "split" ? <input type="hidden" name="view" value="split" /> : null}
              <label>
                Search
                <input type="search" name="fq" defaultValue={filter.q} placeholder="Title, file or text" />
              </label>
              <label>
                Severity
                <select name="fsev" defaultValue={filter.severity}>
                  <option value="">Any</option>
                  {order.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Fix
                <select name="fs" defaultValue={filter.status}>
                  <option value="">Any</option>
                  <option value="open">Open</option>
                  <option value="fixed">Fixed</option>
                  <option value="dismissed">Dismissed</option>
                </select>
              </label>
              <button className="primary">Filter</button>
              {filtered ? <Link href={layout === "split" ? `${path}?view=split` : path}>Clear filters</Link> : null}
            </form>
            {shown.length === 0 ? (
              <div className="empty">
                <strong>No findings match these filters</strong>
                Clear a filter to see all {r.findings.length}.
              </div>
            ) : null}
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
                      <h4>{f.title}</h4>
                    </header>
                    {f.code_context ? (
                      <DiffView text={f.code_context} layout={layout} mark={[f.line_start, f.line_end]} label={`Code for: ${f.title}`} />
                    ) : f.suggestion ? (
                      <p className="nocode">The surrounding code was not recorded for this older review.</p>
                    ) : null}
                    {f.explanation ? <p className="explain">{f.explanation}</p> : null}
                    <FindingState f={f} />
                    {!closed || canReview ? (
                      <div className="fixactions">
                        {!closed && f.status === "open" && (isAuthor || isAdmin || (authorId === null && canReview)) ? (
                          <Note id={f.id} back={back} anchor={`f-${f.id}`} action={markFixed} label="Mark as fixed" placeholder="What did you change?" primary />
                        ) : null}
                        {canReview && f.status !== "open" ? (
                          <Note id={f.id} back={back} anchor={`f-${f.id}`} action={sendBack} label="Send back" required placeholder="What is still wrong?" />
                        ) : null}
                        {!closed && canReview && f.status !== "dismissed" ? (
                          <Note id={f.id} back={back} anchor={`f-${f.id}`} action={dismiss} label="Dismiss" required placeholder="Why is this not a problem?" />
                        ) : null}
                      </div>
                    ) : null}
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
                          {f.status !== "open" ? <span className={`fix fix-${f.status}`}>{f.status === "fixed" ? "fixed" : "dismissed"}</span> : null}
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
