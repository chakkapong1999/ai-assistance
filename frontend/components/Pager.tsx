import Link from "next/link";
import { num } from "@/lib/format";

export const SIZES = [10, 25, 50, 100] as const;
export const DEFAULT_SIZE = 10;

/** Reads ?page and ?size from the address; anything odd falls back to page 1 and 10 rows. */
export function paging(page: string, size: string): { page: number; size: number } {
  const s = Number(size);
  const p = Number(page);
  return {
    page: Number.isInteger(p) && p >= 1 && p <= 100000 ? p : 1,
    size: (SIZES as readonly number[]).includes(s) ? s : DEFAULT_SIZE,
  };
}

// 1 … 4 5 [6] 7 8 … 12: the ends, and two on each side of the current page.
function window(page: number, pages: number): (number | null)[] {
  const keep = new Set([1, pages, ...[-2, -1, 0, 1, 2].map((d) => page + d)].filter((n) => n >= 1 && n <= pages));
  const out: (number | null)[] = [];
  let last = 0;
  for (const n of [...keep].sort((a, b) => a - b)) {
    if (n - last > 1) out.push(null);
    out.push(n);
    last = n;
  }
  return out;
}

export default function Pager({
  base,
  keep,
  page,
  size,
  total,
  noun,
}: {
  base: string;
  /** The filters in force; carried over to every link. */
  keep: Record<string, string>;
  page: number;
  size: number;
  total: number;
  noun: string;
}) {
  const pages = Math.max(1, Math.ceil(total / size));
  const href = (p: number, s = size) => {
    const q = new URLSearchParams(keep);
    if (p > 1) q.set("page", String(p));
    if (s !== DEFAULT_SIZE) q.set("size", String(s));
    const str = q.toString();
    return str ? `${base}?${str}` : base;
  };
  const from = total === 0 ? 0 : (page - 1) * size + 1;
  const to = Math.min(total, page * size);

  return (
    <nav className="pager" aria-label={`${noun} pages`}>
      <span>
        {num(from)}–{num(to)} of {num(total)} {noun}
      </span>
      {pages > 1 ? (
        <ol className="pages">
          <li>{page > 1 ? <Link href={href(page - 1)} rel="prev">Previous</Link> : <span className="off">Previous</span>}</li>
          {window(page, pages).map((n, i) => (
            <li key={n ?? `gap${i}`}>
              {n === null ? (
                <span className="gap" aria-hidden="true">…</span>
              ) : n === page ? (
                <span aria-current="page">{n}</span>
              ) : (
                <Link href={href(n)} aria-label={`Page ${n}`}>{n}</Link>
              )}
            </li>
          ))}
          <li>{page < pages ? <Link href={href(page + 1)} rel="next">Next</Link> : <span className="off">Next</span>}</li>
        </ol>
      ) : null}
      <span className="sizes" role="group" aria-label="Rows per page">
        Rows
        {SIZES.map((s) =>
          s === size ? (
            <span key={s} aria-current="true">{s}</span>
          ) : (
            <Link key={s} href={href(1, s)}>{s}</Link>
          ),
        )}
      </span>
    </nav>
  );
}
