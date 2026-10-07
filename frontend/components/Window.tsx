import Link from "next/link";

// Links to switch the "last N days" window while keeping other query params.
export default function Window({ base, days, extra = {} }: { base: string; days: number; extra?: Record<string, string> }) {
  return (
    <div className="row" role="group" aria-label="Time window">
      {[7, 30, 90].map((d) => {
        const p = new URLSearchParams({ ...extra, days: String(d) });
        return d === days ? (
          <span key={d} className="chip" aria-current="true">
            <strong>{d} days</strong>
          </span>
        ) : (
          <Link key={d} href={`${base}?${p}`}>
            {d} days
          </Link>
        );
      })}
    </div>
  );
}
