import { http, HttpResponse, type HttpHandler } from 'msw';
import type {
  AssetAction, AssetStatus, Availability, BookingInput, BookingItem, BookingLine, CalendarDay, Condition, Dashboard, FeeKind, OfferingCard, Page, PaymentChannel, PaymentPurpose, PaymentSession, PublicPolicy, Quote, Role, Session, ChargeKind,
} from '../lib/api/types';
import { addBusinessDays, businessToday } from '../lib/format';
import { actionsFor, assetActions, bundleCard, bundleDeposit, bundleDetail, conditionToAsset, db, demoPassword, detailOf, financeOf, isOverdue, itemCard, itemDetail, nextCode, paymentOptions, persist, summaryOf, vnd, type MockBooking, type MockPaymentSession } from './db';

// A stand-in for the proposed API in web/API_CONTRACT.md, for local development
// only. Its rules imitate the contract so every screen can be walked through.

const base = '/api/v1';
const hour = 3_600_000;
const sessionKey = 'webee.mock.session';
const mfaCode = '123456';

function fail(status: number, code: string, fields?: { field: string; reason: string }[]) {
  return HttpResponse.json({ error: { code, message: code, fields }, request_id: `mock-${Math.random().toString(16).slice(2, 10)}` }, { status });
}

class Refused extends Error {
  constructor(readonly status: number, readonly code: string) { super(code); }
}

function currentUser() {
  const id = sessionStorage.getItem(sessionKey);
  return db.users.find((u) => u.id === id) ?? null;
}

function permsOf(roles: Role[]): string[] {
  return [...new Set(roles.flatMap((r) => db.matrix.matrix[r]))].sort();
}

function sessionOf(u: NonNullable<ReturnType<typeof currentUser>>): Session {
  return { status: 'authenticated', user_id: u.id, roles: u.roles, permissions: permsOf(u.roles), mfa: u.mfa_enabled, expires_at: new Date(Date.now() + 12 * hour).toISOString() };
}

function requireUser() {
  const u = currentUser();
  if (!u) throw new Refused(401, 'identity.unauthenticated');
  const perms = new Set(permsOf(u.roles));
  return { user: u, can: (p: string) => perms.has(p) };
}

function requirePerm(...anyOf: string[]) {
  const ctx = requireUser();
  if (!anyOf.some(ctx.can)) throw new Refused(403, 'identity.forbidden');
  return ctx;
}

function loadBooking(id: string | readonly string[] | undefined) {
  const b = db.bookings.find((x) => x.id === id);
  if (!b) throw new Refused(404, 'storage.not_found');
  return b;
}

function bookingAccess(id: string | readonly string[] | undefined) {
  const ctx = requireUser();
  const b = loadBooking(id);
  const own = b.customer_id === ctx.user.id;
  if (!own && !ctx.can('booking.read_all')) throw new Refused(404, 'storage.not_found');
  return { ...ctx, b, own };
}

/** Wraps a resolver: mock refusals become the API's error format, state is saved. */
function route<A extends { request: Request; params: Record<string, string | readonly string[] | undefined> }>(fn: (args: A) => Response | Promise<Response>) {
  return async (args: A) => {
    try {
      const res = await fn(args);
      persist();
      return res;
    } catch (err) {
      if (err instanceof Refused) return fail(err.status, err.code);
      throw err;
    }
  };
}

function paged<T>(rows: T[], url: URL): Page<T> {
  const page = Number(url.searchParams.get('page')) || 1;
  const size = Number(url.searchParams.get('page_size')) || 20;
  return { items: rows.slice((page - 1) * size, page * size), page, page_size: size, total: rows.length };
}

const norm = (s: string) => s.normalize('NFD').replace(/\p{M}/gu, '').replace(/đ/gi, 'd').toLowerCase();

function audit(actor: string, action: string, resource_type: string, resource_id: string, reason = '') {
  db.audit.unshift({ id: `e${Date.now()}`, occurred_at: new Date().toISOString(), actor, action, resource_type, resource_id, result: 'SUCCESS', reason, changes: {} });
}

// ---- Catalog and calendar ----

interface CardRow { card: OfferingCard; popularity: number; active: boolean }

function offerings(): CardRow[] {
  return [
    ...db.items.filter((i) => i.individually_rentable).map((i) => ({ card: itemCard(i), popularity: i.popularity, active: i.status === 'ACTIVE' })),
    ...db.bundles.map((b) => ({ card: bundleCard(b), popularity: b.popularity, active: b.status === 'ACTIVE' })),
  ].filter((r) => r.active);
}

const weekdayClosed = 1; // Monday in this demo

function calendarDay(date: string): CalendarDay {
  const today = businessToday();
  const min = addBusinessDays(today, Math.ceil(db.policy.booking.min_lead_hours / 24));
  const max = addBusinessDays(today, Math.floor(db.policy.booking.max_advance_hours / 24));
  const weekday = new Date(`${date}T00:00:00Z`).getUTCDay();
  if (date < min) return { date, open: false, reason: 'Cần đặt trước thời gian tối thiểu', slots: [] };
  if (date > max) return { date, open: false, reason: 'Quá xa so với thời gian được đặt trước', slots: [] };
  if (weekday === weekdayClosed) return { date, open: false, reason: 'Cửa hàng nghỉ', slots: [] };
  return { date, open: true, reason: null, slots: [{ id: 'morning', label: '09:00 đến 12:00' }, { id: 'afternoon', label: '14:00 đến 18:00' }] };
}

function schedule(input: BookingInput) {
  const pkg = db.packages.find((p) => p.id === input.package_id);
  if (!pkg) throw new Refused(400, 'catalog.package_unavailable');
  const day = calendarDay(input.pickup_date);
  if (!day.open) throw new Refused(400, 'rentals.store_closed');
  const start = input.slot_id === 'morning' ? '09:00' : '14:00';
  const pickup = new Date(`${input.pickup_date}T${start}:00+07:00`).getTime();
  const due = pickup + pkg.duration_hours * hour;
  const dueDate = businessToday(new Date(due));
  if (new Date(`${dueDate}T00:00:00Z`).getUTCDay() === weekdayClosed) throw new Refused(400, 'rentals.return_outside_slots');
  return { pkg, pickup, due };
}

interface Priced {
  label: string;
  selection: string;
  kind: 'ITEM' | 'BUNDLE';
  name: string;
  image: BookingLine['image'];
  quantity: number;
  rent: number;
  security: number;
  variants: string[];
}

function priceLines(input: BookingInput): Priced[] {
  return input.lines.map((l) => {
    if (l.kind === 'ITEM') {
      const it = db.items.find((i) => i.id === l.offering_id);
      const variant = it?.variants.find((v) => v.id === l.variant_id);
      const price = it?.prices.find((p) => p.package_id === input.package_id);
      if (!it || !variant) throw new Refused(400, 'catalog.variant_unavailable');
      if (!price) throw new Refused(400, 'catalog.no_price_for_package');
      return {
        label: `${it.name} (${variant.size})`, selection: `Size ${variant.size}`, kind: 'ITEM', name: it.name, image: it.images[0] ?? null, quantity: l.quantity,
        rent: price.amount.amount * l.quantity, security: it.security_deposit.amount * l.quantity, variants: Array.from({ length: l.quantity }, () => variant.id),
      };
    }
    const b = db.bundles.find((x) => x.id === l.offering_id);
    const price = b?.prices.find((p) => p.package_id === input.package_id);
    if (!b) throw new Refused(400, 'catalog.offering_unavailable');
    if (!price) throw new Refused(400, 'catalog.no_price_for_package');
    if (b.components.some((c) => !l.selections?.[c.key])) throw new Refused(400, 'catalog.invalid_selection');
    const variants = b.components.flatMap((c) => Array.from({ length: c.quantity * l.quantity }, () => l.selections?.[c.key] as string));
    const sizes = b.components.map((c) => db.items.find((i) => i.id === c.item_id)?.variants.find((v) => v.id === l.selections?.[c.key])?.size);
    return {
      label: b.name, selection: `Size ${sizes.join(' / ')}`, kind: 'BUNDLE', name: b.name, image: b.images[0] ?? null, quantity: l.quantity,
      rent: price.amount.amount * l.quantity, security: bundleDeposit(b) * l.quantity, variants,
    };
  });
}

function blockOf(pickup: number, due: number) {
  const { default_preparation_hours: prep, default_turnaround_hours: turn } = db.policy.buffers;
  return { start: pickup - prep * hour, end: due + turn * hour };
}

function freeAssets(variant: string, pickup: number, due: number, exceptBooking?: string) {
  const block = blockOf(pickup, due);
  const busy = new Set<string>();
  for (const b of db.bookings) {
    if (b.id === exceptBooking || ['CANCELLED', 'EXPIRED', 'COMPLETED'].includes(b.status)) continue;
    const other = blockOf(new Date(b.pickup_at).getTime(), new Date(b.due_at).getTime());
    if (other.start < block.end && block.start < other.end) b.items.forEach((i) => busy.add(i.asset_id));
  }
  // Decision D33: only READY assets count, however far ahead the booking is.
  return db.assets.filter((a) => a.variant_id === variant && !busy.has(a.id) && a.status === 'READY');
}

function checkAvailability(input: BookingInput): Availability {
  const { pickup, due } = schedule(input);
  const lines = priceLines(input);
  const wanted = new Map<string, number>();
  lines.forEach((l) => l.variants.forEach((v) => wanted.set(v, (wanted.get(v) ?? 0) + 1)));
  const shortages: Availability['shortages'] = [];
  for (const [variant, requested] of wanted) {
    const available = freeAssets(variant, pickup, due).length;
    if (available < requested) {
      const a = db.assets.find((x) => x.variant_id === variant);
      shortages.push({ line_ref: variant, label: `${a?.item_name ?? ''} (${a?.size ?? ''})`, requested, available });
    }
  }
  return { available: shortages.length === 0, shortages, pickup_at: new Date(pickup).toISOString(), due_at: new Date(due).toISOString() };
}

// In this demo the first late booking endangers the next confirmed one.
function conflictsOf() {
  const late = db.bookings.find(isOverdue);
  const upcoming = db.bookings.find((b) => b.status === 'CONFIRMED');
  if (!late || !upcoming) return [];
  return [{ overdue_booking: summaryOf(late), asset_code: late.items[0]?.asset_code ?? '', item_name: late.items[0]?.name ?? '', endangered: [summaryOf(upcoming)] }];
}

function deliveryFee(mode: string) {
  return mode === 'COURIER' ? 30000 : 0;
}

function buildQuote(input: BookingInput): Quote {
  const { pickup, due } = schedule(input);
  const lines = priceLines(input);
  const gross = lines.reduce((n, l) => n + l.rent, 0);
  let discount = 0;
  if (input.coupon_code) {
    const c = db.campaigns.find((x) => x.code === input.coupon_code);
    if (!c) throw new Refused(400, 'promotions.coupon_not_found');
    if (c.status !== 'ACTIVE') throw new Refused(400, 'promotions.coupon_inactive');
    discount = c.value_type === 'PERCENT' ? Math.floor(gross * ((c.percent ?? 0) / 100)) : Math.min(c.amount?.amount ?? 0, gross);
  }
  const rent = gross - discount;
  const security = lines.reduce((n, l) => n + l.security, 0);
  const out = deliveryFee(input.outbound);
  const back = deliveryFee(input.return);
  const reservation = Math.ceil(rent * ((db.policy.reservation.rent_percent ?? 0) / 100));
  return {
    lines: lines.map((l) => ({ label: l.label, quantity: l.quantity, list_rent: vnd(l.rent), discount: vnd(0), rent: vnd(l.rent), security: vnd(l.security) })),
    pickup_at: new Date(pickup).toISOString(), due_at: new Date(due).toISOString(), gross_rent: vnd(gross), discount_total: vnd(discount), rent_total: vnd(rent), reservation_deposit: vnd(reservation),
    reservation_treatment: db.policy.reservation.treatment, security_total: vnd(security), delivery_outbound_fee: vnd(out), delivery_return_fee: vnd(back), due_to_confirm: vnd(reservation),
    due_before_handover: vnd(rent + security + out + back), refundable_total: vnd(security), hold_minutes: db.policy.booking.hold_minutes, policy_version: db.policy.version,
  };
}

function placeBooking(input: BookingInput, customerId: string): MockBooking {
  const availability = checkAvailability(input);
  if (!availability.available) throw new Refused(409, 'inventory.insufficient');
  const { pkg, pickup, due } = schedule(input);
  const priced = priceLines(input);
  const quote = buildQuote(input);
  const code = nextCode('BK');
  const items: BookingItem[] = [];
  const taken = new Set<string>();
  priced.forEach((l, i) => {
    for (const variant of l.variants) {
      const asset = freeAssets(variant, pickup, due).find((a) => !taken.has(a.id));
      if (!asset) throw new Refused(409, 'inventory.insufficient');
      taken.add(asset.id);
      asset.booking_code = code;
      items.push({ asset_id: asset.id, asset_code: asset.code, asset_status: asset.status, line_id: `L${i + 1}`, name: asset.item_name, variant_label: `${asset.size} · ${asset.color}`, state: 'ALLOCATED', preexisting_issues: '', returned_at: null, conditions: [], inspection_notes: '' });
    }
  });
  const now = new Date().toISOString();
  const b: MockBooking = {
    id: `b-${code}`, code, customer_id: customerId, status: 'PENDING_CONFIRMATION', pickup_at: new Date(pickup).toISOString(), due_at: new Date(due).toISOString(), outbound: input.outbound, return: input.return,
    package_label: pkg.label, hold_expires_at: new Date(Date.now() + db.policy.booking.hold_minutes * 60_000).toISOString(), created_at: now, handed_over_at: null, returned_at: null,
    lines: priced.map((l, i) => ({ id: `L${i + 1}`, kind: l.kind, name: l.name, image: l.image, selection: l.selection, quantity: l.quantity, list_rent: vnd(l.rent), discount: vnd(0), rent: vnd(l.rent), security: vnd(l.security) })),
    items, transitions: [{ action: 'booking.create', to: 'PENDING_CONFIRMATION', at: now, actor: 'Khách hàng', note: '' }],
    charges: [
      { id: `${code}-rent`, kind: 'RENT', amount: quote.rent_total.amount, reversed: false, reason: '' },
      { id: `${code}-sec`, kind: 'SECURITY_DEPOSIT', amount: quote.security_total.amount, reversed: false, reason: '' },
      ...(quote.delivery_outbound_fee.amount + quote.delivery_return_fee.amount > 0 ? [{ id: `${code}-del`, kind: 'DELIVERY_FEE' as const, amount: quote.delivery_outbound_fee.amount + quote.delivery_return_fee.amount, reversed: false, reason: '' }] : []),
    ],
    payments: [], refunds: [], fees: [], settled: false,
  };
  db.bookings.unshift(b);
  return b;
}

function settleSession(session: MockPaymentSession): MockPaymentSession {
  if (session.status !== 'PENDING') return session;
  if (session.resolve_at !== null && Date.now() >= session.resolve_at) {
    session.status = session.outcome ?? 'FAILED';
    const b = db.bookings.find((x) => x.id === session.booking_id);
    if (b && session.status === 'SUCCEEDED') {
      b.payments.push({ id: session.id, method: 'GATEWAY', amount: session.amount.amount, bucket: session.purpose === 'RESERVATION' ? 'RESERVATION' : 'RENTAL', at: new Date().toISOString() });
      audit('Cổng thanh toán', 'payment.gateway_succeeded', 'booking', b.code);
    }
  } else if (Date.now() >= new Date(session.expires_at).getTime()) {
    session.status = 'EXPIRED';
  }
  return session;
}

function sessionView({ idempotency_key: _key, outcome: _outcome, resolve_at: _at, ...session }: MockPaymentSession): PaymentSession {
  return session;
}

function cancellationFee(b: MockBooking) {
  const rent = financeOf(b).rent_total.amount;
  const notice = (new Date(b.pickup_at).getTime() - Date.now()) / hour;
  const tiers = [...db.policy.cancellation.tiers].sort((x, y) => y.min_notice_hours - x.min_notice_hours);
  const percent = tiers.find((t) => notice >= t.min_notice_hours)?.fee_percent ?? tiers.at(-1)?.fee_percent ?? 0;
  const fee = Math.floor(rent * (percent / 100));
  const paid = financeOf(b).paid_total.amount;
  return { fee_percent: percent, fee: vnd(fee), refund: vnd(Math.max(0, paid - fee)), owed: vnd(Math.max(0, fee - paid)), basis: 'Theo thời gian còn lại đến giờ nhận' };
}

function setBookingStatus(b: MockBooking, action: string, to: MockBooking['status'], actor: string, note = '') {
  b.status = to;
  b.transitions.push({ action, to, at: new Date().toISOString(), actor, note });
  audit(actor, action, 'booking', b.code, note);
}

function requireAction(b: MockBooking, ctx: { can: (p: string) => boolean }, own: boolean, action: Parameters<typeof actionsFor>[0] extends never ? never : ReturnType<typeof actionsFor>[number]) {
  if (!actionsFor(b, ctx.can, own).includes(action)) throw new Refused(409, 'rentals.invalid_transition');
}

function assetOf(id: string) {
  const a = db.assets.find((x) => x.id === id);
  if (!a) throw new Refused(404, 'storage.not_found');
  return a;
}

function syncAssetView(b: MockBooking) {
  for (const i of b.items) i.asset_status = assetOf(i.asset_id).status;
}

const feeCharge: Record<FeeKind, ChargeKind> = {
  LATE_RETURN: 'LATE_FEE', CLEANING: 'CLEANING_FEE', DAMAGE_REPAIR: 'DAMAGE_FEE', DAMAGE_REPLACEMENT: 'LOSS_FEE', LOST_ITEM: 'LOSS_FEE', REDELIVERY: 'REDELIVERY_FEE',
};

const assetTarget: Record<AssetAction, AssetStatus> = {
  'asset.pass_inspection': 'READY', 'asset.require_cleaning': 'CLEANING', 'asset.complete_cleaning': 'READY', 'asset.send_to_repair': 'REPAIR',
  'asset.complete_repair': 'NEEDS_INSPECTION', 'asset.report_missing': 'MISSING', 'asset.record_found': 'NEEDS_INSPECTION', 'asset.retire': 'RETIRED',
};

function dashboard(days: number): Dashboard {
  const today = businessToday();
  const series = Array.from({ length: days }, (_, i) => {
    const date = addBusinessDays(today, i - days + 1);
    const wave = 900_000 + 700_000 * Math.sin(i / 2.3) + ((i * 7919) % 11) * 110_000;
    return { date, revenue: vnd(Math.max(0, Math.round(wave / 10_000) * 10_000)) };
  });
  const countBy = <K extends string>(rows: K[]) => {
    const m = new Map<K, number>();
    rows.forEach((k) => m.set(k, (m.get(k) ?? 0) + 1));
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  };
  const rented = db.assets.filter((a) => a.status === 'RENTED').length;
  return {
    range_days: days, revenue_recognized_note: 'Doanh thu ghi nhận khi đơn hoàn tất hoặc khi quyết toán hủy, không gồm tiền bảo đảm và tiền giữ lịch.',
    kpis: [
      { key: 'revenue', value: series.reduce((n, p) => n + p.revenue.amount, 0), change_percent: 12 },
      { key: 'bookings', value: db.bookings.length * 6, change_percent: 8 },
      { key: 'customers', value: 38, change_percent: 5 },
      { key: 'active_rentals', value: db.bookings.filter((b) => b.status === 'RENTED').length, change_percent: null },
      { key: 'utilization', value: Math.round((rented / db.assets.length) * 100), change_percent: null },
      { key: 'overdue', value: db.bookings.filter(isOverdue).length, change_percent: null },
    ],
    revenue_series: series,
    bookings_by_status: countBy(db.bookings.map((b) => b.status)).map(([status, count]) => ({ status, count })),
    inventory_by_status: countBy(db.assets.map((a) => a.status)).map(([status, count]) => ({ status, count })),
    top_offerings: [...db.items, ...db.bundles].sort((a, b) => b.popularity - a.popularity).slice(0, 5).map((o, i) => ({ name: o.name, bookings: 24 - i * 3, revenue: vnd((24 - i * 3) * 320000) })),
  };
}

function publicPolicy(): PublicPolicy {
  const p = db.policy;
  return {
    version: p.version, hold_minutes: p.booking.hold_minutes, reservation: p.reservation, cancellation: p.cancellation, late_fee: p.late_fee, loss_verification_hours: p.loss_verification_hours,
    reschedule_cutoff_hours: p.booking.reschedule_cutoff_hours, modification_cutoff_hours: p.modification.self_service_cutoff_hours,
  };
}

const get = (path: string, fn: Parameters<typeof route>[0]) => http.get(`${base}${path}`, route(fn) as never);
const post = (path: string, fn: Parameters<typeof route>[0]) => http.post(`${base}${path}`, route(fn) as never);
const put = (path: string, fn: Parameters<typeof route>[0]) => http.put(`${base}${path}`, route(fn) as never);
const del = (path: string, fn: Parameters<typeof route>[0]) => http.delete(`${base}${path}`, route(fn) as never);

const json = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });
const none = () => new HttpResponse(null, { status: 204 });

export const handlers: HttpHandler[] = [
  // Auth, mirroring internal/httpapi so the app can be walked through without a server.
  get('/auth/me', () => {
    const u = currentUser();
    return u ? json(sessionOf(u)) : fail(401, 'identity.unauthenticated');
  }),
  post('/auth/login', async ({ request }) => {
    const { email, password } = await request.json() as { email: string; password: string };
    const u = db.users.find((x) => x.email === email.toLowerCase());
    if (!u || password !== demoPassword || u.status === 'LOCKED') return fail(401, 'identity.invalid_credentials');
    if (u.mfa_enabled) return json({ status: 'mfa_required', challenge: `mock:${u.id}`, expires_at: new Date(Date.now() + 300_000).toISOString() });
    sessionStorage.setItem(sessionKey, u.id);
    return json(sessionOf(u));
  }),
  post('/auth/login/mfa', async ({ request }) => {
    const { challenge, code } = await request.json() as { challenge: string; code: string };
    const u = db.users.find((x) => `mock:${x.id}` === challenge);
    if (!u) return fail(401, 'identity.invalid_login_challenge');
    if (code !== mfaCode) return fail(400, 'identity.invalid_mfa_code');
    sessionStorage.setItem(sessionKey, u.id);
    return json(sessionOf(u));
  }),
  post('/auth/logout', () => {
    sessionStorage.removeItem(sessionKey);
    return none();
  }),
  post('/auth/register', () => json({ status: 'accepted' }, 202)),
  post('/auth/password-reset', () => json({ status: 'accepted' }, 202)),
  post('/auth/verify-email/resend', () => json({ status: 'accepted' }, 202)),
  post('/auth/password', () => none()),

  // Catalog and store
  get('/catalog/categories', () => json(db.categories.map((c) => ({ ...c, offering_count: offerings().filter((o) => o.card.category?.id === c.id).length })))),
  get('/catalog/packages', () => json(db.packages)),
  get('/catalog/offerings', ({ request }) => {
    const url = new URL(request.url);
    const f = (k: string) => url.searchParams.get(k) ?? '';
    const term = norm(f('q'));
    const all = offerings();
    const rows = all.filter(({ card: c }) => (!term || norm(c.name).includes(term)) && (!f('category') || c.category?.slug === f('category')) && (!f('kind') || c.kind === f('kind'))
      && (!f('size') || c.sizes.includes(f('size'))) && (!f('color') || c.colors.includes(f('color'))));
    const sort = f('sort');
    rows.sort((a, b) => (sort === 'price_asc' ? a.card.from_price.amount - b.card.from_price.amount : sort === 'price_desc' ? b.card.from_price.amount - a.card.from_price.amount : b.popularity - a.popularity));
    return json({
      ...paged(rows.map((r) => r.card), url),
      facets: { sizes: [...new Set(all.flatMap((r) => r.card.sizes))], colors: [...new Set(all.flatMap((r) => r.card.colors))] },
    });
  }),
  get('/catalog/offerings/:kind/:slug', ({ params }) => {
    const found = params.kind === 'ITEM' ? db.items.find((i) => i.slug === params.slug) : null;
    if (found) return json(itemDetail(found));
    const bundle = params.kind === 'BUNDLE' ? db.bundles.find((b) => b.slug === params.slug) : null;
    return bundle ? json(bundleDetail(bundle)) : fail(404, 'storage.not_found');
  }),
  get('/store/info', () => json(db.store)),
  get('/store/fulfillment-options', () => json({ outbound: ['STORE', 'COURIER'], return: ['STORE', 'COURIER'], zones: [{ id: 'noi-thanh', name: 'Nội thành', wards: [{ code: 'w1', name: 'Phường Bến Nghé' }, { code: 'w2', name: 'Phường Thảo Điền' }, { code: 'w3', name: 'Phường Tân Định' }] }] })),
  get('/store/calendar', ({ request }) => {
    const url = new URL(request.url);
    const from = url.searchParams.get('from') ?? businessToday();
    const to = url.searchParams.get('to') ?? from;
    const days: CalendarDay[] = [];
    for (let d = from; d <= to; d = addBusinessDays(d, 1)) days.push(calendarDay(d));
    const today = businessToday();
    return json({ timezone: 'Asia/Ho_Chi_Minh', min_date: addBusinessDays(today, 2), max_date: addBusinessDays(today, 90), days });
  }),
  get('/policies/public', () => json(publicPolicy())),
  post('/availability', async ({ request }) => json(checkAvailability(await request.json() as BookingInput))),
  post('/quotes', async ({ request }) => json(buildQuote(await request.json() as BookingInput))),

  // Bookings
  post('/bookings', async ({ request }) => {
    const ctx = requirePerm('booking.create_own');
    const b = placeBooking(await request.json() as BookingInput, ctx.user.id);
    return json(detailOf(b, ctx.can, true), 201);
  }),
  get('/me/bookings', ({ request }) => {
    const ctx = requireUser();
    const url = new URL(request.url);
    const history = url.searchParams.get('scope') === 'history';
    const rows = db.bookings.filter((b) => b.customer_id === ctx.user.id && ['COMPLETED', 'CANCELLED', 'EXPIRED'].includes(b.status) === history);
    return json(paged(rows.map(summaryOf), url));
  }),
  get('/bookings', ({ request }) => {
    requirePerm('booking.read_all');
    const url = new URL(request.url);
    const term = norm(url.searchParams.get('q') ?? '');
    const status = url.searchParams.get('status') ?? '';
    const rows = db.bookings.map(summaryOf).filter((b) => (!status || b.status === status) && (!term || norm(`${b.code} ${b.customer.name} ${b.customer.phone}`).includes(term)));
    return json(paged(rows, url));
  }),
  get('/bookings/:id', ({ params }) => {
    const { b, can, own } = bookingAccess(params.id);
    return json(detailOf(b, can, own));
  }),
  get('/bookings/:id/cancellation-preview', ({ params }) => json(cancellationFee(bookingAccess(params.id).b))),
  post('/bookings/:id/cancel', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    const { reason, note } = await request.json() as { reason: string; note: string };
    requireAction(ctx.b, ctx, ctx.own, reason === 'NO_SHOW' ? 'no_show' : 'cancel');
    const preview = cancellationFee(ctx.b);
    if (preview.fee.amount > 0) ctx.b.charges.push({ id: `${ctx.b.code}-cf`, kind: 'CANCELLATION_FEE', amount: preview.fee.amount, reversed: false, reason: preview.basis });
    ctx.b.charges.filter((c) => c.kind === 'RENT' || c.kind === 'SECURITY_DEPOSIT').forEach((c) => { c.reversed = true; });
    if (preview.refund.amount > 0) ctx.b.refunds.push({ id: `${ctx.b.code}-rc`, bucket: 'RENTAL', status: 'REQUESTED', amount: preview.refund, reason: 'Hoàn khi hủy đơn', at: new Date().toISOString(), actions: [] });
    ctx.b.items.forEach((i) => { assetOf(i.asset_id).booking_code = null; });
    setBookingStatus(ctx.b, 'booking.cancel', 'CANCELLED', ctx.user.full_name, note);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/reschedule', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'reschedule');
    const { pickup_date, slot_id } = await request.json() as { pickup_date: string; slot_id: string };
    const pkg = db.packages.find((p) => p.label === ctx.b.package_label);
    const { pickup, due } = schedule({ package_id: pkg?.id ?? '', pickup_date, slot_id, outbound: ctx.b.outbound, return: ctx.b.return, lines: [] });
    for (const i of ctx.b.items) {
      if (!freeAssets(assetOf(i.asset_id).variant_id, pickup, due, ctx.b.id).length) throw new Refused(409, 'inventory.insufficient');
    }
    ctx.b.pickup_at = new Date(pickup).toISOString();
    ctx.b.due_at = new Date(due).toISOString();
    ctx.b.transitions.push({ action: 'booking.reschedule', to: ctx.b.status, at: new Date().toISOString(), actor: ctx.user.full_name, note: '' });
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/confirm', ({ params }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'confirm');
    const f = financeOf(ctx.b);
    if (f.paid_total.amount < f.reservation_deposit.amount) throw new Refused(409, 'rentals.insufficient_funds');
    ctx.b.hold_expires_at = null;
    setBookingStatus(ctx.b, 'booking.confirm', 'CONFIRMED', ctx.user.full_name);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/start-preparation', ({ params }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'start_preparation');
    setBookingStatus(ctx.b, 'booking.start_preparation', 'PREPARING', ctx.user.full_name);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/handover', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'handover');
    const body = await request.json() as { checklist_completed: boolean; delivered: { asset_id: string; preexisting_issues: string }[] };
    if (!body.checklist_completed) throw new Refused(400, 'rentals.checklist_incomplete');
    if (financeOf(ctx.b).outstanding.amount > 0) throw new Refused(409, 'rentals.insufficient_funds');
    for (const d of body.delivered) {
      const item = ctx.b.items.find((i) => i.asset_id === d.asset_id);
      if (!item) throw new Refused(409, 'rentals.asset_mismatch');
      item.preexisting_issues = d.preexisting_issues;
      item.state = 'WITH_CUSTOMER';
      const a = assetOf(d.asset_id);
      a.status = 'RENTED';
      a.updated_at = new Date().toISOString();
    }
    ctx.b.handed_over_at = new Date().toISOString();
    syncAssetView(ctx.b);
    setBookingStatus(ctx.b, 'booking.handover', 'RENTED', ctx.user.full_name);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/returns', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'record_return');
    const { asset_ids } = await request.json() as { asset_ids: string[] };
    const now = new Date().toISOString();
    for (const id of asset_ids) {
      const item = ctx.b.items.find((i) => i.asset_id === id);
      if (!item) throw new Refused(409, 'rentals.asset_mismatch');
      item.state = 'RETURNED';
      item.returned_at = now;
      const a = assetOf(id);
      a.status = 'NEEDS_INSPECTION';
      a.updated_at = now;
    }
    syncAssetView(ctx.b);
    if (ctx.b.items.every((i) => i.state === 'RETURNED' || i.state === 'LOST')) {
      ctx.b.returned_at = now;
      setBookingStatus(ctx.b, 'booking.return', 'RETURN_INSPECTION', ctx.user.full_name);
    }
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/report-missing', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'report_missing');
    const { asset_id } = await request.json() as { asset_id: string };
    const item = ctx.b.items.find((i) => i.asset_id === asset_id);
    if (!item) throw new Refused(409, 'rentals.asset_mismatch');
    item.state = 'MISSING_PENDING';
    audit(ctx.user.full_name, 'booking.report_missing', 'booking', ctx.b.code);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/confirm-lost', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'confirm_lost');
    const { asset_id, reason } = await request.json() as { asset_id: string; reason: string };
    const item = ctx.b.items.find((i) => i.asset_id === asset_id);
    if (!item) throw new Refused(409, 'rentals.asset_mismatch');
    item.state = 'LOST';
    assetOf(asset_id).status = 'MISSING';
    syncAssetView(ctx.b);
    if (ctx.b.items.every((i) => i.state === 'RETURNED' || i.state === 'LOST')) setBookingStatus(ctx.b, 'booking.return', 'RETURN_INSPECTION', ctx.user.full_name, reason);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/inspections', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'inspect');
    const body = await request.json() as { asset_id: string; conditions: Condition[]; notes: string; evidence: string[] };
    const needsEvidence = body.conditions.some((c) => ['ABNORMAL_SOILING', 'MINOR_REPAIRABLE', 'MAJOR_REPAIRABLE', 'IRREPARABLE'].includes(c));
    if (needsEvidence && body.evidence.length === 0) throw new Refused(400, 'rentals.evidence_required');
    const item = ctx.b.items.find((i) => i.asset_id === body.asset_id);
    if (!item) throw new Refused(409, 'rentals.asset_mismatch');
    item.conditions = body.conditions;
    item.inspection_notes = body.notes;
    const a = assetOf(body.asset_id);
    a.status = conditionToAsset(body.conditions);
    a.updated_at = new Date().toISOString();
    if (a.status === 'READY') a.booking_code = null;
    syncAssetView(ctx.b);
    audit(ctx.user.full_name, 'booking.inspect_asset', 'booking', ctx.b.code, body.notes);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/fees', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'propose_fee');
    const body = await request.json() as { asset_id: string; kind: FeeKind; amount: { amount: number }; basis: string };
    const code = ctx.b.items.find((i) => i.asset_id === body.asset_id)?.asset_code ?? '';
    const fee = { id: `f-${Date.now()}`, kind: body.kind, status: 'PROPOSED' as const, amount: vnd(body.amount.amount), asset_code: code, basis: body.basis, created_at: new Date().toISOString() };
    ctx.b.fees.push(fee);
    audit(ctx.user.full_name, 'fee.propose', 'booking', ctx.b.code, body.basis);
    return json({ ...fee, actions: [] }, 201);
  }),
  post('/bookings/:id/fees/:fee/decision', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    const { decision, note } = await request.json() as { decision: 'APPROVE' | 'REJECT' | 'WAIVE'; note: string };
    const fee = ctx.b.fees.find((f) => f.id === params.fee);
    if (!fee) throw new Refused(404, 'storage.not_found');
    if (!ctx.can(decision === 'WAIVE' ? 'fee.waive' : 'fee.approve')) throw new Refused(403, 'identity.forbidden');
    if (decision === 'APPROVE') {
      fee.status = 'APPROVED';
      ctx.b.charges.push({ id: `c-${fee.id}`, kind: feeCharge[fee.kind], amount: fee.amount.amount, reversed: false, reason: fee.basis });
    } else if (decision === 'REJECT') {
      fee.status = 'REJECTED';
    } else {
      fee.status = 'WAIVED';
      const charge = ctx.b.charges.find((c) => c.id === `c-${fee.id}`);
      if (charge) charge.reversed = true;
    }
    audit(ctx.user.full_name, `fee.${decision.toLowerCase()}`, 'booking', ctx.b.code, note);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/payments', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'record_payment');
    const body = await request.json() as { idempotency_key: string; method: 'CASH' | 'BANK_TRANSFER'; portions: { bucket: 'RENTAL' | 'SECURITY' | 'RESERVATION'; amount: { amount: number } }[] };
    if (ctx.b.payments.some((p) => p.id.startsWith(body.idempotency_key))) return json(detailOf(ctx.b, ctx.can, ctx.own));
    const total = body.portions.reduce((n, p) => n + p.amount.amount, 0);
    if (total > financeOf(ctx.b).outstanding.amount) throw new Refused(409, 'payments.exceeds_outstanding');
    body.portions.forEach((p, i) => ctx.b.payments.push({ id: `${body.idempotency_key}-${i}`, method: body.method, amount: p.amount.amount, bucket: p.bucket, at: new Date().toISOString() }));
    audit(ctx.user.full_name, 'payment.record', 'booking', ctx.b.code);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/payment-sessions', async ({ params, request }) => {
    const ctx = bookingAccess(params.id);
    if (!ctx.own) throw new Refused(403, 'identity.forbidden');
    const body = await request.json() as { purpose: PaymentPurpose; channel: PaymentChannel; idempotency_key: string };
    const repeated = db.paymentSessions.find((s) => s.idempotency_key === body.idempotency_key);
    if (repeated) return json(sessionView(repeated));
    const open = db.paymentSessions.find((s) => s.booking_id === ctx.b.id && s.purpose === body.purpose && settleSession(s).status === 'PENDING');
    if (open) return json(sessionView(open));
    const option = paymentOptions(ctx.b, true).find((o) => o.purpose === body.purpose);
    if (!option) throw new Refused(409, 'payments.exceeds_outstanding');
    const id = nextCode('PS');
    const session: MockPaymentSession = {
      id, booking_id: ctx.b.id, booking_code: ctx.b.code, purpose: body.purpose, channel: body.channel, amount: option.amount, status: 'PENDING', checkout_url: `/mock-gateway/${id}`,
      expires_at: new Date(Date.now() + 15 * 60_000).toISOString(), idempotency_key: body.idempotency_key, outcome: null, resolve_at: null,
    };
    db.paymentSessions.unshift(session);
    return json(sessionView(session), 201);
  }),
  get('/payment-sessions/:id', ({ params }) => {
    const { user, can } = requireUser();
    const session = db.paymentSessions.find((s) => s.id === params.id);
    const booking = db.bookings.find((b) => b.id === session?.booking_id);
    if (!session || !booking || (booking.customer_id !== user.id && !can('booking.read_all'))) return fail(404, 'storage.not_found');
    return json(sessionView(settleSession(session)));
  }),
  // Stands in for the provider's webhook: the result arrives a moment after the
  // customer is sent back, which is how a real gateway behaves.
  post('/mock-gateway/:id/complete', async ({ params, request }) => {
    const session = db.paymentSessions.find((s) => s.id === params.id);
    if (!session) return fail(404, 'storage.not_found');
    const { outcome } = await request.json() as { outcome: 'SUCCEEDED' | 'FAILED' };
    session.outcome = outcome;
    session.resolve_at = Date.now() + 2500;
    return none();
  }),
  post('/bookings/:id/settle', ({ params }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'settle');
    const held = financeOf(ctx.b).security_held;
    ctx.b.settled = true;
    if (held.amount > 0) ctx.b.refunds.push({ id: `${ctx.b.code}-rs`, bucket: 'SECURITY', status: 'REQUESTED', amount: held, reason: 'Hoàn tiền bảo đảm sau khấu trừ', at: new Date().toISOString(), actions: [] });
    audit(ctx.user.full_name, 'booking.settle', 'booking', ctx.b.code);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/refunds/:refund/approve', ({ params }) => {
    const ctx = bookingAccess(params.id);
    if (!ctx.can('refund.approve')) throw new Refused(403, 'identity.forbidden');
    const r = ctx.b.refunds.find((x) => x.id === params.refund);
    if (!r) throw new Refused(404, 'storage.not_found');
    r.status = 'APPROVED';
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/refunds/:refund/reject', ({ params }) => {
    const ctx = bookingAccess(params.id);
    if (!ctx.can('refund.approve')) throw new Refused(403, 'identity.forbidden');
    const r = ctx.b.refunds.find((x) => x.id === params.refund);
    if (!r) throw new Refused(404, 'storage.not_found');
    r.status = 'REJECTED';
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/refunds/:refund/payout', ({ params }) => {
    const ctx = bookingAccess(params.id);
    if (!ctx.can('payment.record')) throw new Refused(403, 'identity.forbidden');
    const r = ctx.b.refunds.find((x) => x.id === params.refund);
    if (!r) throw new Refused(404, 'storage.not_found');
    r.status = 'SUCCEEDED';
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),
  post('/bookings/:id/complete', ({ params }) => {
    const ctx = bookingAccess(params.id);
    requireAction(ctx.b, ctx, ctx.own, 'complete');
    setBookingStatus(ctx.b, 'booking.complete', 'COMPLETED', ctx.user.full_name);
    return json(detailOf(ctx.b, ctx.can, ctx.own));
  }),

  // Operations
  get('/ops/overview', () => {
    const { can } = requirePerm('booking.read_all');
    const today = businessToday();
    const live = db.bookings.filter((b) => !['CANCELLED', 'EXPIRED', 'COMPLETED'].includes(b.status));
    const on = (iso: string) => businessToday(new Date(iso)) === today;
    const queue = live.filter((b) => actionsFor(b, can, false).some((a) => ['confirm', 'start_preparation', 'handover', 'inspect'].includes(a)) || isOverdue(b));
    return json({
      date: today,
      counters: {
        pending_confirmation: live.filter((b) => b.status === 'PENDING_CONFIRMATION').length, to_prepare: live.filter((b) => b.status === 'CONFIRMED').length,
        handovers_today: live.filter((b) => b.status === 'PREPARING' && on(b.pickup_at)).length, returns_today: live.filter((b) => b.status === 'RENTED' && on(b.due_at)).length,
        inspections_open: live.filter((b) => b.status === 'RETURN_INSPECTION').length, overdue: live.filter(isOverdue).length, conflicts: conflictsOf().length,
        assets_blocked: db.assets.filter((a) => !['READY', 'RENTED', 'RETIRED'].includes(a.status)).length,
      },
      queue: queue.map(summaryOf),
    });
  }),
  get('/ops/schedule', ({ request }) => {
    requirePerm('booking.read_all');
    const url = new URL(request.url);
    const from = url.searchParams.get('from') ?? '';
    const to = url.searchParams.get('to') ?? '';
    const inRange = (iso: string) => { const d = businessToday(new Date(iso)); return d >= from && d <= to; };
    const live = db.bookings.filter((b) => !['CANCELLED', 'EXPIRED', 'COMPLETED'].includes(b.status));
    return json([
      ...live.filter((b) => ['PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING'].includes(b.status) && inRange(b.pickup_at)).map((b) => ({ id: `${b.id}-out`, kind: 'HANDOVER', at: b.pickup_at, mode: b.outbound, booking: summaryOf(b) })),
      ...live.filter((b) => b.status === 'RENTED' && inRange(b.due_at)).map((b) => ({ id: `${b.id}-in`, kind: 'RETURN', at: b.due_at, mode: b.return, booking: summaryOf(b) })),
    ]);
  }),
  get('/ops/alerts', () => {
    requirePerm('booking.read_all', 'inventory.read');
    const overdue = db.bookings.filter(isOverdue);
    return json({
      overdue: overdue.map((b) => ({ booking: summaryOf(b), overdue_hours: Math.ceil((Date.now() - new Date(b.due_at).getTime()) / hour), items_out: b.items.filter((i) => i.state === 'WITH_CUSTOMER').length })),
      conflicts: conflictsOf(),
    });
  }),

  // Inventory
  get('/inventory/assets', ({ request }) => {
    const { can } = requirePerm('inventory.read');
    const url = new URL(request.url);
    const term = norm(url.searchParams.get('q') ?? '');
    const status = url.searchParams.get('status') ?? '';
    const rows = db.assets.filter((a) => (!status || a.status === status) && (!term || norm(`${a.code} ${a.sku} ${a.item_name}`).includes(term))).map(({ history: _history, ...a }) => ({ ...a, actions: assetActions(a, can) }));
    return json(paged(rows, url));
  }),
  get('/inventory/assets/:id/history', ({ params }) => {
    requirePerm('inventory.read');
    return json([...(db.assets.find((a) => a.id === params.id)?.history ?? [])].reverse());
  }),
  post('/inventory/assets/:id/status', async ({ params, request }) => {
    const { can, user } = requirePerm('inventory.read');
    const a = assetOf(params.id as string);
    const body = await request.json() as { action: AssetAction; reason: string; checklist_completed: boolean };
    if (!assetActions(a, can).includes(body.action)) throw new Refused(409, 'inventory.invalid_asset_transition');
    const to = assetTarget[body.action];
    a.history.push({ action: body.action, from: a.status, to, at: new Date().toISOString(), actor: user.full_name, reason: body.reason });
    a.status = to;
    a.note = body.reason;
    a.updated_at = new Date().toISOString();
    audit(user.full_name, body.action, 'asset', a.code, body.reason);
    return json({ ...a, actions: assetActions(a, can) });
  }),
  post('/inventory/assets', async ({ request }) => {
    const { user } = requirePerm('inventory.manage');
    const { code, variant_id } = await request.json() as { code: string; variant_id: string };
    const owner = db.items.find((i) => i.variants.some((v) => v.id === variant_id || v.sku === variant_id));
    const variant = owner?.variants.find((v) => v.id === variant_id || v.sku === variant_id);
    if (!owner || !variant) return fail(400, 'catalog.variant_unavailable');
    if (db.assets.some((a) => a.code === code)) return fail(409, 'storage.duplicate');
    const now = new Date().toISOString();
    const asset = { id: `a-${code}`, code, variant_id: variant.id, sku: variant.sku, item_name: owner.name, size: variant.size, color: variant.color, status: 'NEEDS_INSPECTION' as const, note: '', booking_code: null, updated_at: now, actions: [], history: [{ action: 'asset.intake', from: null, to: 'NEEDS_INSPECTION' as const, at: now, actor: user.full_name, reason: '' }] };
    db.assets.unshift(asset);
    audit(user.full_name, 'asset.intake', 'asset', code);
    return json(asset, 201);
  }),

  // Account and support
  get('/me/profile', () => {
    const { user } = requireUser();
    return json({ email: user.email, full_name: user.full_name, phone: user.phone });
  }),
  put('/me/profile', async ({ request }) => {
    const { user } = requireUser();
    const body = await request.json() as { full_name: string; phone: string };
    user.full_name = body.full_name;
    user.phone = body.phone;
    return json({ email: user.email, ...body });
  }),
  get('/me/support-requests', () => {
    const { user } = requireUser();
    return json(db.support.filter((s) => s.customer_name === user.full_name));
  }),
  post('/me/support-requests', async ({ request }) => {
    const { user } = requireUser();
    const body = await request.json() as { topic: 'OTHER'; subject: string; booking_code: string; message: string };
    const now = new Date().toISOString();
    const created = {
      id: `s${Date.now()}`, code: nextCode('HT'), topic: body.topic, subject: body.subject, status: 'OPEN' as const, booking_code: body.booking_code || null, customer_name: user.full_name, created_at: now,
      messages: [{ from: 'CUSTOMER' as const, author: user.full_name, body: body.message, at: now }],
    };
    db.support.unshift(created);
    return json(created, 201);
  }),
  get('/support-requests', () => {
    requirePerm('booking.read_all');
    return json(db.support);
  }),
  post('/support-requests/:id/replies', async ({ params, request }) => {
    const { user } = requirePerm('booking.read_all');
    const body = await request.json() as { message: string; resolve: boolean };
    const s = db.support.find((x) => x.id === params.id);
    if (!s) return fail(404, 'storage.not_found');
    s.messages.push({ from: 'STAFF', author: user.full_name, body: body.message, at: new Date().toISOString() });
    s.status = body.resolve ? 'RESOLVED' : 'IN_PROGRESS';
    return json(s);
  }),

  // Admin
  get('/admin/dashboard', ({ request }) => {
    requirePerm('revenue.read', 'policy.manage');
    return json(dashboard(Number(new URL(request.url).searchParams.get('range_days')) || 30));
  }),
  get('/admin/users', ({ request }) => {
    requirePerm('user.role_manage');
    const url = new URL(request.url);
    const term = norm(url.searchParams.get('q') ?? '');
    const role = url.searchParams.get('role') ?? '';
    const rows = db.users.filter((u) => (!role || u.roles.includes(role as Role)) && (!term || norm(`${u.full_name} ${u.email}`).includes(term))).map(({ password: _p, ...u }) => u);
    return json(paged(rows, url));
  }),
  get('/admin/users/:id', ({ params }) => {
    requirePerm('user.role_manage');
    const u = db.users.find((x) => x.id === params.id);
    if (!u) return fail(404, 'storage.not_found');
    return json({ id: u.id, email: u.email, status: u.status, roles: u.roles, created_at: u.created_at });
  }),
  put('/admin/users/:id/roles/:role', async ({ params, request }) => {
    const { user } = requirePerm('user.role_manage');
    const { reason } = await request.json() as { reason: string };
    if (params.id === user.id) return fail(403, 'identity.self_role_change');
    const u = db.users.find((x) => x.id === params.id);
    if (!u) return fail(404, 'storage.not_found');
    u.roles = [...new Set([...u.roles, params.role as Role])];
    audit(user.full_name, 'user.role_grant', 'user', u.id, reason);
    return none();
  }),
  del('/admin/users/:id/roles/:role', async ({ params, request }) => {
    const { user } = requirePerm('user.role_manage');
    const { reason } = await request.json() as { reason: string };
    if (params.id === user.id) return fail(403, 'identity.self_role_change');
    const u = db.users.find((x) => x.id === params.id);
    if (!u) return fail(404, 'storage.not_found');
    u.roles = u.roles.filter((r) => r !== params.role);
    audit(user.full_name, 'user.role_revoke', 'user', u.id, reason);
    return none();
  }),
  post('/admin/users/:id/lock', async ({ params, request }) => {
    const { user } = requirePerm('user.lock');
    const u = db.users.find((x) => x.id === params.id);
    if (!u) return fail(404, 'storage.not_found');
    u.status = 'LOCKED';
    audit(user.full_name, 'user.lock', 'user', u.id, (await request.json() as { reason: string }).reason);
    return none();
  }),
  post('/admin/users/:id/unlock', async ({ params, request }) => {
    const { user } = requirePerm('user.lock');
    const u = db.users.find((x) => x.id === params.id);
    if (!u) return fail(404, 'storage.not_found');
    u.status = 'ACTIVE';
    audit(user.full_name, 'user.unlock', 'user', u.id, (await request.json() as { reason: string }).reason);
    return none();
  }),
  post('/admin/users/:id/mfa-reset', () => {
    requirePerm('user.lock');
    return none();
  }),
  get('/admin/permission-matrix', () => {
    requirePerm('policy.manage');
    return json(db.matrix);
  }),
  post('/admin/permission-matrix', async ({ request }) => {
    const { user } = requirePerm('policy.manage');
    const body = await request.json() as typeof db.matrix & { reason: string };
    db.matrix = { version: body.version, matrix: body.matrix };
    audit(user.full_name, 'permission_matrix.publish', 'permission_matrix', body.version, body.reason);
    return json(db.matrix, 201);
  }),
  get('/admin/audit-events', ({ request }) => {
    requirePerm('audit.read');
    const url = new URL(request.url);
    const term = norm(url.searchParams.get('q') ?? '');
    const result = url.searchParams.get('result') ?? '';
    return json(paged(db.audit.filter((e) => (!result || e.result === result) && (!term || norm(`${e.actor} ${e.action} ${e.resource_id}`).includes(term))), url));
  }),
  get('/admin/catalog/items', () => { requirePerm('catalog.manage'); return json(db.items); }),
  post('/admin/catalog/items', async ({ request }) => {
    requirePerm('catalog.manage');
    const body = await request.json() as (typeof db.items)[number];
    const created = { ...body, id: body.slug, version: 1, asset_count: 0, popularity: 0, variants: body.variants.map((v) => ({ ...v, id: v.id || v.sku })) };
    db.items.unshift(created);
    return json(created, 201);
  }),
  put('/admin/catalog/items/:id', async ({ params, request }) => {
    requirePerm('catalog.manage');
    const i = db.items.findIndex((x) => x.id === params.id);
    const current = db.items[i];
    if (!current) return fail(404, 'storage.not_found');
    const body = await request.json() as (typeof db.items)[number];
    db.items[i] = { ...current, ...body, id: current.id, version: current.version + 1, variants: body.variants.map((v) => ({ ...v, id: v.id || v.sku })) };
    return json(db.items[i]);
  }),
  get('/admin/catalog/bundles', () => { requirePerm('catalog.manage'); return json(db.bundles); }),
  post('/admin/catalog/bundles', async ({ request }) => {
    requirePerm('catalog.manage');
    const body = await request.json() as (typeof db.bundles)[number];
    const created = { ...body, id: body.slug, version: 1, popularity: 0 };
    db.bundles.unshift(created);
    return json(created, 201);
  }),
  put('/admin/catalog/bundles/:id', async ({ params, request }) => {
    requirePerm('catalog.manage');
    const i = db.bundles.findIndex((x) => x.id === params.id);
    const current = db.bundles[i];
    if (!current) return fail(404, 'storage.not_found');
    db.bundles[i] = { ...current, ...(await request.json() as (typeof db.bundles)[number]), id: current.id, version: current.version + 1 };
    return json(db.bundles[i]);
  }),
  post('/admin/catalog/categories', async ({ request }) => {
    requirePerm('catalog.manage');
    const body = await request.json() as { name: string; slug: string };
    if (db.categories.some((c) => c.slug === body.slug)) return fail(409, 'storage.duplicate');
    const created = { id: `c-${body.slug}`, image: null, ...body };
    db.categories.push(created);
    return json({ ...created, offering_count: 0 }, 201);
  }),
  put('/admin/catalog/categories/:id', async ({ params, request }) => {
    requirePerm('catalog.manage');
    const c = db.categories.find((x) => x.id === params.id);
    if (!c) return fail(404, 'storage.not_found');
    Object.assign(c, await request.json());
    return json({ ...c, offering_count: 0 });
  }),
  post('/admin/catalog/packages', async ({ request }) => {
    requirePerm('catalog.manage');
    const body = await request.json() as (typeof db.packages)[number];
    const created = { ...body, id: `pkg-${body.code.toLowerCase()}` };
    db.packages.push(created);
    return json(created, 201);
  }),
  put('/admin/catalog/packages/:id', async ({ params, request }) => {
    requirePerm('catalog.manage');
    const p = db.packages.find((x) => x.id === params.id);
    if (!p) return fail(404, 'storage.not_found');
    Object.assign(p, await request.json());
    return json(p);
  }),
  get('/admin/policies/active', () => { requirePerm('policy.manage'); return json(db.policy); }),
  post('/admin/policies', async ({ request }) => {
    const { user } = requirePerm('policy.manage');
    const body = await request.json() as { policy: typeof db.policy; reason: string };
    db.policy = { ...body.policy, published_at: new Date().toISOString(), published_by: user.full_name };
    audit(user.full_name, 'policy.publish', 'policy', db.policy.version, body.reason);
    return json(db.policy, 201);
  }),
  get('/admin/promotions', () => { requirePerm('promotion.manage'); return json(db.campaigns); }),
  post('/admin/promotions', async ({ request }) => {
    requirePerm('promotion.manage');
    const created = { ...(await request.json() as (typeof db.campaigns)[number]), id: `p${Date.now()}`, used: 0 };
    db.campaigns.unshift(created);
    return json(created, 201);
  }),
  put('/admin/promotions/:id', async ({ params, request }) => {
    requirePerm('promotion.manage');
    const c = db.campaigns.find((x) => x.id === params.id);
    if (!c) return fail(404, 'storage.not_found');
    Object.assign(c, await request.json());
    return json(c);
  }),
];


