// Server-rendered SVG bars. `value2` (optional) is drawn in front as a lighter
// share of `value`, e.g. reviewed out of all commits.
export type Bar = { label: string; value: number; value2?: number; tip: string };

export default function BarChart({ bars, title, height = 120 }: { bars: Bar[]; title: string; height?: number }) {
  const W = 600;
  const padL = 30;
  const padB = 18;
  const max = Math.max(1, ...bars.map((b) => b.value));
  const top = niceCeil(max);
  const innerW = W - padL;
  const slot = innerW / Math.max(1, bars.length);
  const bw = Math.max(2, slot * 0.7);
  const y = (v: number) => height - padB - (v / top) * (height - padB - 4);
  const every = Math.ceil(bars.length / 8);
  return (
    <svg className="chart" viewBox={`0 0 ${W} ${height}`} role="img" aria-label={title}>
      {[0, top / 2, top].map((t) => (
        <g key={t}>
          <line className="grid" x1={padL} x2={W} y1={y(t)} y2={y(t)} />
          <text x={padL - 4} y={y(t) + 3} textAnchor="end">
            {Number.isInteger(t) ? t : t.toFixed(2)}
          </text>
        </g>
      ))}
      {bars.map((b, i) => {
        const x = padL + i * slot + (slot - bw) / 2;
        return (
          <g key={b.label}>
            <title>{b.tip}</title>
            <rect x={x} y={y(b.value)} width={bw} height={Math.max(0, height - padB - y(b.value))} style={{ fill: "var(--line)" }} />
            {b.value2 ? <rect x={x} y={y(b.value2)} width={bw} height={Math.max(0, height - padB - y(b.value2))} fill="var(--accent)" /> : null}
            {i % every === 0 ? (
              <text x={x + bw / 2} y={height - 4} textAnchor="middle">
                {b.label}
              </text>
            ) : null}
          </g>
        );
      })}
    </svg>
  );
}

// Tiny trend line for KPI tiles; decorative, the tile states the number.
export function Sparkline({ values, color = "var(--accent)" }: { values: number[]; color?: string }) {
  if (values.length < 2) return null;
  const max = Math.max(1, ...values);
  const pts = values.map((v, i) => `${(i / (values.length - 1)) * 120},${26 - (v / max) * 24}`).join(" ");
  return (
    <svg className="spark" viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true">
      <polyline fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" points={pts} />
    </svg>
  );
}

function niceCeil(n: number): number {
  if (n <= 1) return n > 0 && n < 1 ? Math.ceil(n * 100) / 100 : 1;
  const p = Math.pow(10, Math.floor(Math.log10(n)));
  for (const m of [1, 2, 5, 10]) if (n <= m * p) return m * p;
  return 10 * p;
}
