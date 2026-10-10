import Link from "next/link";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, plural } from "@/lib/format";
import { findingHref, itemHref, itemTitle, sentBackCount } from "@/lib/work";
import { SeverityMark } from "@/components/badges";

export const metadata = { title: "My work" };

export default async function MyWork() {
  const [[me], [work, problem]] = await Promise.all([guard(api.me()), guard(api.myWork())]);
  if (!work) return problem;
  if (!me?.user) {
    return (
      <>
        <div className="head">
          <h1>My work</h1>
        </div>
        <div className="empty">
          <strong>Sign in to see your work</strong>
          <Link href="/signin">Sign in</Link> with your personal token. A shared token does not belong to anyone.
        </div>
      </>
    );
  }
  const { items, total_findings } = work.to_fix;
  const back = sentBackCount(items);
  const reviewer = me.role !== "author" && me.role !== "viewer";

  return (
    <>
      <div className="head">
        <h1>My work</h1>
      </div>
      <p className="intro">
        {total_findings === 0
          ? "Nothing to fix. Findings on your commits and pull requests show up here until you mark them as fixed."
          : `${plural(total_findings, "finding")} to fix in ${plural(items.length, "change")}${back ? `, ${back} sent back by a reviewer` : ""}. Worst first.`}
      </p>

      {items.map((i) => (
        <section key={`${i.kind}-${i.id}`} aria-label={itemTitle(i)}>
          <div className="work-head">
            <h2>
              <Link href={itemHref(i)}>{itemTitle(i)}</Link>
            </h2>
            <span className="meta">
              {i.repository} · reviewed {ago(i.reviewed_at)} · {i.open_findings.length} of {i.findings} open
            </span>
          </div>
          <ul className="rows">
            {i.open_findings.map((f) => (
              <li key={f.id} className="item cols-work">
                <SeverityMark severity={f.severity} />
                <div>
                  <Link href={findingHref(i, f)} className="title">
                    {f.title}
                  </Link>
                  <div className="meta">
                    {f.file_path}:{f.line_start} · {f.category}
                  </div>
                  {f.sent_back ? (
                    <div className="sent-back">
                      <span className="mark warn">Sent back</span> {f.sent_back.by ? `by ${f.sent_back.by.name} ` : ""}
                      {ago(f.sent_back.at)}
                      {f.sent_back.note ? `: ${f.sent_back.note}` : ""}
                    </div>
                  ) : null}
                </div>
                <Link href={findingHref(i, f)} className="r">
                  Open
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ))}

      {reviewer ? (
        <>
          <div className="head" style={{ marginTop: 40 }}>
            <h2>Waiting for your review</h2>
          </div>
          {work.to_review.length === 0 ? (
            <div className="empty">
              <strong>Nothing waiting</strong>
              Work shows up here when every finding is fixed or dismissed and no one has closed the review yet.
            </div>
          ) : (
            <ul className="rows">
              {work.to_review.map((i) => (
                <li key={`${i.kind}-${i.id}`} className="item cols-attn">
                  <time>{ago(i.reviewed_at)}</time>
                  <div>
                    <Link href={itemHref(i)} className="title">
                      {itemTitle(i)}
                    </Link>
                    <div className="meta">
                      {i.author ?? "Unknown author"} · {i.repository} · {plural(i.findings, "finding")}, none open
                    </div>
                  </div>
                  <Link href={itemHref(i)} className="r">
                    Review
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </>
      ) : null}
    </>
  );
}
