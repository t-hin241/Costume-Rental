// Wire types of internal/httpapi. Enum values mirror the Go domain packages
// exactly.

export type Role = 'customer' | 'rental_staff' | 'inventory_manager' | 'finance' | 'admin';

export type Permission = string;

export interface Session {
  status: 'authenticated';
  user_id: string;
  roles: Role[];
  permissions: Permission[];
  mfa: boolean;
  expires_at: string;
  recovery_codes?: string[];
}

export interface LoginChallenge {
  status: 'mfa_required' | 'mfa_enrollment_required';
  challenge: string;
  expires_at: string;
}

export type LoginResult = Session | LoginChallenge;

export interface MFAEnrollment {
  secret: string;
  otpauth_uri: string;
}

export type UserStatus = 'ACTIVE' | 'LOCKED';

export interface User {
  id: string;
  email: string;
  status: UserStatus;
  roles: Role[];
  created_at: string;
}

export interface PermissionMatrix {
  version: string;
  matrix: Record<Role, Permission[]>;
}

// ---- CONTRACT: shared ----

/** Whole VND amounts; the currency is always explicit. */
export interface Money {
  amount: number;
  currency: 'VND';
}

export interface Page<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
}

export type CatalogStatus = 'DRAFT' | 'ACTIVE' | 'ARCHIVED';
export type OfferingKind = 'ITEM' | 'BUNDLE';
export type LegMode = 'STORE' | 'COURIER';

export interface Image {
  url: string;
  alt: string;
}

export interface Category {
  id: string;
  slug: string;
  name: string;
  image: Image | null;
  offering_count: number;
}

export interface RentalPackage {
  id: string;
  code: string;
  label: string;
  duration_hours: number;
  status: CatalogStatus;
}

export interface Variant {
  id: string;
  sku: string;
  size: string;
  color: string;
  status: CatalogStatus;
}

export interface OfferingCard {
  id: string;
  kind: OfferingKind;
  slug: string;
  name: string;
  category: Pick<Category, 'id' | 'slug' | 'name'> | null;
  images: Image[];
  from_price: Money;
  from_package_label: string;
  sizes: string[];
  colors: string[];
  component_count: number;
}

export interface BundleComponent {
  key: string;
  item_name: string;
  quantity: number;
  options: Variant[];
}

export interface PackagePrice {
  package: RentalPackage;
  price: Money;
}

export interface OfferingDetail extends OfferingCard {
  description: string;
  variants: Variant[];
  components: BundleComponent[];
  prices: PackagePrice[];
  security_deposit: Money;
}

export interface CatalogFacets {
  sizes: string[];
  colors: string[];
}

export interface OfferingPage extends Page<OfferingCard> {
  facets: CatalogFacets;
}

export interface StoreInfo {
  name: string;
  address: string;
  phone: string;
  email: string;
  hours: string;
  online_payment_available: boolean;
  payment_methods: PaymentMethod[];
  payment_channels: PaymentChannel[];
  payment_instructions: string;
}

export interface StoreSlot {
  id: string;
  label: string;
}

export interface CalendarDay {
  date: string;
  open: boolean;
  reason: string | null;
  slots: StoreSlot[];
}

export interface StoreCalendar {
  timezone: string;
  min_date: string;
  max_date: string;
  days: CalendarDay[];
}

export interface FulfillmentOptions {
  outbound: LegMode[];
  return: LegMode[];
  zones: { id: string; name: string; wards: { code: string; name: string }[] }[];
}

export interface BookingLineInput {
  kind: OfferingKind;
  offering_id: string;
  variant_id?: string;
  selections?: Record<string, string>;
  quantity: number;
}

export interface DeliveryAddress {
  recipient_name: string;
  phone: string;
  line: string;
  ward_code: string;
}

export interface BookingInput {
  package_id: string;
  pickup_date: string;
  slot_id: string;
  outbound: LegMode;
  return: LegMode;
  lines: BookingLineInput[];
  address?: DeliveryAddress;
  coupon_code?: string;
}

export interface Shortage {
  line_ref: string;
  label: string;
  requested: number;
  available: number;
}

export interface Availability {
  available: boolean;
  shortages: Shortage[];
  pickup_at: string;
  due_at: string;
}

export interface QuoteLine {
  label: string;
  quantity: number;
  list_rent: Money;
  discount: Money;
  rent: Money;
  security: Money;
}

export type ReservationTreatment = 'APPLIED_TO_RENT' | 'REFUNDED_AFTER_RETURN';

export interface Quote {
  lines: QuoteLine[];
  pickup_at: string;
  due_at: string;
  gross_rent: Money;
  discount_total: Money;
  rent_total: Money;
  reservation_deposit: Money;
  reservation_treatment: ReservationTreatment | null;
  security_total: Money;
  delivery_outbound_fee: Money;
  delivery_return_fee: Money;
  /** What must be received for the store to hold the schedule. */
  due_to_confirm: Money;
  /** Everything to be paid before the goods change hands. */
  due_before_handover: Money;
  /** Refundable part of due_before_handover. */
  refundable_total: Money;
  hold_minutes: number;
  policy_version: string;
}

export interface CancellationTier {
  min_notice_hours: number;
  fee_percent: number;
}

export interface LateFeeTier {
  up_to_hours: number;
  percent: number;
}

export interface PublicPolicy {
  version: string;
  hold_minutes: number;
  reservation: {
    kind: 'NONE' | 'FIXED' | 'RENT_PERCENT';
    amount: Money | null;
    rent_percent: number | null;
    treatment: ReservationTreatment;
  };
  cancellation: {
    tiers: CancellationTier[];
    no_show_after_hours: number;
    no_show_fee_percent: number;
  };
  late_fee: {
    grace_minutes: number;
    tiers: LateFeeTier[];
    extra_period_hours: number;
    extra_percent: number;
  };
  loss_verification_hours: number;
  reschedule_cutoff_hours: number;
  modification_cutoff_hours: number;
}

// ---- CONTRACT: bookings ----

export type BookingStatus =
  | 'PENDING_CONFIRMATION'
  | 'CONFIRMED'
  | 'PREPARING'
  | 'RENTED'
  | 'RETURN_INSPECTION'
  | 'COMPLETED'
  | 'CANCELLED'
  | 'EXPIRED';

export type PaymentState =
  | 'UNPAID'
  | 'PARTIALLY_PAID'
  | 'PAID'
  | 'REFUND_PENDING'
  | 'PARTIALLY_REFUNDED'
  | 'REFUNDED';

/** What the caller may do now, decided by the backend for this booking. */
export type BookingAction =
  | 'confirm'
  | 'start_preparation'
  | 'handover'
  | 'record_return'
  | 'report_missing'
  | 'confirm_lost'
  | 'inspect'
  | 'propose_fee'
  | 'record_payment'
  | 'settle'
  | 'complete'
  | 'cancel'
  | 'no_show'
  | 'reschedule';

export interface CustomerRef {
  id: string;
  name: string;
  email: string;
  phone: string;
}

export interface BookingSummary {
  id: string;
  code: string;
  status: BookingStatus;
  payment_state: PaymentState;
  customer: CustomerRef;
  pickup_at: string;
  due_at: string;
  outbound: LegMode;
  return: LegMode;
  package_label: string;
  item_count: number;
  headline: string;
  image: Image | null;
  rent_total: Money;
  is_overdue: boolean;
  hold_expires_at: string | null;
  created_at: string;
}

export interface BookingLine {
  id: string;
  kind: OfferingKind;
  name: string;
  image: Image | null;
  selection: string;
  quantity: number;
  list_rent: Money;
  discount: Money;
  rent: Money;
  security: Money;
}

export type Condition =
  | 'NORMAL_WEAR'
  | 'ROUTINE_SOILING'
  | 'ABNORMAL_SOILING'
  | 'MINOR_REPAIRABLE'
  | 'MAJOR_REPAIRABLE'
  | 'IRREPARABLE'
  | 'NEEDS_VERIFICATION';

export type BookingItemState =
  | 'ALLOCATED'
  | 'WITH_CUSTOMER'
  | 'RETURNED'
  | 'MISSING_PENDING'
  | 'LOST';

export interface BookingItem {
  asset_id: string;
  asset_code: string;
  asset_status: AssetStatus;
  line_id: string;
  name: string;
  variant_label: string;
  state: BookingItemState;
  preexisting_issues: string;
  returned_at: string | null;
  conditions: Condition[];
  inspection_notes: string;
}

export interface BookingTransition {
  action: string;
  to: BookingStatus;
  at: string;
  actor: string;
  note: string;
}

export type ChargeKind =
  | 'RENT'
  | 'SECURITY_DEPOSIT'
  | 'RESERVATION_DEPOSIT'
  | 'DELIVERY_FEE'
  | 'CANCELLATION_FEE'
  | 'LATE_FEE'
  | 'REDELIVERY_FEE'
  | 'CLEANING_FEE'
  | 'DAMAGE_FEE'
  | 'LOSS_FEE'
  | 'OTHER_FEE';

export type PaymentMethod = 'CASH' | 'BANK_TRANSFER' | 'GATEWAY';
export type PaymentChannel = 'ATM_CARD' | 'INTL_CARD' | 'E_WALLET' | 'BANK_QR';
export type PaymentPurpose = 'RESERVATION' | 'FULL';
export type PaymentStatus = 'PENDING' | 'SUCCEEDED' | 'FAILED' | 'EXPIRED';
export type RefundStatus = 'REQUESTED' | 'APPROVED' | 'REJECTED' | 'PROCESSING' | 'SUCCEEDED' | 'FAILED';
export type Bucket = 'RENTAL' | 'SECURITY' | 'RESERVATION';

/** An amount the customer may pay online now, worked out by the backend. */
export interface PaymentOption {
  purpose: PaymentPurpose;
  amount: Money;
}

/** One attempt on the gateway's hosted page. Only the server knows its result. */
export interface PaymentSession {
  id: string;
  booking_id: string;
  booking_code: string;
  purpose: PaymentPurpose;
  channel: PaymentChannel;
  amount: Money;
  status: PaymentStatus;
  checkout_url: string;
  expires_at: string;
}

export interface FinanceCharge {
  id: string;
  kind: ChargeKind;
  amount: Money;
  reversed: boolean;
  reason: string;
}

export interface FinancePayment {
  id: string;
  method: PaymentMethod;
  status: PaymentStatus;
  amount: Money;
  at: string;
}

export type RefundAction = 'approve' | 'reject' | 'payout';

export interface FinanceRefund {
  id: string;
  bucket: Bucket;
  status: RefundStatus;
  amount: Money;
  reason: string;
  at: string;
  /** What the caller may do with this refund now. */
  actions: RefundAction[];
}

export interface BookingFinance {
  payment_state: PaymentState;
  rent_total: Money;
  discount_total: Money;
  reservation_deposit: Money;
  reservation_treatment: ReservationTreatment | null;
  security_required: Money;
  security_held: Money;
  delivery_total: Money;
  fees_total: Money;
  paid_total: Money;
  refund_pending: Money;
  refunded_total: Money;
  outstanding: Money;
  charges: FinanceCharge[];
  payments: FinancePayment[];
  refunds: FinanceRefund[];
}

export type FeeKind =
  | 'LATE_RETURN'
  | 'CLEANING'
  | 'DAMAGE_REPAIR'
  | 'DAMAGE_REPLACEMENT'
  | 'LOST_ITEM'
  | 'REDELIVERY';

export type AssessmentStatus = 'PROPOSED' | 'APPROVED' | 'REJECTED' | 'DISPUTED' | 'WAIVED' | 'SUPERSEDED';

export type FeeAction = 'approve' | 'reject' | 'waive';

export interface FeeAssessment {
  id: string;
  kind: FeeKind;
  status: AssessmentStatus;
  amount: Money;
  asset_code: string;
  basis: string;
  created_at: string;
  /** What the caller may do with this fee now. */
  actions: FeeAction[];
}

export interface BookingDetail extends BookingSummary {
  handed_over_at: string | null;
  returned_at: string | null;
  policy_version: string;
  lines: BookingLine[];
  items: BookingItem[];
  transitions: BookingTransition[];
  finance: BookingFinance;
  fees: FeeAssessment[];
  actions: BookingAction[];
  payment_options: PaymentOption[];
}

export interface CancellationPreview {
  fee_percent: number;
  fee: Money;
  refund: Money;
  owed: Money;
  basis: string;
}

// ---- CONTRACT: operations ----

export interface OpsCounters {
  pending_confirmation: number;
  to_prepare: number;
  handovers_today: number;
  returns_today: number;
  inspections_open: number;
  overdue: number;
  conflicts: number;
  assets_blocked: number;
}

export interface OpsOverview {
  date: string;
  counters: OpsCounters;
  queue: BookingSummary[];
}

export type ScheduleEventKind = 'HANDOVER' | 'RETURN';

export interface ScheduleEvent {
  id: string;
  kind: ScheduleEventKind;
  at: string;
  mode: LegMode;
  booking: BookingSummary;
}

export interface OverdueAlert {
  booking: BookingSummary;
  overdue_hours: number;
  items_out: number;
}

export interface ConflictAlert {
  overdue_booking: BookingSummary;
  asset_code: string;
  item_name: string;
  endangered: BookingSummary[];
}

export interface OpsAlerts {
  overdue: OverdueAlert[];
  conflicts: ConflictAlert[];
}

// ---- CONTRACT: inventory ----

export type AssetStatus =
  | 'READY'
  | 'RENTED'
  | 'NEEDS_INSPECTION'
  | 'CLEANING'
  | 'REPAIR'
  | 'MISSING'
  | 'RETIRED';

export type AssetAction =
  | 'asset.pass_inspection'
  | 'asset.require_cleaning'
  | 'asset.complete_cleaning'
  | 'asset.send_to_repair'
  | 'asset.complete_repair'
  | 'asset.report_missing'
  | 'asset.record_found'
  | 'asset.retire';

export interface Asset {
  id: string;
  code: string;
  variant_id: string;
  sku: string;
  item_name: string;
  size: string;
  color: string;
  status: AssetStatus;
  note: string;
  booking_code: string | null;
  updated_at: string;
  actions: AssetAction[];
}

export interface AssetHistoryEntry {
  action: string;
  from: AssetStatus | null;
  to: AssetStatus;
  at: string;
  actor: string;
  reason: string;
}

// ---- CONTRACT: account ----

export interface Profile {
  email: string;
  full_name: string;
  phone: string;
}

export type SupportStatus = 'OPEN' | 'IN_PROGRESS' | 'RESOLVED';
export type SupportTopic = 'BOOKING' | 'PAYMENT' | 'PRODUCT' | 'DELIVERY' | 'OTHER';

export interface SupportMessage {
  from: 'CUSTOMER' | 'STAFF';
  author: string;
  body: string;
  at: string;
}

export interface SupportRequest {
  id: string;
  code: string;
  topic: SupportTopic;
  subject: string;
  status: SupportStatus;
  booking_code: string | null;
  customer_name: string;
  messages: SupportMessage[];
  created_at: string;
}

// ---- CONTRACT: admin ----

export interface DashboardKpi {
  key: 'bookings' | 'revenue' | 'customers' | 'active_rentals' | 'utilization' | 'overdue';
  value: number;
  /** Change from the previous period, in percent; null when not comparable. */
  change_percent: number | null;
}

export interface Dashboard {
  range_days: number;
  revenue_recognized_note: string;
  kpis: DashboardKpi[];
  revenue_series: { date: string; revenue: Money }[];
  bookings_by_status: { status: BookingStatus; count: number }[];
  inventory_by_status: { status: AssetStatus; count: number }[];
  top_offerings: { name: string; bookings: number; revenue: Money }[];
}

export interface AdminUser extends User {
  full_name: string;
  phone: string;
  mfa_enabled: boolean;
  last_seen_at: string | null;
}

export interface AdminItem {
  id: string;
  slug: string;
  name: string;
  category_id: string | null;
  description: string;
  images: Image[];
  status: CatalogStatus;
  individually_rentable: boolean;
  variants: Variant[];
  prices: { package_id: string; amount: Money }[];
  security_deposit: Money;
  daily_rate: Money;
  buffer_class: string;
  version: number;
  asset_count: number;
}

export interface AdminBundleComponent {
  key: string;
  item_id: string;
  item_name: string;
  quantity: number;
  options: string[];
}

export interface AdminBundle {
  id: string;
  slug: string;
  name: string;
  category_id: string | null;
  description: string;
  images: Image[];
  status: CatalogStatus;
  components: AdminBundleComponent[];
  prices: { package_id: string; amount: Money }[];
  deposit_override: Money | null;
  version: number;
}

export type AuditResult = 'SUCCESS' | 'DENIED' | 'FAILED';

export interface AuditEvent {
  id: string;
  occurred_at: string;
  actor: string;
  action: string;
  resource_type: string;
  resource_id: string;
  result: AuditResult;
  reason: string;
  changes: Record<string, string>;
}

/** Field values are what rentals.Policy stores, in display-friendly units. */
export interface PolicyDocument {
  version: string;
  published_at: string;
  published_by: string;
  booking: {
    hold_minutes: number;
    min_lead_hours: number;
    max_advance_hours: number;
    max_lines_per_booking: number;
    max_quantity_per_line: number;
    reschedule_cutoff_hours: number;
  };
  confirmation: {
    mode: 'STAFF' | 'AUTO';
    funds: FundsRequirement;
    handover_funds: FundsRequirement;
  };
  reservation: PublicPolicy['reservation'];
  buffers: {
    default_preparation_hours: number;
    default_turnaround_hours: number;
    by_class: { class: string; preparation_hours: number; turnaround_hours: number }[];
  };
  cancellation: PublicPolicy['cancellation'];
  late_fee: PublicPolicy['late_fee'];
  loss_verification_hours: number;
  fee_approval_above: Money;
  modification: {
    self_service_cutoff_hours: number;
    self_service_size_changes: number;
    hold_minutes: number;
  };
}

export type FundsRequirement = 'NONE' | 'RESERVATION_DEPOSIT' | 'FULL_RENT' | 'FULL_RENT_AND_SECURITY';

export type CampaignType = 'LINE_DISCOUNT' | 'COUPON' | 'ORDER_DISCOUNT' | 'MEMBER_DISCOUNT';
export type CampaignStatus = 'DRAFT' | 'ACTIVE' | 'PAUSED' | 'ENDED';

export interface Campaign {
  id: string;
  code: string;
  name: string;
  type: CampaignType;
  status: CampaignStatus;
  value_type: 'PERCENT' | 'FIXED_AMOUNT';
  percent: number | null;
  amount: Money | null;
  valid_from: string;
  valid_until: string;
  combinable: boolean;
  used: number;
  usage_limit: number | null;
}
