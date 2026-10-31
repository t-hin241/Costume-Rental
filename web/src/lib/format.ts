import type { Money } from './api/types';

// All business dates and times are shown in the shop's timezone, whatever the
// device is set to. The API sends UTC instants and YYYY-MM-DD business dates.
const zone = 'Asia/Ho_Chi_Minh';

const vnd = new Intl.NumberFormat('vi-VN', { maximumFractionDigits: 0 });
const oneDecimal = new Intl.NumberFormat('vi-VN', { maximumFractionDigits: 1 });
const dateTime = new Intl.DateTimeFormat('vi-VN', {
  timeZone: zone, day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false,
});
const dateOnly = new Intl.DateTimeFormat('vi-VN', {
  timeZone: zone, day: '2-digit', month: '2-digit', year: 'numeric',
});
const timeOnly = new Intl.DateTimeFormat('vi-VN', {
  timeZone: zone, hour: '2-digit', minute: '2-digit', hour12: false,
});
const dayLabel = new Intl.DateTimeFormat('vi-VN', {
  timeZone: 'UTC', weekday: 'long', day: '2-digit', month: '2-digit',
});
const zonedParts = new Intl.DateTimeFormat('en-CA', {
  timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit',
});

export function formatMoney(m: Money): string {
  return `${vnd.format(m.amount)}đ`;
}

export function formatNumber(n: number): string {
  return vnd.format(n);
}

/** Compact axis label: 1,2 tr / 350 N. */
export function formatCompactVND(amount: number): string {
  if (amount >= 1_000_000) return `${oneDecimal.format(amount / 1_000_000)} tr`;
  if (amount >= 1_000) return `${vnd.format(Math.round(amount / 1_000))} N`;
  return vnd.format(amount);
}

export function formatDateTime(iso: string): string {
  return dateTime.format(new Date(iso));
}

export function formatDate(iso: string): string {
  return dateOnly.format(new Date(iso));
}

export function formatTime(iso: string): string {
  return timeOnly.format(new Date(iso));
}

/** Formats a YYYY-MM-DD business date, which has no timezone of its own. */
export function formatBusinessDate(date: string): string {
  return dayLabel.format(new Date(`${date}T00:00:00Z`));
}

/** Today's business date (YYYY-MM-DD) in the shop's timezone. */
export function businessToday(now: Date = new Date()): string {
  return zonedParts.format(now);
}

export function addBusinessDays(date: string, days: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

/** Human duration from whole hours, e.g. 168 → "7 ngày", 2 → "2 giờ". */
export function formatHours(hours: number): string {
  if (hours === 0) return '0 giờ';
  if (hours % 24 === 0) return `${hours / 24} ngày`;
  return `${hours} giờ`;
}

/** Remaining time for a countdown, e.g. "12:05" or "1:02:05". */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}
