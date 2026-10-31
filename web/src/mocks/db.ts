import type {
  AdminBundle, AdminItem, AdminUser, Asset, AssetAction, AssetHistoryEntry, AssetStatus, AuditEvent, BookingAction, BookingDetail, BookingItem,
  BookingLine, BookingStatus, BookingSummary, BookingTransition, Campaign, Category, Condition, FeeAssessment, FinanceRefund, Image, LegMode,
  Money, OfferingCard, OfferingDetail, PaymentMethod, PaymentOption, PaymentSession, PaymentState, PermissionMatrix, PolicyDocument, RentalPackage, Role, StoreInfo, SupportRequest, Bucket, Variant,
} from '../lib/api/types';
import { businessToday } from '../lib/format';
import { art } from './art';

// Fake data for local development. Every name, price and rule here is
// invented for the demo and is not a business policy.

export const vnd = (amount: number): Money => ({ amount, currency: 'VND' });
const hour = 3_600_000;
const iso = (ms: number) => new Date(ms).toISOString();

export interface MockUser extends AdminUser {
  password: string;
}

export const demoPassword = 'demo-webee-2026';

const matrix = {
  customer: ['booking.create_own', 'booking.read_own', 'booking.cancel_own', 'booking.reschedule_own', 'booking.modify_own', 'payment.read_own', 'delivery.read_own', 'complaint.create_own'],
  rental_staff: ['booking.read_all', 'booking.confirm', 'booking.cancel_any', 'booking.reschedule_any', 'booking.modify_any', 'booking.complete', 'rental.prepare', 'rental.handover', 'rental.return_receive', 'rental.return_inspect', 'inventory.read', 'payment.record', 'charge.create', 'refund.create', 'deduction.create', 'fee.assess', 'delivery.read_all', 'delivery.manage'],
  inventory_manager: ['catalog.manage', 'inventory.read', 'inventory.manage', 'rental.return_inspect', 'fee.assess'],
  finance: ['booking.read_all', 'payment.read', 'payment.record', 'charge.reverse', 'refund.create', 'refund.approve', 'deduction.approve', 'fee.approve', 'fee.waive', 'revenue.read', 'audit.read'],
  admin: [] as string[],
} satisfies Record<Role, string[]>;
matrix.admin = [...new Set([...Object.values(matrix).flat(), 'rental.confirm_lost', 'fee.approve_high', 'promotion.manage', 'pricing.manual_adjust', 'tax.manage', 'user.role_manage', 'user.lock', 'policy.manage'])];

function user(id: string, email: string, full_name: string, phone: string, roles: Role[]): MockUser {
  return {
    id, email, full_name, phone, roles, status: 'ACTIVE', password: demoPassword, created_at: iso(Date.now() - 90 * 24 * hour),
    mfa_enabled: roles.includes('admin') || roles.includes('finance'), last_seen_at: iso(Date.now() - 2 * hour),
  };
}

export interface MockCatalogItem extends AdminItem {
  popularity: number;
}

export interface MockBundle extends AdminBundle {
  popularity: number;
}

interface MockAsset extends Asset {
  history: AssetHistoryEntry[];
}

export interface MockCharge { id: string; kind: BookingDetail['finance']['charges'][number]['kind']; amount: number; reversed: boolean; reason: string }
export interface MockPayment { id: string; method: PaymentMethod; amount: number; bucket: Bucket; at: string }

export interface MockBooking {
  id: string;
  code: string;
  customer_id: string;
  status: BookingStatus;
  pickup_at: string;
  due_at: string;
  outbound: LegMode;
  return: LegMode;
  package_label: string;
  hold_expires_at: string | null;
  created_at: string;
  handed_over_at: string | null;
  returned_at: string | null;
  lines: BookingLine[];
  items: BookingItem[];
  transitions: BookingTransition[];
  charges: MockCharge[];
  payments: MockPayment[];
  refunds: FinanceRefund[];
  fees: Omit<FeeAssessment, 'actions'>[];
  settled: boolean;
}

export interface MockPaymentSession extends PaymentSession {
  idempotency_key: string;
  /** What the simulated gateway will report, and when. */
  outcome: 'SUCCEEDED' | 'FAILED' | null;
  resolve_at: number | null;
}

export interface MockDb {
  users: MockUser[];
  categories: Omit<Category, 'offering_count'>[];
  packages: RentalPackage[];
  items: MockCatalogItem[];
  bundles: MockBundle[];
  assets: MockAsset[];
  bookings: MockBooking[];
  paymentSessions: MockPaymentSession[];
  support: SupportRequest[];
  audit: AuditEvent[];
  policy: PolicyDocument;
  campaigns: Campaign[];
  matrix: PermissionMatrix;
  store: StoreInfo;
  nextSeq: number;
}

const pkgs: RentalPackage[] = [
  { id: 'pkg-1d', code: '1D', label: '1 ngày', duration_hours: 24, status: 'ACTIVE' },
  { id: 'pkg-3d', code: '3D', label: '3 ngày', duration_hours: 72, status: 'ACTIVE' },
  { id: 'pkg-7d', code: '7D', label: '7 ngày', duration_hours: 168, status: 'ACTIVE' },
];

function variants(prefix: string, sizes: string[], color: string): Variant[] {
  return sizes.map((size) => ({ id: `${prefix}-${size}`, sku: `${prefix}-${size}`.toUpperCase(), size, color, status: 'ACTIVE' }));
}

function prices(one: number, three: number, seven: number) {
  return [
    { package_id: 'pkg-1d', amount: vnd(one) },
    { package_id: 'pkg-3d', amount: vnd(three) },
    { package_id: 'pkg-7d', amount: vnd(seven) },
  ];
}

function item(id: string, name: string, category: string, description: string, images: Image[], sizes: string[], color: string, price: [number, number, number], deposit: number, popularity: number, standalone = true): MockCatalogItem {
  return {
    id, slug: id, name, category_id: category, description, images, status: 'ACTIVE', individually_rentable: standalone,
    variants: variants(id, sizes, color), prices: standalone ? prices(...price) : [], security_deposit: vnd(deposit), daily_rate: vnd(price[0]),
    buffer_class: 'default', version: 1, asset_count: sizes.length * 2, popularity,
  };
}

function bundle(id: string, name: string, category: string, description: string, images: Image[], components: { key: string; item: MockCatalogItem; quantity: number }[], price: [number, number, number], popularity: number): MockBundle {
  return {
    id, slug: id, name, category_id: category, description, images, status: 'ACTIVE', version: 1, deposit_override: null, popularity,
    prices: prices(...price),
    components: components.map((c) => ({ key: c.key, item_id: c.item.id, item_name: c.item.name, quantity: c.quantity, options: c.item.variants.map((v) => v.id) })),
  };
}

function seed(): MockDb {
  const now = Date.now();
  const users = [
    user('u-customer', 'khach@webee.test', 'Nguyễn Minh An', '0901 000 111', ['customer']),
    user('u-customer2', 'linh@webee.test', 'Trần Khánh Linh', '0902 000 222', ['customer']),
    user('u-customer3', 'duy@webee.test', 'Lê Quốc Duy', '0903 000 333', ['customer']),
    user('u-staff', 'nhanvien@webee.test', 'Phạm Hải Nam', '0904 000 444', ['rental_staff']),
    user('u-inventory', 'kho@webee.test', 'Đỗ Thu Hà', '0905 000 555', ['inventory_manager']),
    user('u-finance', 'taichinh@webee.test', 'Vũ Bảo Châu', '0906 000 666', ['finance']),
    user('u-admin', 'admin@webee.test', 'Hoàng Gia Bảo', '0907 000 777', ['admin']),
  ];

  const catImage = (name: string, shape: Parameters<typeof art>[1], m: string, a: string, bg: string) => art(name, shape, m, a, bg)[0] ?? null;
  const categories = [
    { id: 'c-anime', slug: 'cosplay-anime', name: 'Cosplay anime', image: catImage('Cosplay anime', 'suit', '#e8552d', '#1d1d1b', '#2d3a6b') },
    { id: 'c-tradition', slug: 'co-trang', name: 'Cổ trang', image: catImage('Cổ trang', 'robe', '#a42c2c', '#ffc400', '#3a2418') },
    { id: 'c-aodai', slug: 'ao-dai', name: 'Áo dài', image: catImage('Áo dài', 'aodai', '#f6f1e5', '#c9a33b', '#7a1f2b') },
    { id: 'c-halloween', slug: 'halloween', name: 'Halloween', image: catImage('Halloween', 'hat', '#2b2238', '#ff8a1f', '#5a3a86') },
    { id: 'c-stage', slug: 'san-khau', name: 'Sân khấu', image: catImage('Sân khấu', 'gown', '#2f6fb0', '#f4f1ea', '#101f3c') },
    { id: 'c-accessory', slug: 'phu-kien', name: 'Phụ kiện', image: catImage('Phụ kiện', 'mask', '#ffc400', '#1d1d1b', '#2c2c29') },
  ];

  const ninjaTop = item('ao-ninja-cam', 'Áo khoác ninja cam', 'c-anime', 'Áo khoác cổ đứng, vải kaki dày, viền đen ở vai và tay áo.', art('Áo khoác ninja cam', 'suit', '#ef7d20', '#1d1d1b', '#23345e'), ['S', 'M', 'L', 'XL'], 'Cam', [120000, 280000, 520000], 300000, 90);
  const ninjaPants = item('quan-ninja-cam', 'Quần ninja cam', 'c-anime', 'Quần ống đứng, bo gấu, lưng chun thoải mái.', art('Quần ninja cam', 'suit', '#e36a12', '#23345e', '#1a2747'), ['S', 'M', 'L', 'XL'], 'Cam', [60000, 140000, 260000], 150000, 40);
  const headband = item('bang-tran-ninja', 'Băng trán ninja', 'c-accessory', 'Băng trán tấm kim loại khắc họa tiết.', art('Băng trán ninja', 'mask', '#23345e', '#c9ced8', '#ef7d20'), ['Freesize'], 'Xanh đen', [20000, 50000, 90000], 50000, 30);
  const kimono = item('kimono-hoa-anh-dao', 'Kimono hoa anh đào', 'c-tradition', 'Kimono tay rộng, thắt lưng obi rời, chất lụa nhẹ.', art('Kimono hoa anh đào', 'robe', '#d94f7a', '#fff1d0', '#3c1730'), ['S', 'M', 'L'], 'Hồng', [180000, 420000, 780000], 500000, 80);
  const hanfu = item('co-phuc-thanh-y', 'Cổ phục thanh y', 'c-tradition', 'Bộ cổ phục dáng dài, tay rộng, thêu viền bạc.', art('Cổ phục thanh y', 'robe', '#2f8f83', '#f4f1ea', '#10302c'), ['M', 'L'], 'Xanh ngọc', [200000, 460000, 860000], 600000, 70);
  const aodaiRed = item('ao-dai-do-tam-than', 'Áo dài đỏ tầm xuân', 'c-aodai', 'Áo dài lụa đỏ, cổ cao, quần lụa trắng đi kèm.', art('Áo dài đỏ tầm xuân', 'aodai', '#b5202c', '#f6f1e5', '#2e0f13'), ['S', 'M', 'L', 'XL'], 'Đỏ', [150000, 350000, 650000], 400000, 95);
  const aodaiWhite = item('ao-dai-trang-tinh-khoi', 'Áo dài trắng tinh khôi', 'c-aodai', 'Áo dài trắng truyền thống, phù hợp chụp ảnh kỷ yếu.', art('Áo dài trắng tinh khôi', 'aodai', '#f7f4ec', '#c9a33b', '#39506b'), ['S', 'M', 'L'], 'Trắng', [130000, 300000, 560000], 350000, 85);
  const witch = item('vay-phu-thuy-dem', 'Váy phù thủy đêm', 'c-halloween', 'Váy đen tím xòe rộng, kèm thắt lưng satin.', art('Váy phù thủy đêm', 'gown', '#3b2a63', '#ff8a1f', '#17111f'), ['S', 'M', 'L'], 'Tím đen', [140000, 320000, 600000], 350000, 75);
  const witchHat = item('non-phu-thuy', 'Nón phù thủy', 'c-halloween', 'Nón chóp cao, vành rộng, viền cam.', art('Nón phù thủy', 'hat', '#26203a', '#ff8a1f', '#4a2f75'), ['Freesize'], 'Đen', [40000, 90000, 170000], 80000, 45);
  const vampire = item('ao-choang-ma-ca-rong', 'Áo choàng ma cà rồng', 'c-halloween', 'Áo choàng cổ cao, lót đỏ, phù hợp sân khấu và tiệc.', art('Áo choàng ma cà rồng', 'cape', '#16151a', '#a11d2e', '#3b0d16'), ['M', 'L', 'XL'], 'Đen đỏ', [160000, 380000, 700000], 400000, 88);
  const pirate = item('ao-khoac-thuyen-truong', 'Áo khoác thuyền trưởng', 'c-stage', 'Áo khoác dài nút đồng, phong cách cướp biển.', art('Áo khoác thuyền trưởng', 'coat', '#5a2a17', '#e0b23d', '#143341'), ['M', 'L', 'XL'], 'Nâu', [170000, 400000, 740000], 450000, 60);
  const princess = item('vay-cong-chua-xanh', 'Váy công chúa xanh', 'c-stage', 'Váy dạ hội xòe nhiều lớp voan, phù hợp sân khấu.', art('Váy công chúa xanh', 'gown', '#3f8fd0', '#f4f1ea', '#142a4a'), ['S', 'M', 'L'], 'Xanh', [220000, 500000, 920000], 650000, 92);

  const items = [ninjaTop, ninjaPants, headband, kimono, hanfu, aodaiRed, aodaiWhite, witch, witchHat, vampire, pirate, princess];

  const bundles = [
    bundle('bo-ninja-lang-la', 'Bộ ninja làng lá', 'c-anime', 'Trọn bộ áo khoác, quần và băng trán. Chọn size riêng cho áo và quần.', art('Bộ ninja làng lá', 'suit', '#ef7d20', '#23345e', '#101a33'),
      [{ key: 'top', item: ninjaTop, quantity: 1 }, { key: 'pants', item: ninjaPants, quantity: 1 }, { key: 'band', item: headband, quantity: 1 }], [170000, 400000, 750000], 99),
    bundle('bo-phu-thuy', 'Bộ phù thủy đêm Halloween', 'c-halloween', 'Váy phù thủy kèm nón chóp cao.', art('Bộ phù thủy đêm Halloween', 'gown', '#3b2a63', '#ff8a1f', '#0f0b18'),
      [{ key: 'dress', item: witch, quantity: 1 }, { key: 'hat', item: witchHat, quantity: 1 }], [160000, 370000, 690000], 86),
  ];

  const assets: MockAsset[] = [];
  let n = 1;
  for (const it of items) {
    for (const v of it.variants) {
      for (let copy = 0; copy < 2; copy++) {
        const code = `CST-${String(n++).padStart(5, '0')}`;
        assets.push({
          id: `a-${code}`, code, variant_id: v.id, sku: v.sku, item_name: it.name, size: v.size, color: v.color, status: 'READY', note: '', booking_code: null,
          updated_at: iso(now - 24 * hour), actions: [], history: [{ action: 'asset.intake', from: null, to: 'NEEDS_INSPECTION', at: iso(now - 40 * 24 * hour), actor: 'Đỗ Thu Hà', reason: '' }, { action: 'asset.pass_inspection', from: 'NEEDS_INSPECTION', to: 'READY', at: iso(now - 39 * 24 * hour), actor: 'Đỗ Thu Hà', reason: '' }],
        });
      }
    }
  }
  const setAsset = (index: number, status: AssetStatus, note = '') => {
    const a = assets[index];
    if (a) { a.status = status; a.note = note; }
  };
  setAsset(5, 'CLEANING');
  setAsset(9, 'NEEDS_INSPECTION');
  setAsset(14, 'REPAIR', 'Rách đường may tay trái');
  setAsset(19, 'MISSING', 'Không tìm thấy khi kiểm kê');
  setAsset(23, 'RETIRED', 'Vải bạc màu');

  const db: MockDb = {
    users, categories, packages: pkgs, items, bundles, assets, bookings: [], paymentSessions: [], support: [], audit: [], campaigns: [], nextSeq: 100,
    matrix: { version: 'reference-2026-10-30', matrix },
    store: {
      name: 'Webee Rental', address: 'Địa chỉ mô phỏng, TP.HCM', phone: '0900 000 000', email: 'hotro@webee.test', hours: 'Thứ Ba đến Chủ nhật, 09:00 đến 18:00',
      online_payment_available: true, payment_methods: ['CASH', 'BANK_TRANSFER', 'GATEWAY'], payment_channels: ['ATM_CARD', 'INTL_CARD', 'E_WALLET', 'BANK_QR'],
      payment_instructions: 'Vui lòng thanh toán tại cửa hàng hoặc chuyển khoản theo hướng dẫn của nhân viên trước khi hết thời gian giữ hàng.',
    },
    policy: {
      version: 'mock-2026-10-31', published_at: iso(now - 5 * 24 * hour), published_by: 'Hoàng Gia Bảo',
      booking: { hold_minutes: 15, min_lead_hours: 48, max_advance_hours: 2160, max_lines_per_booking: 5, max_quantity_per_line: 5, reschedule_cutoff_hours: 72 },
      confirmation: { mode: 'STAFF', funds: 'RESERVATION_DEPOSIT', handover_funds: 'FULL_RENT_AND_SECURITY' },
      reservation: { kind: 'RENT_PERCENT', amount: null, rent_percent: 30, treatment: 'APPLIED_TO_RENT' },
      buffers: { default_preparation_hours: 24, default_turnaround_hours: 24, by_class: [] },
      cancellation: { tiers: [{ min_notice_hours: 168, fee_percent: 0 }, { min_notice_hours: 72, fee_percent: 10 }, { min_notice_hours: 24, fee_percent: 30 }, { min_notice_hours: 0, fee_percent: 50 }], no_show_after_hours: 2, no_show_fee_percent: 70 },
      late_fee: { grace_minutes: 30, tiers: [{ up_to_hours: 4, percent: 25 }, { up_to_hours: 8, percent: 50 }, { up_to_hours: 12, percent: 75 }, { up_to_hours: 24, percent: 100 }], extra_period_hours: 24, extra_percent: 100 },
      loss_verification_hours: 48, fee_approval_above: vnd(500000),
      modification: { self_service_cutoff_hours: 24, self_service_size_changes: 1, hold_minutes: 15 },
    },
  };

  db.campaigns = [
    { id: 'p1', code: 'WEBEE10', name: 'Giảm 10% cho khách mới', type: 'COUPON', status: 'ACTIVE', value_type: 'PERCENT', percent: 10, amount: null, valid_from: iso(now - 10 * 24 * hour), valid_until: iso(now + 50 * 24 * hour), combinable: false, used: 18, usage_limit: 200 },
    { id: 'p2', code: '', name: 'Ưu đãi thành viên thân thiết', type: 'MEMBER_DISCOUNT', status: 'PAUSED', value_type: 'FIXED_AMOUNT', percent: null, amount: vnd(50000), valid_from: iso(now - 30 * 24 * hour), valid_until: iso(now + 30 * 24 * hour), combinable: true, used: 6, usage_limit: null },
  ];

  const place = (over: Partial<MockBooking> & Pick<MockBooking, 'status' | 'customer_id'>, it: MockCatalogItem, sizeIdx: number, packageId: string, pickup: number): MockBooking => {
    const pkg = pkgs.find((p) => p.id === packageId) as RentalPackage;
    const code = `BK-${String(1000 + db.nextSeq++)}`;
    const rent = it.prices.find((p) => p.package_id === packageId)?.amount.amount ?? 0;
    const variant = it.variants[sizeIdx] as Variant;
    const asset = assets.find((a) => a.variant_id === variant.id && a.status === 'READY' && !a.booking_code) as MockAsset;
    const booked = over.status !== 'CANCELLED' && over.status !== 'EXPIRED' && over.status !== 'COMPLETED';
    if (booked) asset.booking_code = code;
    if (over.status === 'RENTED') asset.status = 'RENTED';
    if (over.status === 'RETURN_INSPECTION') asset.status = 'NEEDS_INSPECTION';
    const deposit = it.security_deposit.amount;
    const reservation = Math.ceil(rent * 0.3);
    const createdAt = Math.min(pickup - 72 * hour, now - 3 * hour);
    const b: MockBooking = {
      id: `b-${code}`, code, outbound: 'STORE', return: 'STORE', package_label: pkg.label, hold_expires_at: null, created_at: iso(createdAt), handed_over_at: null, returned_at: null,
      pickup_at: iso(pickup), due_at: iso(pickup + pkg.duration_hours * hour),
      lines: [{ id: 'L1', kind: 'ITEM', name: it.name, image: it.images[0] ?? null, selection: `Size ${variant.size}`, quantity: 1, list_rent: vnd(rent), discount: vnd(0), rent: vnd(rent), security: vnd(deposit) }],
      items: [{ asset_id: asset.id, asset_code: asset.code, asset_status: asset.status, line_id: 'L1', name: it.name, variant_label: `${variant.size} · ${variant.color}`, state: 'ALLOCATED', preexisting_issues: '', returned_at: null, conditions: [], inspection_notes: '' }],
      transitions: [{ action: 'booking.create', to: 'PENDING_CONFIRMATION', at: iso(createdAt), actor: 'Khách hàng', note: '' }],
      charges: [
        { id: `${code}-rent`, kind: 'RENT', amount: rent, reversed: false, reason: '' },
        { id: `${code}-sec`, kind: 'SECURITY_DEPOSIT', amount: deposit, reversed: false, reason: '' },
      ],
      payments: [], refunds: [], fees: [], settled: false, ...over,
    };
    const paidAt = iso(Math.min(pickup - 60 * hour, now - 2 * hour));
    if (over.status !== 'PENDING_CONFIRMATION' && over.status !== 'EXPIRED') b.payments.push({ id: `${code}-p1`, method: 'BANK_TRANSFER', amount: reservation, bucket: 'RESERVATION', at: paidAt });
    if (['RENTED', 'RETURN_INSPECTION', 'COMPLETED'].includes(over.status)) {
      b.payments.push({ id: `${code}-p2`, method: 'CASH', amount: rent - reservation + deposit, bucket: 'RENTAL', at: iso(pickup) });
      b.handed_over_at = iso(pickup);
      const first = b.items[0] as BookingItem;
      first.state = over.status === 'RENTED' ? 'WITH_CUSTOMER' : 'RETURNED';
      first.asset_status = asset.status;
      b.transitions.push({ action: 'booking.confirm', to: 'CONFIRMED', at: paidAt, actor: 'Phạm Hải Nam', note: '' }, { action: 'booking.handover', to: 'RENTED', at: iso(pickup), actor: 'Phạm Hải Nam', note: '' });
    } else if (over.status === 'CONFIRMED' || over.status === 'PREPARING') {
      b.transitions.push({ action: 'booking.confirm', to: 'CONFIRMED', at: paidAt, actor: 'Phạm Hải Nam', note: '' });
    }
    if (over.status === 'PREPARING') b.transitions.push({ action: 'booking.start_preparation', to: 'PREPARING', at: iso(now - hour), actor: 'Phạm Hải Nam', note: '' });
    if (over.status === 'RETURN_INSPECTION' || over.status === 'COMPLETED') {
      b.returned_at = b.due_at;
      (b.items[0] as BookingItem).returned_at = b.due_at;
      b.transitions.push({ action: 'booking.return', to: 'RETURN_INSPECTION', at: b.due_at, actor: 'Phạm Hải Nam', note: '' });
    }
    if (over.status === 'COMPLETED') {
      (b.items[0] as BookingItem).conditions = ['NORMAL_WEAR'];
      (b.items[0] as BookingItem).asset_status = 'READY';
      asset.booking_code = null;
      b.settled = true;
      b.refunds.push({ id: `${code}-r1`, bucket: 'SECURITY', status: 'SUCCEEDED', amount: vnd(deposit), reason: 'Hoàn tiền bảo đảm', at: iso(pickup + (pkg.duration_hours + 30) * hour), actions: [] });
      b.transitions.push({ action: 'booking.complete', to: 'COMPLETED', at: iso(pickup + (pkg.duration_hours + 30) * hour), actor: 'Phạm Hải Nam', note: '' });
    }
    if (over.status === 'CANCELLED') b.transitions.push({ action: 'booking.cancel', to: 'CANCELLED', at: iso(pickup - 100 * hour), actor: 'Khách hàng', note: 'Đổi kế hoạch' });
    return b;
  };

  const slotAt = (daysFromNow: number, h: number) => {
    const today = businessToday();
    return new Date(`${today}T${String(h).padStart(2, '0')}:00:00+07:00`).getTime() + daysFromNow * 24 * hour;
  };

  db.bookings = [
    place({ status: 'PENDING_CONFIRMATION', customer_id: 'u-customer', hold_expires_at: iso(now + 12 * 60_000) }, aodaiRed, 1, 'pkg-3d', slotAt(5, 9)),
    place({ status: 'CONFIRMED', customer_id: 'u-customer' }, princess, 1, 'pkg-1d', slotAt(9, 14)),
    place({ status: 'PREPARING', customer_id: 'u-customer2' }, kimono, 0, 'pkg-3d', slotAt(0, 14)),
    place({ status: 'RENTED', customer_id: 'u-customer' }, vampire, 1, 'pkg-3d', slotAt(-2, 9)),
    place({ status: 'RENTED', customer_id: 'u-customer3' }, ninjaTop, 2, 'pkg-3d', slotAt(-5, 9)),
    place({ status: 'RENTED', customer_id: 'u-customer2' }, witch, 1, 'pkg-1d', slotAt(-1, 14)),
    place({ status: 'RETURN_INSPECTION', customer_id: 'u-customer3' }, pirate, 0, 'pkg-3d', slotAt(-4, 9)),
    place({ status: 'COMPLETED', customer_id: 'u-customer' }, aodaiWhite, 0, 'pkg-1d', slotAt(-20, 9)),
    place({ status: 'CANCELLED', customer_id: 'u-customer' }, hanfu, 0, 'pkg-3d', slotAt(-12, 9)),
    place({ status: 'PENDING_CONFIRMATION', customer_id: 'u-customer3', hold_expires_at: iso(now + 6 * 60_000) }, ninjaPants, 0, 'pkg-7d', slotAt(3, 14)),
  ];

  db.support = [
    {
      id: 's1', code: 'HT-1001', topic: 'BOOKING', subject: 'Đổi size áo dài có được không?', status: 'IN_PROGRESS', booking_code: db.bookings[0]?.code ?? null, customer_name: 'Nguyễn Minh An', created_at: iso(now - 5 * hour),
      messages: [
        { from: 'CUSTOMER', author: 'Nguyễn Minh An', body: 'Mình muốn đổi từ size M sang L, còn kịp không?', at: iso(now - 5 * hour) },
        { from: 'STAFF', author: 'Phạm Hải Nam', body: 'Bạn chờ bên mình kiểm tra hàng size L trong ngày nhận nhé.', at: iso(now - 4 * hour) },
      ],
    },
    {
      id: 's2', code: 'HT-1002', topic: 'PAYMENT', subject: 'Khi nào được hoàn tiền bảo đảm?', status: 'OPEN', booking_code: null, customer_name: 'Lê Quốc Duy', created_at: iso(now - 26 * hour),
      messages: [{ from: 'CUSTOMER', author: 'Lê Quốc Duy', body: 'Mình đã trả đồ hôm qua, bao lâu thì nhận lại tiền bảo đảm?', at: iso(now - 26 * hour) }],
    },
  ];

  db.audit = [
    { id: 'e1', occurred_at: iso(now - 2 * hour), actor: 'Phạm Hải Nam', action: 'booking.confirm', resource_type: 'booking', resource_id: db.bookings[1]?.code ?? '', result: 'SUCCESS', reason: '', changes: { status: 'CONFIRMED' } },
    { id: 'e2', occurred_at: iso(now - 5 * hour), actor: 'Hoàng Gia Bảo', action: 'user.role_grant', resource_type: 'user', resource_id: 'u-staff', result: 'SUCCESS', reason: 'Nhân viên mới ca sáng', changes: { role: 'rental_staff' } },
    { id: 'e3', occurred_at: iso(now - 8 * hour), actor: 'Hệ thống', action: 'login', resource_type: 'user', resource_id: 'u-customer3', result: 'DENIED', reason: 'Sai mật khẩu', changes: {} },
    { id: 'e4', occurred_at: iso(now - 26 * hour), actor: 'Đỗ Thu Hà', action: 'asset.send_to_repair', resource_type: 'asset', resource_id: assets[14]?.code ?? '', result: 'SUCCESS', reason: 'Rách đường may tay trái', changes: { status: 'REPAIR' } },
    { id: 'e5', occurred_at: iso(now - 5 * 24 * hour), actor: 'Hoàng Gia Bảo', action: 'policy.publish', resource_type: 'policy', resource_id: 'mock-2026-10-31', result: 'SUCCESS', reason: 'Cập nhật chính sách quý IV', changes: {} },
  ];

  return db;
}

// sessionStorage keeps the mock's state across reloads while developing.
const storageKey = 'webee.mock.db.v2';

function load(): MockDb {
  try {
    const raw = sessionStorage.getItem(storageKey);
    if (raw) return JSON.parse(raw) as MockDb;
  } catch {
    // Falls through to fresh data.
  }
  return seed();
}

export const db: MockDb = load();

export function persist() {
  try {
    sessionStorage.setItem(storageKey, JSON.stringify(db));
  } catch {
    // The mock then lasts until the next reload.
  }
}

export function nextCode(prefix: string): string {
  return `${prefix}-${1000 + db.nextSeq++}`;
}

// ---- Views ----

function fromPrice(prices: { package_id: string; amount: Money }[]) {
  const sorted = [...prices].sort((a, b) => a.amount.amount - b.amount.amount);
  const first = sorted[0];
  return { price: first?.amount ?? vnd(0), label: db.packages.find((p) => p.id === first?.package_id)?.label ?? '' };
}

function categoryRef(id: string | null) {
  const c = db.categories.find((x) => x.id === id);
  return c ? { id: c.id, slug: c.slug, name: c.name } : null;
}

export function itemCard(it: MockCatalogItem): OfferingCard {
  const from = fromPrice(it.prices);
  return {
    id: it.id, kind: 'ITEM', slug: it.slug, name: it.name, category: categoryRef(it.category_id), images: it.images, from_price: from.price, from_package_label: from.label,
    sizes: [...new Set(it.variants.map((v) => v.size))], colors: [...new Set(it.variants.map((v) => v.color))], component_count: 1,
  };
}

export function bundleCard(b: MockBundle): OfferingCard {
  const from = fromPrice(b.prices);
  const items = b.components.map((c) => db.items.find((i) => i.id === c.item_id));
  return {
    id: b.id, kind: 'BUNDLE', slug: b.slug, name: b.name, category: categoryRef(b.category_id), images: b.images, from_price: from.price, from_package_label: from.label,
    sizes: [...new Set(items.flatMap((i) => i?.variants.map((v) => v.size) ?? []))], colors: [...new Set(items.flatMap((i) => i?.variants.map((v) => v.color) ?? []))], component_count: b.components.length,
  };
}

function packagePrices(prices: { package_id: string; amount: Money }[]) {
  return prices.flatMap((p) => {
    const pkg = db.packages.find((x) => x.id === p.package_id);
    return pkg ? [{ package: pkg, price: p.amount }] : [];
  });
}

export function bundleDeposit(b: MockBundle): number {
  if (b.deposit_override) return b.deposit_override.amount;
  return b.components.reduce((sum, c) => sum + (db.items.find((i) => i.id === c.item_id)?.security_deposit.amount ?? 0) * c.quantity, 0);
}

export function itemDetail(it: MockCatalogItem): OfferingDetail {
  return { ...itemCard(it), description: it.description, variants: it.variants, components: [], prices: packagePrices(it.prices), security_deposit: it.security_deposit };
}

export function bundleDetail(b: MockBundle): OfferingDetail {
  return {
    ...bundleCard(b), description: b.description, variants: [], prices: packagePrices(b.prices), security_deposit: vnd(bundleDeposit(b)),
    components: b.components.map((c) => ({
      key: c.key, item_name: c.item_name, quantity: c.quantity,
      options: db.items.find((i) => i.id === c.item_id)?.variants.filter((v) => c.options.includes(v.id)) ?? [],
    })),
  };
}

export function customerOf(b: MockBooking) {
  const u = db.users.find((x) => x.id === b.customer_id);
  return { id: b.customer_id, name: u?.full_name ?? '', email: u?.email ?? '', phone: u?.phone ?? '' };
}

function sum(rows: { amount: number }[]): number {
  return rows.reduce((n, r) => n + r.amount, 0);
}

export function financeOf(b: MockBooking): BookingDetail['finance'] {
  const active = b.charges.filter((c) => !c.reversed);
  const by = (k: MockCharge['kind']) => sum(active.filter((c) => c.kind === k));
  const rent = by('RENT');
  const security = by('SECURITY_DEPOSIT');
  const delivery = by('DELIVERY_FEE');
  const fees = sum(active.filter((c) => !['RENT', 'SECURITY_DEPOSIT', 'DELIVERY_FEE', 'RESERVATION_DEPOSIT'].includes(c.kind)));
  const paid = sum(b.payments);
  const refunded = sum(b.refunds.filter((r) => r.status === 'SUCCEEDED').map((r) => ({ amount: r.amount.amount })));
  const pending = sum(b.refunds.filter((r) => ['REQUESTED', 'APPROVED', 'PROCESSING'].includes(r.status)).map((r) => ({ amount: r.amount.amount })));
  const outstanding = Math.max(0, rent + security + delivery + fees - paid);
  const securityHeld = Math.max(0, Math.min(security, paid - rent - delivery) - fees - refunded - pending);
  let state: PaymentState = 'UNPAID';
  if (paid > 0) state = outstanding > 0 ? 'PARTIALLY_PAID' : 'PAID';
  if (pending > 0) state = 'REFUND_PENDING';
  else if (refunded > 0) state = refunded >= paid ? 'REFUNDED' : 'PARTIALLY_REFUNDED';
  return {
    payment_state: state, rent_total: vnd(rent), discount_total: vnd(sum(b.lines.map((l) => ({ amount: l.discount.amount })))),
    reservation_deposit: vnd(Math.ceil(rent * ((db.policy.reservation.rent_percent ?? 0) / 100))), reservation_treatment: db.policy.reservation.treatment,
    security_required: vnd(security), security_held: vnd(securityHeld), delivery_total: vnd(delivery), fees_total: vnd(fees), paid_total: vnd(paid), refund_pending: vnd(pending), refunded_total: vnd(refunded), outstanding: vnd(outstanding),
    charges: b.charges.map((c) => ({ id: c.id, kind: c.kind, amount: vnd(c.amount), reversed: c.reversed, reason: c.reason })),
    payments: b.payments.map((p) => ({ id: p.id, method: p.method, status: 'SUCCEEDED', amount: vnd(p.amount), at: p.at })),
    refunds: b.refunds,
  };
}

/** What the customer may pay online now: the reservation deposit, or all that is owed. */
export function paymentOptions(b: MockBooking, own: boolean): PaymentOption[] {
  if (!own || !['PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING'].includes(b.status)) return [];
  const f = financeOf(b);
  if (f.outstanding.amount <= 0) return [];
  const full: PaymentOption = { purpose: 'FULL', amount: f.outstanding };
  const reservationLeft = f.reservation_deposit.amount - f.paid_total.amount;
  return reservationLeft > 0 && reservationLeft < f.outstanding.amount
    ? [{ purpose: 'RESERVATION', amount: vnd(reservationLeft) }, full]
    : [full];
}

export function isOverdue(b: MockBooking): boolean {
  return b.status === 'RENTED' && Date.now() > new Date(b.due_at).getTime() + db.policy.late_fee.grace_minutes * 60_000;
}

export function summaryOf(b: MockBooking): BookingSummary {
  const first = b.lines[0];
  const extra = b.lines.length > 1 ? ` và ${b.lines.length - 1} món khác` : '';
  return {
    id: b.id, code: b.code, status: b.status, payment_state: financeOf(b).payment_state, customer: customerOf(b), pickup_at: b.pickup_at, due_at: b.due_at, outbound: b.outbound, return: b.return,
    package_label: b.package_label, item_count: b.items.length, headline: `${first?.name ?? ''}${extra}`, image: first?.image ?? null,
    rent_total: financeOf(b).rent_total, is_overdue: isOverdue(b), hold_expires_at: b.hold_expires_at, created_at: b.created_at,
  };
}

export function actionsFor(b: MockBooking, can: (p: string) => boolean, own: boolean): BookingAction[] {
  const out: BookingAction[] = [];
  const add = (a: BookingAction, ok: boolean) => { if (ok) out.push(a); };
  const preHandover = ['PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING'].includes(b.status);
  const pastNoShow = Date.now() > new Date(b.pickup_at).getTime() + db.policy.cancellation.no_show_after_hours * hour;
  add('confirm', b.status === 'PENDING_CONFIRMATION' && can('booking.confirm'));
  add('start_preparation', b.status === 'CONFIRMED' && can('rental.prepare'));
  add('handover', b.status === 'PREPARING' && can('rental.handover'));
  add('record_return', b.status === 'RENTED' && can('rental.return_receive'));
  add('report_missing', b.status === 'RENTED' && can('rental.return_receive'));
  add('confirm_lost', b.status === 'RENTED' && can('rental.confirm_lost'));
  add('inspect', b.items.some((i) => i.state === 'RETURNED') && ['RENTED', 'RETURN_INSPECTION'].includes(b.status) && can('rental.return_inspect'));
  add('propose_fee', ['RENTED', 'RETURN_INSPECTION'].includes(b.status) && can('fee.assess'));
  add('record_payment', financeOf(b).outstanding.amount > 0 && !['CANCELLED', 'EXPIRED', 'COMPLETED'].includes(b.status) && can('payment.record'));
  const inspected = b.items.every((i) => i.state === 'LOST' || i.conditions.length > 0);
  const feesOpen = b.fees.some((f) => f.status === 'PROPOSED' || f.status === 'DISPUTED');
  add('settle', b.status === 'RETURN_INSPECTION' && !b.settled && inspected && !feesOpen && can('refund.create'));
  const refundsOpen = b.refunds.some((r) => ['REQUESTED', 'APPROVED', 'PROCESSING'].includes(r.status));
  add('complete', b.status === 'RETURN_INSPECTION' && b.settled && !refundsOpen && can('booking.complete'));
  add('cancel', preHandover && !pastNoShow && (can('booking.cancel_any') || (own && can('booking.cancel_own'))));
  add('no_show', preHandover && pastNoShow && can('booking.cancel_any'));
  const cutoff = new Date(b.pickup_at).getTime() - db.policy.booking.reschedule_cutoff_hours * hour;
  add('reschedule', ['PENDING_CONFIRMATION', 'CONFIRMED'].includes(b.status) && (can('booking.reschedule_any') || (own && can('booking.reschedule_own') && Date.now() < cutoff)));
  return out;
}

export function detailOf(b: MockBooking, can: (p: string) => boolean, own: boolean): BookingDetail {
  return {
    ...summaryOf(b), handed_over_at: b.handed_over_at, returned_at: b.returned_at, policy_version: db.policy.version, lines: b.lines, items: b.items, transitions: [...b.transitions].reverse(),
    finance: {
      ...financeOf(b),
      refunds: b.refunds.map((r) => ({
        ...r,
        actions: [
          ...(r.status === 'REQUESTED' && can('refund.approve') ? ['approve', 'reject'] as const : []),
          ...(r.status === 'REQUESTED' && can('payment.record') ? ['payout'] as const : []),
        ],
      })),
    },
    fees: b.fees.map((f) => ({
      ...f,
      actions: [
        ...(f.status === 'PROPOSED' && can('fee.approve') ? ['approve', 'reject'] as const : []),
        ...(f.status === 'APPROVED' && can('fee.waive') ? ['waive'] as const : []),
      ],
    })),
    actions: actionsFor(b, can, own),
    payment_options: paymentOptions(b, own),
  };
}

export function assetActions(a: Asset, can: (p: string) => boolean): AssetAction[] {
  const inspect = can('rental.return_inspect') || can('inventory.manage');
  const manage = can('inventory.manage');
  switch (a.status) {
    case 'NEEDS_INSPECTION': return [...(inspect ? ['asset.pass_inspection', 'asset.require_cleaning'] as const : []), ...(manage ? ['asset.send_to_repair', 'asset.retire'] as const : [])];
    case 'CLEANING': return inspect ? ['asset.complete_cleaning'] : [];
    case 'REPAIR': return manage ? ['asset.complete_repair', 'asset.retire'] : [];
    case 'MISSING': return manage ? ['asset.record_found', 'asset.retire'] : [];
    case 'READY': return manage ? ['asset.send_to_repair', 'asset.report_missing', 'asset.retire'] : [];
    default: return [];
  }
}

export const conditionToAsset = (conditions: Condition[]): AssetStatus => {
  if (conditions.some((c) => c === 'ROUTINE_SOILING' || c === 'ABNORMAL_SOILING')) return 'CLEANING';
  if (conditions.every((c) => c === 'NORMAL_WEAR')) return 'READY';
  return 'NEEDS_INSPECTION';
};
