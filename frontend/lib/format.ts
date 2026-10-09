const tz = process.env.DISPLAY_TZ ?? "Asia/Bangkok";

export function dateTime(iso: string | null | undefined): string {
  if (!iso) return "-";
  return new Intl.DateTimeFormat("en-GB", { dateStyle: "medium", timeStyle: "short", timeZone: tz }).format(new Date(iso));
}

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "-";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

export const num = (n: number) => new Intl.NumberFormat("en-US").format(n);

export function compact(n: number): string {
  return new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 }).format(n);
}

export function usd(n: number | null | undefined): string {
  if (n === null || n === undefined) return "-";
  return `$${n.toFixed(n < 1 ? 4 : 2)}`;
}

export const score = (n: number | null | undefined) => (n === null || n === undefined ? "-" : String(Math.round(n * 10) / 10));

export function intParam(v: string | string[] | undefined, def: number, min: number, max: number): number {
  const n = Number(Array.isArray(v) ? v[0] : v);
  return Number.isInteger(n) && n >= min && n <= max ? n : def;
}

export const one = (v: string | string[] | undefined): string => (Array.isArray(v) ? v[0] : v) ?? "";

// The two cut-offs that colour a score: 90 and up is good, below 70 is bad.
export const tone = (n: number | null | undefined) => (n === null || n === undefined ? "none" : n >= 90 ? "good" : n >= 70 ? "warn" : "bad");

/** "1 commit", "2 commits": the count with its noun in the right number. */
export const plural = (n: number, word: string, many = `${word}s`) => `${num(n)} ${n === 1 ? word : many}`;
