import Link from "next/link";

// Switch the "last N days" window while keeping other query params.
export default function Window({ base, days, extra = {} }: { base: string; days: number; extra?: Record<string, string> }) {
  return (
    <div className="seg" role="group" aria-label="Time window">
      {[7, 30, 90].map((d) =>
        d === days ? (
          <span key={d} aria-current="true">
            {d} days
          </span>
        ) : (
          <Link key={d} href={`${base}?${new URLSearchParams({ ...extra, days: String(d) })}`}>
            {d} days
          </Link>
        ),
      )}
    </div>
  );
}
