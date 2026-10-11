import Link from "next/link";
import { redirect } from "next/navigation";
import { api } from "@/lib/api";
import { guard } from "@/lib/guard";
import { intParam, num, one } from "@/lib/format";
import Score from "@/components/Score";
import { Parts } from "@/components/badges";
import Segments from "@/components/Segments";
import Window from "@/components/Window";
import Pager, { DEFAULT_SIZE, paging } from "@/components/Pager";

export const metadata = { title: "People" };

const sorts: [string, string][] = [
  ["commits", "Most commits"],
  ["score", "Highest score"],
  ["name", "Name"],
];

export default async function Users({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const sp = await searchParams;
  const days = intParam(sp.days, 30, 1, 365);
  const q = one(sp.q);
  const sort = sorts.some(([k]) => k === one(sp.sort)) ? one(sp.sort) : "commits";
  const { page, size } = paging(one(sp.page), one(sp.size));
  const [list, problem] = await guard(api.users({ days, q, sort, offset: (page - 1) * size, limit: size }));
  if (!list) return problem;
  const keep = { days: String(days), ...(q ? { q } : {}) };
  if (list.items.length === 0 && page > 1 && list.total > 0) redirect(`/users?${new URLSearchParams({ ...keep, sort, page: String(Math.ceil(list.total / size)), ...(size !== DEFAULT_SIZE ? { size: String(size) } : {}) })}`);

  return (
    <>
      <div className="head">
        <h1>People</h1>
        <Window base="/users" days={days} extra={{ sort, ...(q ? { q } : {}) }} />
      </div>
      <p className="intro">Commit authors over the last {days} days. The score is the mean of each commit&apos;s latest review.</p>
      <form className="toolbar" action="/users">
        <input type="hidden" name="days" value={days} />
        <input type="hidden" name="sort" value={sort} />
        {size !== DEFAULT_SIZE ? <input type="hidden" name="size" value={size} /> : null}
        <label>
          Search
          <input type="search" name="q" defaultValue={q} placeholder="Name" />
        </label>
        <button className="primary">Search</button>
        <Segments base="/users" param="sort" current={sort} keep={keep} label="Sort by" options={sorts} />
      </form>
      {list.items.length === 0 ? (
        <div className="empty">
          <strong>No people found</strong>
          Authors are created from pushes and from the poller.
        </div>
      ) : (
        <ul className="rows">
          <li className="item hd cols-people" aria-hidden="true">
            <span>Score</span>
            <span>Person</span>
            <span className="r">Commits</span>
            <span className="r">Reviewed</span>
            <span className="r">Findings</span>
          </li>
          {list.items.map((u) => (
            <li key={u.id} className="item cols-people">
              <Score value={u.avg_score} />
              <div>
                <Link href={`/users/${u.id}?days=${days}`} className="title">
                  {u.display_name}
                </Link>
                <div className="meta">
                  <Parts items={[u.job_title, u.email, u.linked ? null : "Not linked to Bitbucket"]} empty="Bitbucket account" />
                </div>
              </div>
              <div className="side r">{num(u.commits)} commits</div>
              <div className="side r">{num(u.reviewed)} reviewed</div>
              <div className="side r">{num(u.findings)} findings</div>
            </li>
          ))}
        </ul>
      )}
      <Pager base="/users" keep={{ ...keep, sort }} page={page} size={size} total={list.total} noun="people" />
    </>
  );
}
