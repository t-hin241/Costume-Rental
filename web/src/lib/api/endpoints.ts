import { api } from './client';
import type {
  AdminBundle, AdminItem, AdminUser, Asset, AssetAction, AssetHistoryEntry, AssetStatus, AuditEvent, Availability, BookingDetail,
  BookingInput, BookingStatus, BookingSummary, Bucket, Campaign, CancellationPreview, Category, Condition, Dashboard, FeeAssessment, FeeKind,
  FulfillmentOptions, LoginResult, MFAEnrollment, Money, OfferingDetail, OfferingKind, OfferingPage, OpsAlerts, OpsOverview, Page,
  PaymentChannel, PaymentMethod, PaymentPurpose, PaymentSession, PermissionMatrix, PolicyDocument, Profile, PublicPolicy, Quote, RentalPackage, Role, ScheduleEvent, Session, StoreCalendar,
  StoreInfo, SupportRequest, SupportTopic, User,
} from './types';

// Accounts and access.
export const authApi = {
  me: () => api<Session>('/auth/me'),
  register: (email: string, password: string) => api<void>('/auth/register', { method: 'POST', body: { email, password } }),
  verifyEmail: (token: string) => api<void>('/auth/verify-email', { method: 'POST', body: { token } }),
  resendVerification: (email: string) => api<void>('/auth/verify-email/resend', { method: 'POST', body: { email } }),
  login: (email: string, password: string) => api<LoginResult>('/auth/login', { method: 'POST', body: { email, password } }),
  loginEnroll: (challenge: string) => api<MFAEnrollment>('/auth/login/mfa/enroll', { method: 'POST', body: { challenge } }),
  loginMFA: (challenge: string, code: string) => api<Session>('/auth/login/mfa', { method: 'POST', body: { challenge, code } }),
  logout: () => api<void>('/auth/logout', { method: 'POST' }),
  changePassword: (current_password: string, new_password: string) =>
    api<void>('/auth/password', { method: 'POST', body: { current_password, new_password } }),
  requestReset: (email: string) => api<void>('/auth/password-reset', { method: 'POST', body: { email } }),
  confirmReset: (token: string, new_password: string) =>
    api<void>('/auth/password-reset/confirm', { method: 'POST', body: { token, new_password } }),
  mfaSetup: () => api<MFAEnrollment>('/auth/mfa/setup', { method: 'POST' }),
  mfaConfirm: (code: string) => api<{ recovery_codes: string[] }>('/auth/mfa/setup/confirm', { method: 'POST', body: { code } }),
  mfaDisable: (code: string) => api<void>('/auth/mfa/disable', { method: 'POST', body: { code } }),
};

export const accessApi = {
  user: (id: string) => api<User>(`/admin/users/${encodeURIComponent(id)}`),
  grantRole: (id: string, role: Role, reason: string) =>
    api<void>(`/admin/users/${encodeURIComponent(id)}/roles/${role}`, { method: 'PUT', body: { reason } }),
  revokeRole: (id: string, role: Role, reason: string) =>
    api<void>(`/admin/users/${encodeURIComponent(id)}/roles/${role}`, { method: 'DELETE', body: { reason } }),
  lock: (id: string, reason: string) => api<void>(`/admin/users/${encodeURIComponent(id)}/lock`, { method: 'POST', body: { reason } }),
  unlock: (id: string, reason: string) => api<void>(`/admin/users/${encodeURIComponent(id)}/unlock`, { method: 'POST', body: { reason } }),
  resetMFA: (id: string, reason: string) =>
    api<void>(`/admin/users/${encodeURIComponent(id)}/mfa-reset`, { method: 'POST', body: { reason } }),
  matrix: () => api<PermissionMatrix>('/admin/permission-matrix'),
  publishMatrix: (body: PermissionMatrix & { reason: string }) =>
    api<PermissionMatrix>('/admin/permission-matrix', { method: 'POST', body }),
};

// The storefront, booking, operations and admin API. web/API_CONTRACT.md lists
// what it serves and the parts that wait on an outside provider.

export interface OfferingQuery {
  q?: string;
  category?: string;
  kind?: OfferingKind | '';
  size?: string;
  color?: string;
  sort?: string;
  page?: number;
  page_size?: number;
}

export const catalogApi = {
  categories: () => api<Category[]>('/catalog/categories'),
  packages: () => api<RentalPackage[]>('/catalog/packages'),
  offerings: (query: OfferingQuery) => api<OfferingPage>('/catalog/offerings', { query: { ...query } }),
  offering: (kind: string, slug: string) => api<OfferingDetail>(`/catalog/offerings/${kind}/${encodeURIComponent(slug)}`),
};

export const storeApi = {
  info: () => api<StoreInfo>('/store/info'),
  calendar: (from: string, to: string) => api<StoreCalendar>('/store/calendar', { query: { from, to } }),
  fulfillment: () => api<FulfillmentOptions>('/store/fulfillment-options'),
  policy: () => api<PublicPolicy>('/policies/public'),
};

export interface BookingQuery {
  q?: string;
  status?: BookingStatus | '';
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
}

export interface HandoverInput {
  checklist_completed: boolean;
  delivered: { asset_id: string; preexisting_issues: string }[];
}

export interface InspectionInput {
  asset_id: string;
  conditions: Condition[];
  notes: string;
  evidence: string[];
}

export interface FeeInput {
  asset_id: string;
  kind: FeeKind;
  amount: Money;
  basis: string;
  evidence: string[];
  confirmed_new_damage: boolean;
}

export interface PaymentInput {
  idempotency_key: string;
  method: PaymentMethod;
  portions: { bucket: Bucket; amount: Money }[];
}

export interface RefundPayoutInput {
  method: 'CASH' | 'BANK_TRANSFER';
  reference: string;
  paid_at: string;
}

const booking = (id: string) => `/bookings/${encodeURIComponent(id)}`;

export const bookingApi = {
  availability: (body: BookingInput) => api<Availability>('/availability', { method: 'POST', body }),
  quote: (body: BookingInput) => api<Quote>('/quotes', { method: 'POST', body }),
  place: (body: BookingInput & { request_key: string }) => api<BookingDetail>('/bookings', { method: 'POST', body }),
  mine: (query: { scope: 'active' | 'history'; page: number; page_size: number }) =>
    api<Page<BookingSummary>>('/me/bookings', { query: { ...query } }),
  list: (query: BookingQuery) => api<Page<BookingSummary>>('/bookings', { query: { ...query } }),
  get: (id: string) => api<BookingDetail>(booking(id)),
  cancellationPreview: (id: string, reason?: 'NO_SHOW' | 'STAFF_DECISION') =>
    api<CancellationPreview>(`${booking(id)}/cancellation-preview`, { query: { reason } }),
  cancel: (id: string, reason: 'CUSTOMER_REQUEST' | 'NO_SHOW' | 'STAFF_DECISION', note: string) =>
    api<BookingDetail>(`${booking(id)}/cancel`, { method: 'POST', body: { reason, note } }),
  reschedule: (id: string, pickup_date: string, slot_id: string) =>
    api<BookingDetail>(`${booking(id)}/reschedule`, { method: 'POST', body: { pickup_date, slot_id } }),
  confirm: (id: string) => api<BookingDetail>(`${booking(id)}/confirm`, { method: 'POST' }),
  startPreparation: (id: string) => api<BookingDetail>(`${booking(id)}/start-preparation`, { method: 'POST' }),
  handover: (id: string, body: HandoverInput) => api<BookingDetail>(`${booking(id)}/handover`, { method: 'POST', body }),
  recordReturn: (id: string, asset_ids: string[]) => api<BookingDetail>(`${booking(id)}/returns`, { method: 'POST', body: { asset_ids } }),
  reportMissing: (id: string, asset_id: string) => api<BookingDetail>(`${booking(id)}/report-missing`, { method: 'POST', body: { asset_id } }),
  confirmLost: (id: string, asset_id: string, reason: string) =>
    api<BookingDetail>(`${booking(id)}/confirm-lost`, { method: 'POST', body: { asset_id, reason } }),
  inspect: (id: string, body: InspectionInput) => api<BookingDetail>(`${booking(id)}/inspections`, { method: 'POST', body }),
  proposeFee: (id: string, body: FeeInput) => api<FeeAssessment>(`${booking(id)}/fees`, { method: 'POST', body }),
  recordPayment: (id: string, body: PaymentInput) => api<BookingDetail>(`${booking(id)}/payments`, { method: 'POST', body }),
  decideFee: (id: string, feeId: string, decision: 'APPROVE' | 'REJECT' | 'WAIVE', note: string) =>
    api<BookingDetail>(`${booking(id)}/fees/${encodeURIComponent(feeId)}/decision`, { method: 'POST', body: { decision, note } }),
  approveRefund: (id: string, refundId: string) =>
    api<BookingDetail>(`${booking(id)}/refunds/${encodeURIComponent(refundId)}/approve`, { method: 'POST' }),
  rejectRefund: (id: string, refundId: string, note: string) =>
    api<BookingDetail>(`${booking(id)}/refunds/${encodeURIComponent(refundId)}/reject`, { method: 'POST', body: { note } }),
  payoutRefund: (id: string, refundId: string, body: RefundPayoutInput) =>
    api<BookingDetail>(`${booking(id)}/refunds/${encodeURIComponent(refundId)}/payout`, { method: 'POST', body }),
  settle: (id: string) => api<BookingDetail>(`${booking(id)}/settle`, { method: 'POST' }),
  complete: (id: string) => api<BookingDetail>(`${booking(id)}/complete`, { method: 'POST' }),
};

export const paymentApi = {
  createSession: (bookingId: string, body: { purpose: PaymentPurpose; channel: PaymentChannel; idempotency_key: string }) =>
    api<PaymentSession>(`${booking(bookingId)}/payment-sessions`, { method: 'POST', body }),
  session: (id: string) => api<PaymentSession>(`/payment-sessions/${encodeURIComponent(id)}`),
};

export const opsApi = {
  overview: () => api<OpsOverview>('/ops/overview'),
  schedule: (from: string, to: string) => api<ScheduleEvent[]>('/ops/schedule', { query: { from, to } }),
  alerts: () => api<OpsAlerts>('/ops/alerts'),
};

export interface AssetQuery {
  q?: string;
  status?: AssetStatus | '';
  page?: number;
  page_size?: number;
}

export const inventoryApi = {
  assets: (query: AssetQuery) => api<Page<Asset>>('/inventory/assets', { query: { ...query } }),
  history: (id: string) => api<AssetHistoryEntry[]>(`/inventory/assets/${encodeURIComponent(id)}/history`),
  changeStatus: (id: string, body: { action: AssetAction; reason: string; checklist_completed: boolean }) =>
    api<Asset>(`/inventory/assets/${encodeURIComponent(id)}/status`, { method: 'POST', body }),
  register: (code: string, variant_id: string) => api<Asset>('/inventory/assets', { method: 'POST', body: { code, variant_id } }),
};

export const accountApi = {
  profile: () => api<Profile>('/me/profile'),
  saveProfile: (body: Pick<Profile, 'full_name' | 'phone'>) => api<Profile>('/me/profile', { method: 'PUT', body }),
  supportRequests: () => api<SupportRequest[]>('/me/support-requests'),
  createSupportRequest: (body: { topic: SupportTopic; subject: string; booking_code: string; message: string }) =>
    api<SupportRequest>('/me/support-requests', { method: 'POST', body }),
};

export const supportApi = {
  list: () => api<SupportRequest[]>('/support-requests'),
  reply: (id: string, body: { message: string; resolve: boolean }) =>
    api<SupportRequest>(`/support-requests/${encodeURIComponent(id)}/replies`, { method: 'POST', body }),
};

export interface AuditQuery {
  q?: string;
  result?: string;
  page?: number;
  page_size?: number;
}

export type ItemInput = Omit<AdminItem, 'id' | 'version' | 'asset_count'>;
export type BundleInput = Omit<AdminBundle, 'id' | 'version'>;

export const adminApi = {
  dashboard: (range_days: number) => api<Dashboard>('/admin/dashboard', { query: { range_days } }),
  users: (query: { q?: string; role?: string; page?: number; page_size?: number }) =>
    api<Page<AdminUser>>('/admin/users', { query: { ...query } }),
  audit: (query: AuditQuery) => api<Page<AuditEvent>>('/admin/audit-events', { query: { ...query } }),
  items: () => api<AdminItem[]>('/admin/catalog/items'),
  saveItem: (id: string | null, body: ItemInput) =>
    id ? api<AdminItem>(`/admin/catalog/items/${id}`, { method: 'PUT', body }) : api<AdminItem>('/admin/catalog/items', { method: 'POST', body }),
  bundles: () => api<AdminBundle[]>('/admin/catalog/bundles'),
  saveBundle: (id: string | null, body: BundleInput) =>
    id ? api<AdminBundle>(`/admin/catalog/bundles/${id}`, { method: 'PUT', body }) : api<AdminBundle>('/admin/catalog/bundles', { method: 'POST', body }),
  saveCategory: (id: string | null, body: { name: string; slug: string }) =>
    id ? api<Category>(`/admin/catalog/categories/${id}`, { method: 'PUT', body }) : api<Category>('/admin/catalog/categories', { method: 'POST', body }),
  savePackage: (id: string | null, body: { code: string; label: string; duration_hours: number; status: string }) =>
    id ? api<RentalPackage>(`/admin/catalog/packages/${id}`, { method: 'PUT', body }) : api<RentalPackage>('/admin/catalog/packages', { method: 'POST', body }),
  policy: () => api<PolicyDocument>('/admin/policies/active'),
  publishPolicy: (body: { policy: PolicyDocument; reason: string }) => api<PolicyDocument>('/admin/policies', { method: 'POST', body }),
  campaigns: () => api<Campaign[]>('/admin/promotions'),
  saveCampaign: (id: string | null, body: Omit<Campaign, 'id' | 'used'>) =>
    id ? api<Campaign>(`/admin/promotions/${id}`, { method: 'PUT', body }) : api<Campaign>('/admin/promotions', { method: 'POST', body }),
};
