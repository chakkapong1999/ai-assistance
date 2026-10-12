import type { ReactNode } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import type { Severity } from "@/lib/api";
import { guard } from "@/lib/guard";
import { ago, plural } from "@/lib/format";
import { itemHref, itemTitle, sentBackCount } from "@/lib/work";
import { InboxShell, Row } from "@/components/Inbox";

const order: Severity[] = ["critical", "major", "minor", "info"];

type Entry = { key: string; href: string; mark: ReactNode; title: string; meta: string; when: string };

function Group({ title, entries, empty }: { title: string; entries: Entry[]; empty?: string }) {
  if (entries.length === 0 && !empty) return null;
  return (
    <section>
      <h2>{title}</h2>
      {entries.length === 0 ? (
        <p className="none">{empty}</p>
      ) : (
        <ul>
          {entries.map((e) => (
            <li key={e.key}>
              <Row href={e.href}>
                <span className="m">{e.mark}</span>
                <span>
                  <span className="t">{e.title}</span>
                  <span className="sub">{e.meta}</span>
                </span>
                <time>{e.when}</time>
              </Row>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// The list stays put while the detail pane on the right changes with the address.
export default async function InboxLayout({ children }: { children: ReactNode }) {
  const [[me], [work, problem]] = await Promise.all([guard(api.me()), guard(api.myWork())]);
  if (!work) return problem;
  if (!me?.user) {
    return (
      <>
        <div className="head">
          <h1>Inbox</h1>
        </div>
        <div className="empty">
          <strong>Sign in to see your inbox</strong>
          <Link href="/signin?next=/inbox">Sign in</Link> with your personal token. A shared token does not belong to anyone.
        </div>
      </>
    );
  }
  const admin = me.role === "admin";
  const reviewer = me.role !== "author" && me.role !== "viewer";
  // Only an admin can review again, so only an admin is shown what failed.
  const [[fc], [fp]] = admin
    ? await Promise.all([guard(api.commits({ status: "failed", limit: 20 })), guard(api.pullRequests({ review_status: "failed", limit: 20 }))])
    : [[null], [null]];

  const fix: Entry[] = work.to_fix.items.map((i) => {
    const worst = order.find((s) => i.open_findings.some((f) => f.severity === s)) ?? "info";
    const back = sentBackCount([i]);
    return {
      key: `${i.kind}-${i.id}`,
      href: `/inbox${itemHref(i)}`,
      mark: <span className={`sev sev-${worst}`}>{worst[0].toUpperCase() + worst.slice(1)}</span>,
      title: itemTitle(i),
      meta: `${i.repository} · ${i.open_findings.length} open${back ? `, ${back} sent back` : ""}`,
      when: ago(i.reviewed_at),
    };
  });
  const close: Entry[] = work.to_review.map((i) => ({
    key: `${i.kind}-${i.id}`,
    href: `/inbox${itemHref(i)}`,
    mark: <span className="mark info">Ready</span>,
    title: itemTitle(i),
    meta: `${i.repository} · ${i.author ?? "Unknown author"} · ${plural(i.findings, "finding")}, none open`,
    when: ago(i.reviewed_at),
  }));
  const failed: Entry[] = [
    ...(fc?.items ?? []).map((c) => ({ key: `c${c.id}`, href: `/inbox/commits/${c.id}`, title: c.subject || "(no message)", meta: c.repository.full_name, when: ago(c.committed_at) })),
    ...(fp?.items ?? []).map((p) => ({ key: `p${p.id}`, href: `/inbox/pull-requests/${p.id}`, title: `#${p.number} ${p.title || "(no title)"}`, meta: p.repository.full_name, when: ago(p.updated_at) })),
  ].map((e) => ({ ...e, mark: <span className="mark bad">Failed</span> }));

  return (
    <InboxShell
      list={
        <>
          <Group title="Yours to fix" entries={fix} empty="Nothing to fix. Findings on your work show up here until you mark them as fixed." />
          {reviewer ? <Group title="Ready to close" entries={close} empty="Nothing waiting for your review." /> : null}
          <Group title="Review failed" entries={failed} />
          <p className="keys">
            <kbd>j</kbd> <kbd>k</kbd> next and previous · <kbd>/</kbd> search
          </p>
        </>
      }
    >
      {children}
    </InboxShell>
  );
}
