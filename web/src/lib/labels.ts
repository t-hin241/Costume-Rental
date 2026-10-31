import type {
  AssessmentStatus, AssetAction, AssetStatus, AuditResult, BookingAction, BookingItemState, BookingStatus, Bucket,
  CampaignStatus, CampaignType, CatalogStatus, ChargeKind, Condition, FeeKind, FundsRequirement, LegMode,
  PaymentChannel, PaymentMethod, PaymentPurpose, PaymentState, PaymentStatus, RefundStatus, Role, SupportStatus, SupportTopic, UserStatus,
} from './api/types';

// Vietnamese labels for the backend's enums. Status colours always travel
// with these words (StatusBadge), so a state is never told by colour alone.

export type Tone = 'neutral' | 'info' | 'brand' | 'success' | 'warning' | 'danger';

export interface StatusLabel {
  label: string;
  tone: Tone;
}

export const bookingStatus: Record<BookingStatus, StatusLabel> = {
  PENDING_CONFIRMATION: { label: 'Chờ xác nhận', tone: 'warning' },
  CONFIRMED: { label: 'Đã xác nhận', tone: 'info' },
  PREPARING: { label: 'Đang chuẩn bị', tone: 'brand' },
  RENTED: { label: 'Đang thuê', tone: 'brand' },
  RETURN_INSPECTION: { label: 'Đang kiểm tra', tone: 'warning' },
  COMPLETED: { label: 'Hoàn tất', tone: 'success' },
  CANCELLED: { label: 'Đã hủy', tone: 'neutral' },
  EXPIRED: { label: 'Hết hạn giữ', tone: 'neutral' },
};

/** The happy path shown as a stepper; cancelled and expired leave it. */
export const bookingFlow: BookingStatus[] = [
  'PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING', 'RENTED', 'RETURN_INSPECTION', 'COMPLETED',
];

export const paymentState: Record<PaymentState, StatusLabel> = {
  UNPAID: { label: 'Chưa thanh toán', tone: 'danger' },
  PARTIALLY_PAID: { label: 'Thanh toán một phần', tone: 'warning' },
  PAID: { label: 'Đã thanh toán', tone: 'success' },
  REFUND_PENDING: { label: 'Chờ hoàn tiền', tone: 'warning' },
  PARTIALLY_REFUNDED: { label: 'Đã hoàn một phần', tone: 'info' },
  REFUNDED: { label: 'Đã hoàn tiền', tone: 'success' },
};

export const paymentStatus: Record<PaymentStatus, StatusLabel> = {
  PENDING: { label: 'Đang xử lý', tone: 'warning' },
  SUCCEEDED: { label: 'Thành công', tone: 'success' },
  FAILED: { label: 'Thất bại', tone: 'danger' },
  EXPIRED: { label: 'Hết hạn', tone: 'neutral' },
};

export const refundStatus: Record<RefundStatus, StatusLabel> = {
  REQUESTED: { label: 'Chờ duyệt', tone: 'warning' },
  APPROVED: { label: 'Đã duyệt', tone: 'info' },
  REJECTED: { label: 'Từ chối', tone: 'neutral' },
  PROCESSING: { label: 'Đang hoàn', tone: 'info' },
  SUCCEEDED: { label: 'Đã hoàn', tone: 'success' },
  FAILED: { label: 'Hoàn thất bại', tone: 'danger' },
};

export const assetStatus: Record<AssetStatus, StatusLabel> = {
  READY: { label: 'Sẵn sàng', tone: 'success' },
  RENTED: { label: 'Đang cho thuê', tone: 'brand' },
  NEEDS_INSPECTION: { label: 'Cần kiểm tra', tone: 'warning' },
  CLEANING: { label: 'Đang vệ sinh', tone: 'info' },
  REPAIR: { label: 'Đang sửa', tone: 'warning' },
  MISSING: { label: 'Thất lạc', tone: 'danger' },
  RETIRED: { label: 'Ngừng dùng', tone: 'neutral' },
};

export const assetAction: Record<AssetAction, { label: string; needsReason: boolean; needsChecklist: boolean }> = {
  'asset.pass_inspection': { label: 'Đạt kiểm tra', needsReason: false, needsChecklist: true },
  'asset.require_cleaning': { label: 'Chuyển vệ sinh', needsReason: false, needsChecklist: true },
  'asset.complete_cleaning': { label: 'Vệ sinh xong', needsReason: false, needsChecklist: false },
  'asset.send_to_repair': { label: 'Gửi sửa chữa', needsReason: true, needsChecklist: false },
  'asset.complete_repair': { label: 'Sửa xong', needsReason: false, needsChecklist: false },
  'asset.report_missing': { label: 'Báo thất lạc', needsReason: true, needsChecklist: false },
  'asset.record_found': { label: 'Đã tìm thấy', needsReason: true, needsChecklist: false },
  'asset.retire': { label: 'Ngừng dùng', needsReason: true, needsChecklist: false },
};

export const bookingItemState: Record<BookingItemState, StatusLabel> = {
  ALLOCATED: { label: 'Đã giữ cho đơn', tone: 'info' },
  WITH_CUSTOMER: { label: 'Khách đang giữ', tone: 'brand' },
  RETURNED: { label: 'Đã trả', tone: 'success' },
  MISSING_PENDING: { label: 'Thiếu, chờ xác minh', tone: 'warning' },
  LOST: { label: 'Đã xác nhận mất', tone: 'danger' },
};

export const condition: Record<Condition, StatusLabel> = {
  NORMAL_WEAR: { label: 'Hao mòn tự nhiên', tone: 'success' },
  ROUTINE_SOILING: { label: 'Bẩn thông thường', tone: 'info' },
  ABNORMAL_SOILING: { label: 'Bẩn bất thường', tone: 'warning' },
  MINOR_REPAIRABLE: { label: 'Hư nhẹ, sửa được', tone: 'warning' },
  MAJOR_REPAIRABLE: { label: 'Hư nặng, sửa được', tone: 'danger' },
  IRREPARABLE: { label: 'Không thể sửa', tone: 'danger' },
  NEEDS_VERIFICATION: { label: 'Cần xác minh', tone: 'warning' },
};

export const feeKind: Record<FeeKind, string> = {
  LATE_RETURN: 'Phí trả trễ',
  CLEANING: 'Phí vệ sinh',
  DAMAGE_REPAIR: 'Phí sửa chữa',
  DAMAGE_REPLACEMENT: 'Bồi thường thay thế',
  LOST_ITEM: 'Bồi thường mất món',
  REDELIVERY: 'Phí giao lại',
};

export const assessmentStatus: Record<AssessmentStatus, StatusLabel> = {
  PROPOSED: { label: 'Chờ duyệt', tone: 'warning' },
  APPROVED: { label: 'Đã duyệt', tone: 'success' },
  REJECTED: { label: 'Từ chối', tone: 'neutral' },
  DISPUTED: { label: 'Đang tranh chấp', tone: 'danger' },
  WAIVED: { label: 'Đã miễn', tone: 'neutral' },
  SUPERSEDED: { label: 'Đã điều chỉnh', tone: 'neutral' },
};

export const chargeKind: Record<ChargeKind, string> = {
  RENT: 'Tiền thuê',
  SECURITY_DEPOSIT: 'Tiền bảo đảm',
  RESERVATION_DEPOSIT: 'Tiền giữ lịch',
  DELIVERY_FEE: 'Phí giao nhận',
  CANCELLATION_FEE: 'Phí hủy',
  LATE_FEE: 'Phí trả trễ',
  REDELIVERY_FEE: 'Phí giao lại',
  CLEANING_FEE: 'Phí vệ sinh',
  DAMAGE_FEE: 'Phí hư hỏng',
  LOSS_FEE: 'Bồi thường mất món',
  OTHER_FEE: 'Phí khác',
};

export const paymentMethod: Record<PaymentMethod, string> = {
  CASH: 'Tiền mặt',
  BANK_TRANSFER: 'Chuyển khoản',
  GATEWAY: 'Cổng thanh toán',
};

export const paymentChannel: Record<PaymentChannel, string> = {
  ATM_CARD: 'Thẻ ATM nội địa',
  INTL_CARD: 'Thẻ quốc tế',
  E_WALLET: 'Ví điện tử',
  BANK_QR: 'Quét mã QR ngân hàng',
};

export const paymentPurpose: Record<PaymentPurpose, { label: string; hint: string }> = {
  RESERVATION: { label: 'Tiền giữ lịch', hint: 'Đủ để giữ lịch, phần còn lại trả trước khi nhận hàng.' },
  FULL: { label: 'Toàn bộ số còn lại', hint: 'Gồm tiền thuê, tiền bảo đảm và phí giao nhận.' },
};

export const bucket: Record<Bucket, string> = {
  RENTAL: 'Tiền thuê và phí',
  SECURITY: 'Tiền bảo đảm',
  RESERVATION: 'Tiền giữ lịch',
};

export const legMode: Record<'outbound' | 'return', Record<LegMode, string>> = {
  outbound: { STORE: 'Nhận tại cửa hàng', COURIER: 'Giao tận nơi' },
  return: { STORE: 'Trả tại cửa hàng', COURIER: 'Shipper đến lấy' },
};

export const roleLabel: Record<Role, string> = {
  customer: 'Khách hàng',
  rental_staff: 'Nhân viên thuê',
  inventory_manager: 'Quản lý kho',
  finance: 'Tài chính',
  admin: 'Quản trị viên',
};

export const allRoles: Role[] = ['customer', 'rental_staff', 'inventory_manager', 'finance', 'admin'];

export const userStatus: Record<UserStatus, StatusLabel> = {
  ACTIVE: { label: 'Đang hoạt động', tone: 'success' },
  LOCKED: { label: 'Đã khóa', tone: 'danger' },
};

export const catalogStatus: Record<CatalogStatus, StatusLabel> = {
  DRAFT: { label: 'Bản nháp', tone: 'neutral' },
  ACTIVE: { label: 'Đang bán', tone: 'success' },
  ARCHIVED: { label: 'Lưu trữ', tone: 'neutral' },
};

export const auditResult: Record<AuditResult, StatusLabel> = {
  SUCCESS: { label: 'Thành công', tone: 'success' },
  DENIED: { label: 'Bị từ chối', tone: 'danger' },
  FAILED: { label: 'Thất bại', tone: 'warning' },
};

export const supportStatus: Record<SupportStatus, StatusLabel> = {
  OPEN: { label: 'Mới', tone: 'warning' },
  IN_PROGRESS: { label: 'Đang xử lý', tone: 'info' },
  RESOLVED: { label: 'Đã giải quyết', tone: 'success' },
};

export const supportTopic: Record<SupportTopic, string> = {
  BOOKING: 'Đơn thuê',
  PAYMENT: 'Thanh toán',
  PRODUCT: 'Sản phẩm',
  DELIVERY: 'Giao nhận',
  OTHER: 'Khác',
};

/** Booking history entries; anything else falls back to the status label. */
export const transitionAction: Record<string, string> = {
  'booking.create': 'Đặt đơn',
  'booking.confirm': 'Xác nhận đơn',
  'booking.start_preparation': 'Bắt đầu chuẩn bị',
  'booking.handover': 'Bàn giao cho khách',
  'booking.return': 'Nhận trả hàng',
  'booking.inspect_asset': 'Kiểm tra món',
  'booking.complete': 'Hoàn tất đơn',
  'booking.cancel': 'Hủy đơn',
  'booking.expire': 'Hết hạn giữ hàng',
  'booking.reschedule': 'Đổi lịch',
  'booking.report_missing': 'Báo thiếu món',
  'booking.confirm_lost': 'Xác nhận mất món',
  'booking.replace_asset': 'Thay món',
  'booking.modify': 'Sửa đơn',
};

export const campaignType: Record<CampaignType, string> = {
  LINE_DISCOUNT: 'Giảm theo sản phẩm',
  COUPON: 'Mã giảm giá',
  ORDER_DISCOUNT: 'Giảm cả đơn',
  MEMBER_DISCOUNT: 'Ưu đãi thành viên',
};

export const campaignStatus: Record<CampaignStatus, StatusLabel> = {
  DRAFT: { label: 'Bản nháp', tone: 'neutral' },
  ACTIVE: { label: 'Đang chạy', tone: 'success' },
  PAUSED: { label: 'Tạm dừng', tone: 'warning' },
  ENDED: { label: 'Đã kết thúc', tone: 'neutral' },
};

export const fundsRequirement: Record<FundsRequirement, string> = {
  NONE: 'Không yêu cầu',
  RESERVATION_DEPOSIT: 'Đủ tiền giữ lịch',
  FULL_RENT: 'Đủ tiền thuê và phí giao nhận',
  FULL_RENT_AND_SECURITY: 'Đủ tiền thuê, phí giao nhận và tiền bảo đảm',
};

export const bookingActionLabel: Record<BookingAction, string> = {
  confirm: 'Xác nhận đơn',
  start_preparation: 'Bắt đầu chuẩn bị',
  handover: 'Bàn giao',
  record_return: 'Nhận trả',
  report_missing: 'Báo thiếu món',
  confirm_lost: 'Xác nhận mất',
  inspect: 'Kiểm tra món',
  propose_fee: 'Đề xuất phí',
  record_payment: 'Ghi nhận thu tiền',
  settle: 'Quyết toán',
  complete: 'Hoàn tất đơn',
  cancel: 'Hủy đơn',
  no_show: 'Ghi nhận khách không đến',
  reschedule: 'Đổi lịch',
};

/** Vietnamese names for permissions, for the matrix screen. */
export const permissionGroups: { title: string; items: Record<string, string> }[] = [
  { title: 'Danh mục và kho', items: {
    'catalog.manage': 'Quản lý danh mục sản phẩm',
    'inventory.read': 'Xem tồn kho',
    'inventory.manage': 'Quản lý tồn kho',
  } },
  { title: 'Đơn thuê', items: {
    'booking.create_own': 'Tạo đơn của mình',
    'booking.read_own': 'Xem đơn của mình',
    'booking.read_all': 'Xem mọi đơn',
    'booking.cancel_own': 'Hủy đơn của mình',
    'booking.cancel_any': 'Hủy mọi đơn',
    'booking.reschedule_own': 'Đổi lịch đơn của mình',
    'booking.reschedule_any': 'Đổi lịch mọi đơn',
    'booking.modify_own': 'Sửa đơn của mình',
    'booking.modify_any': 'Sửa mọi đơn',
    'booking.confirm': 'Xác nhận đơn',
    'booking.complete': 'Hoàn tất đơn',
  } },
  { title: 'Vận hành thuê', items: {
    'rental.prepare': 'Chuẩn bị hàng',
    'rental.handover': 'Bàn giao',
    'rental.return_receive': 'Nhận trả',
    'rental.return_inspect': 'Kiểm tra hàng trả',
    'rental.confirm_lost': 'Xác nhận mất',
  } },
  { title: 'Giao nhận', items: {
    'delivery.read_own': 'Xem giao nhận của mình',
    'delivery.read_all': 'Xem mọi giao nhận',
    'delivery.manage': 'Quản lý giao nhận',
    'complaint.create_own': 'Gửi khiếu nại giao hàng',
  } },
  { title: 'Phí phát sinh', items: {
    'fee.assess': 'Đề xuất phí',
    'fee.approve': 'Duyệt phí',
    'fee.approve_high': 'Duyệt phí vượt ngưỡng',
    'fee.waive': 'Miễn, giảm phí',
  } },
  { title: 'Thanh toán', items: {
    'payment.read_own': 'Xem thanh toán của mình',
    'payment.read': 'Xem mọi thanh toán',
    'payment.record': 'Ghi nhận thu tiền',
    'charge.create': 'Tạo khoản thu',
    'charge.reverse': 'Đảo khoản thu',
    'refund.create': 'Tạo yêu cầu hoàn',
    'refund.approve': 'Duyệt hoàn tiền',
    'deduction.create': 'Đề xuất khấu trừ cọc',
    'deduction.approve': 'Duyệt khấu trừ cọc',
  } },
  { title: 'Giá, thuế, doanh thu', items: {
    'promotion.manage': 'Quản lý khuyến mãi',
    'pricing.manual_adjust': 'Điều chỉnh giá thủ công',
    'tax.manage': 'Quản lý thuế',
    'revenue.read': 'Xem doanh thu',
  } },
  { title: 'Quản trị hệ thống', items: {
    'user.role_manage': 'Quản lý vai trò',
    'user.lock': 'Khóa tài khoản',
    'audit.read': 'Xem lịch sử thao tác',
    'policy.manage': 'Quản lý chính sách',
  } },
];
