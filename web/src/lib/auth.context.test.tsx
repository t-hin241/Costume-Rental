import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { act, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from './api/client';
import { authApi } from './api/endpoints';
import type { Session } from './api/types';
import { AuthProvider, useAuth } from './auth';

const session: Session = { status: 'authenticated', user_id: 'u1', roles: ['customer'], permissions: ['booking.read_own'], mfa: false, expires_at: '' };

let auth: ReturnType<typeof useAuth>;

function Probe() {
  auth = useAuth();
  return <p>{auth.loading ? 'loading' : auth.session?.user_id ?? 'guest'}</p>;
}

function CachedRow({ client }: { client: QueryClient }) {
  useQuery({ queryKey: ['bookings', 'mine'], queryFn: () => Promise.resolve('private'), staleTime: Infinity }, client);
  return null;
}

function setup(children?: ReactNode) {
  vi.spyOn(authApi, 'me').mockRejectedValue(new ApiError({ status: 401, code: 'identity.unauthenticated' }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><AuthProvider><Probe />{children}</AuthProvider></QueryClientProvider>);
  return client;
}

describe('AuthProvider', () => {
  it('treats a 401 from /auth/me as a guest, not an error', async () => {
    setup();
    expect(await screen.findByText('guest')).toBeInTheDocument();
  });

  it('sees the new session immediately after sign-in, so guards do not bounce the user', async () => {
    setup();
    await screen.findByText('guest');
    act(() => auth.setSession(session));
    expect(await screen.findByText('u1')).toBeInTheDocument();
    expect(auth.can('booking.read_own')).toBe(true);
  });

  it('drops the previous user data on sign-in and on sign-out', async () => {
    vi.spyOn(authApi, 'logout').mockResolvedValue();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    vi.spyOn(authApi, 'me').mockRejectedValue(new ApiError({ status: 401, code: 'identity.unauthenticated' }));
    render(<QueryClientProvider client={client}><AuthProvider><Probe /><CachedRow client={client} /></AuthProvider></QueryClientProvider>);
    await screen.findByText('guest');
    await waitFor(() => expect(client.getQueryData(['bookings', 'mine'])).toBe('private'));

    act(() => auth.setSession(session));
    expect(client.getQueryData(['bookings', 'mine'])).toBeUndefined();

    client.setQueryData(['bookings', 'mine'], 'private');
    await act(() => auth.signOut());
    expect(client.getQueryData(['bookings', 'mine'])).toBeUndefined();
    expect(await screen.findByText('guest')).toBeInTheDocument();
  });

  it('clears the session locally even if the logout request fails', async () => {
    setup();
    await screen.findByText('guest');
    act(() => auth.setSession(session));
    await screen.findByText('u1');
    vi.spyOn(authApi, 'logout').mockRejectedValue(new ApiError({ status: 0, code: 'request.network' }));
    await act(async () => { await auth.signOut().catch(() => undefined); });
    expect(await screen.findByText('guest')).toBeInTheDocument();
  });
});
