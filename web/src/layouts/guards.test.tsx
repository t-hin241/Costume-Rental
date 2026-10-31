import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Session } from '../lib/api/types';
import { RequireAuth, RequirePermission } from './guards';

const auth = vi.hoisted(() => ({ value: { session: null as Session | null, loading: false, canAny: (_: string[]): boolean => false } }));

vi.mock('../lib/auth', async (original) => ({ ...await original<typeof import('../lib/auth')>(), useAuth: () => auth.value }));

const staff: Session = { status: 'authenticated', user_id: 'u', roles: ['rental_staff'], permissions: ['booking.read_all'], mfa: false, expires_at: '' };

beforeEach(() => {
  auth.value = { session: null, loading: false, canAny: () => false };
});

function LoginProbe() {
  return <p>{`login ${window.location.pathname}`}</p>;
}

describe('RequireAuth', () => {
  it('sends guests to sign in and remembers where they were going', () => {
    render(
      <MemoryRouter initialEntries={['/account/bookings?x=1']}>
        <Routes>
          <Route path="/login" element={<LoginProbe />} />
          <Route path="/account/*" element={<RequireAuth><p>Riêng tư</p></RequireAuth>} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.queryByText('Riêng tư')).not.toBeInTheDocument();
    expect(screen.getByText(/^login/)).toBeInTheDocument();
  });

  it('lets a signed-in user through', () => {
    auth.value = { session: staff, loading: false, canAny: () => true };
    render(<MemoryRouter><RequireAuth><p>Riêng tư</p></RequireAuth></MemoryRouter>);
    expect(screen.getByText('Riêng tư')).toBeInTheDocument();
  });
});

describe('RequirePermission', () => {
  it('hides the screen without the permission and offers a way back', () => {
    auth.value = { session: staff, loading: false, canAny: () => false };
    render(<MemoryRouter><RequirePermission anyOf={['policy.manage']}><p>Chính sách</p></RequirePermission></MemoryRouter>);
    expect(screen.queryByText('Chính sách')).not.toBeInTheDocument();
    expect(screen.getByText('Bạn không có quyền xem trang này')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Về trang của bạn' })).toHaveAttribute('href', '/ops');
  });

  it('shows the screen with any of the permissions', () => {
    auth.value = { session: staff, loading: false, canAny: () => true };
    render(<MemoryRouter><RequirePermission anyOf={['booking.read_all']}><p>Đơn thuê</p></RequirePermission></MemoryRouter>);
    expect(screen.getByText('Đơn thuê')).toBeInTheDocument();
  });
});
