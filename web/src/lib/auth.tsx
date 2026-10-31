import { useQuery, useQueryClient } from '@tanstack/react-query';
import { createContext, useCallback, useContext, useMemo, type ReactNode } from 'react';
import { ApiError } from './api/client';
import { authApi } from './api/endpoints';
import type { Permission, Session } from './api/types';

const sessionKey = ['session'];

interface AuthValue {
  session: Session | null;
  loading: boolean;
  /** For shaping the UI only: the backend checks every action. */
  can: (permission: Permission) => boolean;
  canAny: (permissions: Permission[]) => boolean;
  setSession: (s: Session) => void;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  const { data, isPending } = useQuery({
    queryKey: sessionKey,
    queryFn: async () => {
      try {
        return await authApi.me();
      } catch (err) {
        if (err instanceof ApiError && err.unauthenticated) return null;
        throw err;
      }
    },
    retry: false,
    staleTime: 60_000,
  });
  const session = data ?? null;

  // Another user's cached data must never survive a sign-in or sign-out. The
  // session query itself stays in the cache: this provider is observing it.
  const replaceSession = useCallback((s: Session | null) => {
    client.removeQueries({ predicate: (q) => q.queryKey[0] !== sessionKey[0] });
    client.setQueryData(sessionKey, s);
  }, [client]);

  const signOut = useCallback(async () => {
    try {
      await authApi.logout();
    } finally {
      replaceSession(null);
    }
  }, [replaceSession]);

  const value = useMemo<AuthValue>(() => {
    const perms = new Set(session?.permissions ?? []);
    return {
      session,
      loading: isPending,
      can: (p) => perms.has(p),
      canAny: (ps) => ps.some((p) => perms.has(p)),
      setSession: replaceSession,
      signOut,
    };
  }, [session, isPending, replaceSession, signOut]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error('useAuth must be used inside AuthProvider');
  return value;
}

/** Permissions that open a back-office area, in landing-page order. */
const adminAreaPermissions: Permission[] = ['policy.manage', 'user.role_manage', 'catalog.manage', 'audit.read', 'revenue.read'];
const opsAreaPermissions: Permission[] = ['booking.read_all', 'inventory.read'];

export function isBackOffice(s: Session): boolean {
  return [...adminAreaPermissions, ...opsAreaPermissions].some((p) => s.permissions.includes(p));
}

/** Where a user lands after signing in. */
export function homeFor(s: Session): string {
  if (s.roles.includes('admin')) return '/admin';
  if (s.permissions.includes('booking.read_all') || s.permissions.includes('inventory.read')) return '/ops';
  if (isBackOffice(s)) return '/admin';
  return '/account';
}

/** Accepts only same-site paths, so a crafted ?next= cannot redirect away. */
export function safeNext(next: string | null): string | null {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return null;
  return next;
}
