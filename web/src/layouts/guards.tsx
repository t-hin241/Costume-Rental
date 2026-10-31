import type { ReactNode } from 'react';
import { Link, Navigate, useLocation } from 'react-router-dom';
import { EmptyState, Skeleton, btn } from '../components/ui';
import type { Permission } from '../lib/api/types';
import { homeFor, useAuth } from '../lib/auth';

/** Sends guests to sign in, then back to where they were going. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { session, loading } = useAuth();
  const location = useLocation();
  if (loading) return <div className="container"><Skeleton height={280} /></div>;
  if (!session) {
    const next = encodeURIComponent(location.pathname + location.search);
    return <Navigate to={`/login?next=${next}`} replace />;
  }
  return <>{children}</>;
}

/** Hides screens from users without access. The API still enforces every call. */
export function RequirePermission({ anyOf, children }: { anyOf: Permission[]; children: ReactNode }) {
  const { session, canAny } = useAuth();
  if (!canAny(anyOf)) {
    return (
      <EmptyState icon="shield" title="Bạn không có quyền xem trang này">
        <p>Nếu cần truy cập, hãy liên hệ quản trị viên để được cấp quyền.</p>
        {session && <Link className={btn('outline', 'sm')} to={homeFor(session)}>Về trang của bạn</Link>}
      </EmptyState>
    );
  }
  return <>{children}</>;
}
