import { Field, Input } from '../../components/ui';
import { businessToday } from '../../lib/format';

/** The first day of the current business month. */
export function monthStart(): string {
  return `${businessToday().slice(0, 8)}01`;
}

/** A span of business days, both included. */
export function PeriodFields({ from, to, onChange }: { from: string; to: string; onChange: (from: string, to: string) => void }) {
  return (
    <>
      <Field label="Từ ngày"><Input type="date" value={from} max={to} onChange={(e) => onChange(e.target.value, to)} /></Field>
      <Field label="Đến ngày"><Input type="date" value={to} min={from} onChange={(e) => onChange(from, e.target.value)} /></Field>
    </>
  );
}
