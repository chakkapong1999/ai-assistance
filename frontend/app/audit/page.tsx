import Link from "next/link";
import { redirect } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { actorName, describe, groups } from "@/lib/audit";
import { dateTime, one } from "@/lib/format";
import Pager, { DEFAULT_SIZE, paging } from "@/components/Pager";
import Segments from "@/components/Segments";

export const metadata = { title: "Audit log" };

type SP = Record<string, string | string[] | undefined>;

export default async function Audit({ searchParams }: { searchParams: Promise<SP> }) {
  const sp = await searchParams;
  const action = groups.some(([v]) => v === one(sp.action)) ? one(sp.action) : "";
  const actorId = /^\d+$/.test(one(sp.actor_id)) ? one(sp.actor_id) : "";
  const { page, size } = paging(one(sp.page), one(sp.size));

  const [me, p0] = await guard(api.me());
  if (!me) return p0;
  if (me.role !== "admin") {
    return (
      <>
        <div className="head">
          <h1>Audit log</h1>
        </div>
        <div className="empty">
          <strong>Admins only</strong>
          The audit log shows who changed what. Sign in with an admin token to read it.
        </div>
      </>
    );
  }

  const [list, problem] = await guard(api.audit({ action, actor_id: actorId, offset: (page - 1) * size, limit: size }));
  if (!list) return problem;

  const keep = { ...(action ? { action } : {}), ...(actorId ? { actor_id: actorId } : {}) };
  if (list.items.length === 0 && page > 1 && list.total > 0) {
    redirect(`/audit?${new URLSearchParams({ ...keep, page: String(Math.ceil(list.total / size)), ...(size !== DEFAULT_SIZE ? { size: String(size) } : {}) })}`);
  }
  const who = actorId ? list.items.find((e) => String(e.actor.id) === actorId) : null;

  return (
    <>
      <div className="head">
        <h1>Audit log</h1>
      </div>
      <p className="intro">Every change made through the dashboard or the API, newest first. Entries cannot be edited or removed.</p>
      <div className="toolbar">
        <Segments base="/audit" param="action" current={action} keep={keep} label="What changed" options={groups} />
        {actorId ? (
          <span>
            Only {who ? actorName(who) : `user ${actorId}`} · <Link href={action ? `/audit?action=${action}` : "/audit"}>Show everyone</Link>
          </span>
        ) : null}
      </div>
      {list.items.length === 0 ? (
        <div className="empty">
          <strong>Nothing logged{action || actorId ? " for this filter" : " yet"}</strong>
          Changes appear here as people fix, dismiss and close findings, or switch review on and off.
        </div>
      ) : (
        <ul className="rows">
          <li className="item hd cols-audit" aria-hidden="true">
            <span>When</span>
            <span>What happened</span>
            <span>Role</span>
          </li>
          {list.items.map((e) => {
            const line = describe(e);
            const note = typeof e.detail.note === "string" ? e.detail.note : null;
            return (
              <li key={e.id} className="item cols-audit">
                <time dateTime={e.at}>{dateTime(e.at)}</time>
                <div>
                  <div>
                    {e.actor.id ? <Link href={`/audit?${new URLSearchParams({ ...keep, actor_id: String(e.actor.id) })}`}>{actorName(e)}</Link> : <strong>{actorName(e)}</strong>}{" "}
                    {line.href ? <Link href={line.href}>{line.text}</Link> : line.text}
                  </div>
                  {note ? <div className="meta">“{note}”</div> : null}
                </div>
                <div className="side">{e.actor.role}</div>
              </li>
            );
          })}
        </ul>
      )}
      <Pager base="/audit" keep={keep} page={page} size={size} total={list.total} noun="entries" />
    </>
  );
}
