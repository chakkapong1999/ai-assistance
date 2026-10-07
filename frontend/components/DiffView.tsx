// Renders unified-diff text as a table: old line, new line, sign, code.
// Used for the code a finding is about and for suggested changes. Server
// component, no client JS.

type Kind = "hunk" | "add" | "del" | "ctx";
type Row = { kind: Kind; old: number | null; neu: number | null; text: string };

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

export function parseDiff(text: string): Row[] {
  const rows: Row[] = [];
  let o = 0;
  let n = 0;
  let started = false;
  for (const raw of text.replace(/\r?\n$/, "").split("\n")) {
    const m = HUNK.exec(raw);
    if (m) {
      o = Number(m[1]);
      n = Number(m[2]);
      started = true;
      rows.push({ kind: "hunk", old: null, neu: null, text: raw });
      continue;
    }
    if (!started || raw.startsWith("\\")) continue; // file headers, "\ No newline"
    const sign = raw[0];
    const body = raw.slice(1);
    if (sign === "+") rows.push({ kind: "add", old: null, neu: n++, text: body });
    else if (sign === "-") rows.push({ kind: "del", old: o++, neu: null, text: body });
    else rows.push({ kind: "ctx", old: o++, neu: n++, text: body });
  }
  return rows;
}

type Props = {
  text: string;
  layout?: "unified" | "split";
  /** New-side line range to mark, e.g. the lines a finding is about. */
  mark?: [number, number];
  /** Show the "@@ -a,b +c,d @@" rows. */
  hunks?: boolean;
  label: string;
};

export default function DiffView({ text, layout = "unified", mark, hunks = false, label }: Props) {
  const all = parseDiff(text);
  const rows = hunks ? all : all.filter((r) => r.kind !== "hunk");
  if (rows.length === 0) return null;
  const hit = (r: Row) => !!mark && r.kind !== "del" && r.neu !== null && r.neu >= mark[0] && r.neu <= mark[1];
  const cls = (r: Row) => `${r.kind}${hit(r) ? " hl" : ""}`;

  if (layout === "split") {
    type Pair = { l: Row | null; r: Row | null; hunk?: Row };
    const pairs: Pair[] = [];
    for (let i = 0; i < rows.length; ) {
      const r = rows[i];
      if (r.kind === "hunk") {
        pairs.push({ l: null, r: null, hunk: r });
        i++;
      } else if (r.kind === "ctx") {
        pairs.push({ l: r, r });
        i++;
      } else {
        const dels: Row[] = [];
        const adds: Row[] = [];
        while (i < rows.length && rows[i].kind === "del") dels.push(rows[i++]);
        while (i < rows.length && rows[i].kind === "add") adds.push(rows[i++]);
        for (let k = 0; k < Math.max(dels.length, adds.length); k++) pairs.push({ l: dels[k] ?? null, r: adds[k] ?? null });
      }
    }
    return (
      <div className="code" role="region" aria-label={label} tabIndex={0}>
        <table className="dv split">
          <tbody>
            {pairs.map((p, i) =>
              p.hunk ? (
                <tr key={i} className="hunk">
                  <td colSpan={4}>{p.hunk.text}</td>
                </tr>
              ) : (
                <tr key={i}>
                  <td className={`n ${p.l ? p.l.kind : "void"}`}>{p.l?.old ?? ""}</td>
                  <td className={`c ${p.l ? p.l.kind : "void"}`}>{p.l?.text}</td>
                  <td className={`n ${p.r ? p.r.kind : "void"}`}>{p.r?.neu ?? ""}</td>
                  <td className={`c ${p.r ? p.r.kind : "void"}${p.r && hit(p.r) ? " hl" : ""}`}>{p.r?.text}</td>
                </tr>
              ),
            )}
          </tbody>
        </table>
      </div>
    );
  }

  return (
    <div className="code" role="region" aria-label={label} tabIndex={0}>
      <table className="dv">
        <tbody>
          {rows.map((r, i) =>
            r.kind === "hunk" ? (
              <tr key={i} className="hunk">
                <td colSpan={4}>{r.text}</td>
              </tr>
            ) : (
              <tr key={i} className={cls(r)}>
                <td className="n">{r.old ?? ""}</td>
                <td className="n">{r.neu ?? ""}</td>
                <td className="s">{r.kind === "add" ? "+" : r.kind === "del" ? "−" : ""}</td>
                <td className="c">{r.text}</td>
              </tr>
            ),
          )}
        </tbody>
      </table>
    </div>
  );
}
