import { describe, expect, it } from 'vitest';
import {
  assetStatus, bookingFlow, bookingStatus, paymentState, permissionGroups, refundStatus, revenueMismatch, thresholdLevel, type StatusLabel,
} from './labels';

describe('status labels', () => {
  it('always give a state words as well as a colour', () => {
    const groups: Record<string, StatusLabel>[] = [bookingStatus, paymentState, assetStatus, refundStatus, thresholdLevel, revenueMismatch];
    for (const group of groups) {
      for (const status of Object.values(group)) {
        expect(status.label.length).toBeGreaterThan(0);
        expect(status.tone).toBeTruthy();
      }
    }
  });

  it('keep cancelled and expired off the happy path', () => {
    expect(bookingFlow).not.toContain('CANCELLED');
    expect(bookingFlow).not.toContain('EXPIRED');
    expect(bookingFlow.at(-1)).toBe('COMPLETED');
  });

  it('name the revenue permissions of decision D41', () => {
    const all = permissionGroups.flatMap((g) => Object.keys(g.items));
    expect(all).toEqual(expect.arrayContaining(['revenue.read', 'revenue.record', 'payment.reconcile', 'tax.manage']));
  });

  it('never list a permission twice', () => {
    const all = permissionGroups.flatMap((g) => Object.keys(g.items));
    expect(new Set(all).size).toBe(all.length);
  });
});
