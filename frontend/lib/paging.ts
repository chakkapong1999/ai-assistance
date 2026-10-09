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
export function window(page: number, pages: number): (number | null)[] {
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
