import { describe, expect, it } from 'vitest';
import type { Session } from './api/types';
import { homeFor, isBackOffice, safeNext } from './auth';

function session(roles: Session['roles'], permissions: string[]): Session {
  return { status: 'authenticated', user_id: 'u', roles, permissions, mfa: false, expires_at: '' };
}

describe('safeNext', () => {
  it('keeps same-site paths', () => {
    expect(safeNext('/checkout')).toBe('/checkout');
    expect(safeNext('/account/bookings?x=1')).toBe('/account/bookings?x=1');
  });

  it('refuses anything that could leave the site', () => {
    for (const bad of ['https://evil.test', '//evil.test', '/\\evil.test', 'evil', '', null]) {
      expect(safeNext(bad)).toBeNull();
    }
  });
});

describe('landing page', () => {
  it('sends each role to its own area', () => {
    expect(homeFor(session(['customer'], ['booking.read_own']))).toBe('/account');
    expect(homeFor(session(['rental_staff'], ['booking.read_all']))).toBe('/ops');
    expect(homeFor(session(['inventory_manager'], ['inventory.read', 'catalog.manage']))).toBe('/ops');
    expect(homeFor(session(['admin'], ['booking.read_all', 'policy.manage']))).toBe('/admin');
  });

  it('does not treat a customer as back office', () => {
    expect(isBackOffice(session(['customer'], ['booking.read_own']))).toBe(false);
    expect(isBackOffice(session(['finance'], ['booking.read_all']))).toBe(true);
  });
});
