import { score as fmt } from "@/lib/format";

// A 0–100 bar with ticks at 70 and 90, the two cut-offs that colour a score.
export default function ScoreMeter({ value, large = false }: { value: number | null | undefined; large?: boolean }) {
  if (value === null || value === undefined) {
    return (
      <div className={`meter none${large ? " lg" : ""}`}>
        <b aria-label="No score">–</b>
      </div>
    );
  }
  const tone = value >= 90 ? "" : value >= 70 ? " warn" : " bad";
  return (
    <div className={`meter${tone}${large ? " lg" : ""}`} role="img" aria-label={`Score ${fmt(value)} out of 100`}>
      <b>{fmt(value)}</b>
      <div className="track" style={{ ["--v" as string]: Math.max(0, Math.min(100, value)) }}>
        <div className="fill" />
        <span className="tick" style={{ left: "70%" }} />
        <span className="tick" style={{ left: "90%" }} />
      </div>
      {large ? (
        <div className="scale" aria-hidden="true">
          <span>0</span>
          <span>70</span>
          <span>90</span>
          <span>100</span>
        </div>
      ) : null}
    </div>
  );
}
