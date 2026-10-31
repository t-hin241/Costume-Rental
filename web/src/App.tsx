import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { lazy, Suspense, useState } from 'react';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { ToastProvider } from './components/feedback';
import { Skeleton } from './components/ui';
import { BackOfficeLayout } from './layouts/BackOfficeLayout';
import { RequireAuth, RequirePermission } from './layouts/guards';
import { AccountLayout, ShopLayout } from './layouts/ShopLayout';
import { ApiError } from './lib/api/client';
import { AuthProvider } from './lib/auth';
import { BagProvider } from './lib/bag';

const Home = lazy(() => import('./features/shop/Home'));
const Catalog = lazy(() => import('./features/shop/Catalog'));
const Product = lazy(() => import('./features/shop/Product'));
const Checkout = lazy(() => import('./features/shop/Checkout'));
const Policies = lazy(() => import('./features/shop/Policies'));
const AuthPages = lazy(() => import('./features/auth/routes'));
const MyBookings = lazy(() => import('./features/account/MyBookings'));
const MyBooking = lazy(() => import('./features/account/MyBooking'));
const Profile = lazy(() => import('./features/account/Profile'));
const Security = lazy(() => import('./features/account/Security'));
const Support = lazy(() => import('./features/account/Support'));
const OpsToday = lazy(() => import('./features/ops/Today'));
const OpsBookings = lazy(() => import('./features/ops/Bookings'));
const OpsBooking = lazy(() => import('./features/ops/BookingDesk'));
const OpsSchedule = lazy(() => import('./features/ops/Schedule'));
const OpsAlerts = lazy(() => import('./features/ops/Alerts'));
const OpsAssets = lazy(() => import('./features/ops/Assets'));
const OpsSupport = lazy(() => import('./features/ops/SupportDesk'));
const AdminDashboard = lazy(() => import('./features/admin/Dashboard'));
const AdminCatalog = lazy(() => import('./features/admin/CatalogAdmin'));
const AdminUsers = lazy(() => import('./features/admin/Users'));
const AdminPermissions = lazy(() => import('./features/admin/Permissions'));
const AdminAudit = lazy(() => import('./features/admin/Audit'));
const AdminPolicies = lazy(() => import('./features/admin/Policies'));
const AdminPromotions = lazy(() => import('./features/admin/Promotions'));

const PaymentReturn = lazy(() => import('./features/payment/PaymentReturn'));
const NotFound = lazy(() => import('./features/NotFound'));
// Never reaches production: __USE_MOCKS__ is false there, so this is not bundled.
const MockGateway = __USE_MOCKS__ ? lazy(() => import('./mocks/MockGateway')) : null;

function createClient() {
  const client: QueryClient = new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        refetchOnWindowFocus: false,
        // Only network and server failures are worth retrying.
        retry: (count, err) => count < 2 && err instanceof ApiError && (err.status === 0 || err.status >= 500),
      },
    },
    // An expired session anywhere refreshes the session query, and the route
    // guards then send the user to sign in.
    queryCache: new QueryCache({ onError: (err, query) => onAuthError(client, err, query.queryKey[0]) }),
    mutationCache: new MutationCache({ onError: (err) => onAuthError(client, err, undefined) }),
  });
  return client;
}

function onAuthError(client: QueryClient, err: unknown, root: unknown) {
  if (err instanceof ApiError && err.code === 'identity.unauthenticated' && root !== 'session') {
    client.setQueryData(['session'], null);
  }
}

const page = <div className="container"><Skeleton height={320} /></div>;

export function App() {
  const [client] = useState(createClient);
  return (
    <QueryClientProvider client={client}>
      <ToastProvider>
        <AuthProvider>
          <BagProvider>
            <BrowserRouter>
              {__USE_MOCKS__ && <div className="mock-banner">Dữ liệu mô phỏng: chế độ phát triển, không phải dữ liệu thật</div>}
              <Suspense fallback={page}>
                <Routes>
                  <Route element={<ShopLayout />}>
                    <Route index element={<Home />} />
                    <Route path="catalog" element={<Catalog />} />
                    <Route path="catalog/:kind/:slug" element={<Product />} />
                    <Route path="checkout" element={<Checkout />} />
                    <Route path="policies" element={<Policies />} />
                    <Route path="payment/return" element={<RequireAuth><PaymentReturn /></RequireAuth>} />
                    <Route path="account" element={<RequireAuth><AccountLayout /></RequireAuth>}>
                      <Route index element={<Navigate to="bookings" replace />} />
                      <Route path="bookings" element={<MyBookings />} />
                      <Route path="bookings/:id" element={<MyBooking />} />
                      <Route path="profile" element={<Profile />} />
                      <Route path="security" element={<Security />} />
                      <Route path="support" element={<Support />} />
                    </Route>
                  </Route>

                  <Route path="/*" element={<AuthPagesOrNotFound />} />
                  {MockGateway && <Route path="mock-gateway/:id" element={<MockGateway />} />}

                  <Route element={<RequireAuth><BackOfficeLayout /></RequireAuth>}>
                    <Route path="ops" element={<RequirePermission anyOf={['booking.read_all']}><OpsToday /></RequirePermission>} />
                    <Route path="ops/bookings" element={<RequirePermission anyOf={['booking.read_all']}><OpsBookings /></RequirePermission>} />
                    <Route path="ops/bookings/:id" element={<RequirePermission anyOf={['booking.read_all']}><OpsBooking /></RequirePermission>} />
                    <Route path="ops/schedule" element={<RequirePermission anyOf={['booking.read_all']}><OpsSchedule /></RequirePermission>} />
                    <Route path="ops/alerts" element={<RequirePermission anyOf={['booking.read_all', 'inventory.read']}><OpsAlerts /></RequirePermission>} />
                    <Route path="ops/assets" element={<RequirePermission anyOf={['inventory.read']}><OpsAssets /></RequirePermission>} />
                    <Route path="ops/support" element={<RequirePermission anyOf={['booking.read_all']}><OpsSupport /></RequirePermission>} />
                    <Route path="admin" element={<RequirePermission anyOf={['revenue.read', 'policy.manage']}><AdminDashboard /></RequirePermission>} />
                    <Route path="admin/catalog" element={<RequirePermission anyOf={['catalog.manage']}><AdminCatalog /></RequirePermission>} />
                    <Route path="admin/users" element={<RequirePermission anyOf={['user.role_manage', 'user.lock']}><AdminUsers /></RequirePermission>} />
                    <Route path="admin/permissions" element={<RequirePermission anyOf={['policy.manage']}><AdminPermissions /></RequirePermission>} />
                    <Route path="admin/audit" element={<RequirePermission anyOf={['audit.read']}><AdminAudit /></RequirePermission>} />
                    <Route path="admin/policies" element={<RequirePermission anyOf={['policy.manage']}><AdminPolicies /></RequirePermission>} />
                    <Route path="admin/promotions" element={<RequirePermission anyOf={['promotion.manage']}><AdminPromotions /></RequirePermission>} />
                  </Route>
                </Routes>
              </Suspense>
            </BrowserRouter>
          </BagProvider>
        </AuthProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

function AuthPagesOrNotFound() {
  return <AuthPages fallback={<NotFound />} />;
}
