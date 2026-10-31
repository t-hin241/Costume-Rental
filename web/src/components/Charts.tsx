import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';

function useWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const observer = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.round(entry.contentRect.width));
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return { ref, width };
}

/** Picks an axis with round ticks: steps of 1, 2, 2.5 or 5 times a power of ten. */
export function niceScale(max: number): { top: number; step: number } {
  if (max <= 0) return { top: 1, step: 1 };
  const rough = max / 4;
  const power = 10 ** Math.floor(Math.log10(rough));
  const step = ([1, 2, 2.5, 5, 10].find((m) => rough <= m * power) ?? 10) * power;
  return { top: Math.ceil(max / step) * step, step };
}

export interface ColumnDatum {
  key: string;
  label: string;
  value: number;
  tooltip: ReactNode;
}

const height = 240;
const pad = { top: 12, right: 8, bottom: 24, left: 46 };
const maxBar = 24;

/** Single-series column chart; the card title says what is plotted. */
export function ColumnChart({ data, formatTick, ariaLabel }: {
  data: ColumnDatum[]; formatTick: (v: number) => string; ariaLabel: string;
}) {
  const { ref, width } = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const { top, step } = niceScale(Math.max(...data.map((d) => d.value), 0));
  const plotW = Math.max(width - pad.left - pad.right, 10);
  const plotH = height - pad.top - pad.bottom;
  const band = plotW / Math.max(data.length, 1);
  const bar = Math.min(maxBar, Math.max(band - 2, 3));
  const y = (v: number) => pad.top + plotH - (v / top) * plotH;
  const labelEvery = Math.max(1, Math.ceil(data.length / Math.floor(plotW / 56)));
  const active = hover !== null ? data[hover] : undefined;

  return (
    <div ref={ref} className="chart-frame">
      <svg className="chart" width={width} height={height} role="img" aria-label={ariaLabel} onMouseLeave={() => setHover(null)}>
        {Array.from({ length: Math.round(top / step) + 1 }, (_, i) => {
          const v = step * i;
          return (
            <g key={i}>
              <line x1={pad.left} x2={width - pad.right} y1={y(v)} y2={y(v)} stroke="var(--line)" />
              <text x={pad.left - 6} y={y(v) + 4} textAnchor="end">{formatTick(v)}</text>
            </g>
          );
        })}
        {data.map((d, i) => {
          const x = pad.left + band * i + (band - bar) / 2;
          const h = Math.max(y(0) - y(d.value), d.value > 0 ? 2 : 0);
          const r = Math.min(4, bar / 2, h);
          return (
            <g key={d.key}>
              <path fill={hover === i ? 'var(--yellow)' : 'var(--black)'}
                d={`M${x} ${y(0)}V${y(0) - h + r}Q${x} ${y(0) - h} ${x + r} ${y(0) - h}H${x + bar - r}Q${x + bar} ${y(0) - h} ${x + bar} ${y(0) - h + r}V${y(0)}Z`} />
              {i % labelEvery === 0 && <text x={x + bar / 2} y={height - 6} textAnchor="middle">{d.label}</text>}
              <rect x={pad.left + band * i} y={pad.top} width={band} height={plotH + pad.bottom} fill="transparent" onMouseEnter={() => setHover(i)} />
            </g>
          );
        })}
      </svg>
      {active && hover !== null && (
        <div className="chart-tip" style={{ left: Math.min(Math.max(pad.left + band * (hover + 0.5), 70), width - 70), top: y(active.value) - 6 }}>{active.tooltip}</div>
      )}
    </div>
  );
}

/** Ranked bars with the status word beside each, so nothing relies on colour. */
export function BarList({ rows }: { rows: { key: string; label: ReactNode; value: number }[] }) {
  const max = Math.max(...rows.map((r) => r.value), 1);
  return (
    <div>
      {rows.map((r) => (
        <div key={r.key} className="hbar-row">
          <span>{r.label}</span>
          <div className="hbar-track"><div className="hbar-fill" style={{ width: `${(r.value / max) * 100}%` }} /></div>
          <span className="tnum strong right">{r.value}</span>
        </div>
      ))}
    </div>
  );
}
