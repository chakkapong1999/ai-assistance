import { score as fmt, tone } from "@/lib/format";

// The score gutter: a tinted cell at the left edge of a row, like a diff's line-number gutter.
// `large` is the verdict block on a detail page. Unscored cells are hatched and left empty.
export default function Score({ value, large = false }: { value: number | null | undefined; large?: boolean }) {
  const t = tone(value);
  return (
    <span className={`score ${t}${large ? " lg" : ""}`} role="img" aria-label={t === "none" ? "Not scored" : `Score ${fmt(value)} out of 100`}>
      {t === "none" ? null : <b>{fmt(value)}</b>}
      {large ? <span>{t === "none" ? "Not scored yet" : "out of 100"}</span> : null}
    </span>
  );
}
