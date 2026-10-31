import { describe, expect, it } from 'vitest';
import { addBusinessDays, businessToday, formatCompactVND, formatCountdown, formatDateTime, formatHours, formatMoney } from './format';

describe('money', () => {
  it('shows whole VND with Vietnamese grouping', () => {
    expect(formatMoney({ amount: 1250000, currency: 'VND' })).toBe('1.250.000đ');
    expect(formatMoney({ amount: 0, currency: 'VND' })).toBe('0đ');
  });

  it('compacts axis labels', () => {
    expect(formatCompactVND(1_500_000)).toBe('1,5 tr');
    expect(formatCompactVND(350_000)).toBe('350 N');
  });
});

describe('business time', () => {
  it('shows instants in the shop timezone, whatever the device is set to', () => {
    // 17:30 UTC is 00:30 the next day in Asia/Ho_Chi_Minh.
    expect(formatDateTime('2026-11-09T17:30:00Z')).toContain('10/11/2026');
    expect(formatDateTime('2026-11-09T17:30:00Z')).toContain('00:30');
  });

  it('takes the business date from the shop timezone', () => {
    expect(businessToday(new Date('2026-11-09T17:30:00Z'))).toBe('2026-11-10');
    expect(businessToday(new Date('2026-11-09T16:59:00Z'))).toBe('2026-11-09');
  });

  it('moves business dates across month ends without a timezone', () => {
    expect(addBusinessDays('2026-10-31', 1)).toBe('2026-11-01');
    expect(addBusinessDays('2026-03-01', -1)).toBe('2026-02-28');
  });
});

describe('durations', () => {
  it('prefers days only for whole days', () => {
    expect(formatHours(168)).toBe('7 ngày');
    expect(formatHours(30)).toBe('30 giờ');
  });

  it('counts down and stops at zero', () => {
    expect(formatCountdown(125_000)).toBe('2:05');
    expect(formatCountdown(3_725_000)).toBe('1:02:05');
    expect(formatCountdown(-5)).toBe('0:00');
  });
});
