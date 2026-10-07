import Link from "next/link";

// Quick filter: one link per value, the current one is marked. Keeps the
// other query parameters and drops the pagination cursor.
export default function Segments({
  base,
  param,
  current,
  options,
  keep,
  label,
}: {
  base: string;
  param: string;
  current: string;
  options: [value: string, label: string][];
  keep: Record<string, string>;
  label: string;
}) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {options.map(([v, l]) => {
        if (v === current) {
          return (
            <span key={v} aria-current="true">
              {l}
            </span>
          );
        }
        const p = new URLSearchParams(Object.entries(keep).filter(([k, x]) => x && k !== param && k !== "cursor"));
        if (v) p.set(param, v);
        const q = p.toString();
        return (
          <Link key={v} href={q ? `${base}?${q}` : base}>
            {l}
          </Link>
        );
      })}
    </div>
  );
}
