// Vietnamese text for the backend's stable error codes. Backend messages are
// English and meant for logs; the UI always shows these.
const messages: Record<string, string> = {
  'request.network': 'Không kết nối được máy chủ. Vui lòng kiểm tra mạng và thử lại.',
  'request.rate_limited': 'Bạn thao tác quá nhanh. Vui lòng thử lại sau ít phút.',
  'request.not_found': 'Chức năng này chưa được kết nối API.',
  'request.cross_origin': 'Yêu cầu bị từ chối vì không đến từ trang hợp lệ.',
  'request.invalid_body': 'Dữ liệu gửi lên không hợp lệ.',
  'request.body_too_large': 'Dữ liệu gửi lên quá lớn.',
  'request.unsupported_media_type': 'Định dạng dữ liệu không được hỗ trợ.',
  validation_failed: 'Thông tin chưa hợp lệ. Vui lòng kiểm tra lại.',
  internal: 'Hệ thống đang gặp lỗi. Vui lòng thử lại sau.',
  'storage.not_found': 'Không tìm thấy dữ liệu.',
  'storage.duplicate': 'Dữ liệu này đã tồn tại.',
  'storage.stale_write': 'Dữ liệu vừa được người khác thay đổi. Vui lòng tải lại và thử lại.',

  'identity.unauthenticated': 'Vui lòng đăng nhập để tiếp tục.',
  'identity.invalid_credentials': 'Email hoặc mật khẩu không đúng, hoặc email chưa được xác minh.',
  'identity.invalid_login_challenge': 'Phiên xác thực đã hết hạn. Vui lòng đăng nhập lại.',
  'identity.forbidden': 'Bạn không có quyền thực hiện thao tác này.',
  'identity.invalid_email': 'Email không hợp lệ.',
  'identity.self_role_change': 'Bạn không thể tự đổi vai trò của mình.',
  'identity.admin_required': 'Chỉ quản trị viên mới được cấp hoặc thu vai trò quản trị.',
  'identity.self_lock': 'Bạn không thể tự khóa tài khoản của mình.',
  'identity.reason_required': 'Vui lòng nhập lý do.',
  'identity.last_admin': 'Hệ thống phải còn ít nhất một quản trị viên đang hoạt động.',
  'identity.mfa_required': 'Vai trò của bạn bắt buộc bật xác thực hai bước.',
  'identity.invalid_mfa_code': 'Mã xác thực không đúng hoặc đã được dùng.',
  'identity.mfa_already_enabled': 'Xác thực hai bước đã được bật.',
  'identity.mfa_not_enabled': 'Xác thực hai bước chưa được bật.',
  'identity.mfa_unavailable': 'Xác thực hai bước tạm thời không dùng được.',
  'identity.self_mfa_reset': 'Bạn không thể tự đặt lại xác thực hai bước của mình.',
  'identity.invalid_reset_token': 'Liên kết đặt lại mật khẩu không hợp lệ hoặc đã hết hạn.',
  'identity.invalid_verification_token': 'Liên kết xác minh không hợp lệ hoặc đã hết hạn.',
  'identity.invalid_permission_matrix': 'Ma trận quyền vi phạm quy tắc bắt buộc của hệ thống.',
  'identity.unknown_role': 'Vai trò không hợp lệ.',
  'identity.account_locked': 'Tài khoản đã bị khóa.',

  'rentals.store_closed': 'Cửa hàng nghỉ vào ngày đã chọn.',
  'rentals.slot_not_found': 'Khung giờ đã chọn không có vào ngày này.',
  'rentals.return_outside_slots': 'Hạn trả rơi vào ngày hoặc giờ cửa hàng không nhận trả. Hãy chọn ngày hoặc gói khác.',
  'rentals.duration_mismatch': 'Thời gian thuê phải đúng với gói đã chọn.',
  'rentals.pickup_too_soon': 'Ngày nhận quá gần. Vui lòng chọn ngày muộn hơn.',
  'rentals.pickup_too_far_ahead': 'Ngày nhận quá xa. Vui lòng chọn ngày gần hơn.',
  'rentals.mixed_packages': 'Mọi món trong một đơn phải dùng cùng gói thuê.',
  'rentals.pricing_mismatch': 'Giá đã thay đổi. Vui lòng kiểm tra lại đơn.',
  'rentals.invalid_transition': 'Đơn thuê không thể thực hiện thao tác này ở trạng thái hiện tại.',
  'rentals.hold_expired': 'Thời gian giữ hàng của đơn đã hết.',
  'rentals.staff_confirmation_required': 'Đơn này cần nhân viên xác nhận.',
  'rentals.insufficient_funds': 'Chưa nhận đủ số tiền cần thiết.',
  'rentals.checklist_incomplete': 'Vui lòng hoàn tất danh sách kiểm tra bàn giao.',
  'rentals.asset_mismatch': 'Tài sản bàn giao không khớp với tài sản đã giữ cho đơn.',
  'rentals.handover_too_late': 'Thời gian thuê của đơn đã kết thúc.',
  'rentals.cancel_window_closed': 'Đã quá hạn hủy. Đơn chỉ có thể được ghi nhận là khách không đến.',
  'rentals.reschedule_cutoff_passed': 'Đã quá hạn tự đổi lịch. Vui lòng liên hệ cửa hàng.',
  'rentals.no_show_too_early': 'Chưa đến mốc ghi nhận khách không đến.',
  'rentals.inspection_incomplete': 'Cần kiểm tra hết các món đã trả.',
  'rentals.open_fee_assessments': 'Còn khoản phí chờ quyết định trước khi hoàn tất đơn.',
  'rentals.modification_open': 'Đơn đang có yêu cầu thay đổi chưa xử lý xong.',
  'rentals.verification_window_open': 'Chưa hết thời hạn xác minh món bị thiếu.',
  'rentals.not_settled': 'Cần đối soát xong tiền thuê, tiền bảo đảm và khoản hoàn trước khi hoàn tất.',
  'rentals.reason_required': 'Vui lòng nhập lý do.',
  'rentals.fee_not_allowed': 'Kết quả kiểm tra hoặc tình trạng món không cho phép tính khoản phí này.',
  'rentals.duplicate_fee': 'Món này đã có khoản phí cho cùng thiệt hại.',
  'rentals.evidence_required': 'Khoản phí này cần có bằng chứng.',
  'rentals.new_damage_unproven': 'Món đã có lỗi lúc bàn giao. Cần xác nhận hư hỏng mới kèm bằng chứng.',
  'rentals.no_delivery_window': 'Thời gian đã chọn không khớp khung giao nhận của ngày mở cửa.',
  'rentals.delivery_not_offered': 'Hiện chưa hỗ trợ giao nhận tận nơi cho lựa chọn này.',

  'inventory.insufficient': 'Không còn đủ hàng trong thời gian đã chọn.',
  'inventory.invalid_asset_transition': 'Tài sản không thể chuyển sang trạng thái này.',
  'inventory.asset_on_rental': 'Tài sản đang ở chỗ khách, cần xử lý trên đơn thuê.',
  'inventory.reason_required': 'Vui lòng nhập lý do.',
  'inventory.checklist_incomplete': 'Vui lòng hoàn tất danh sách kiểm tra.',
  'inventory.handover_outside_block': 'Chưa đến thời gian bàn giao của đơn này.',
  'inventory.already_handed_over': 'Món này đã được bàn giao.',
  'inventory.not_handed_over': 'Món này chưa được bàn giao.',
  'inventory.asset_not_resolved': 'Món này chưa được xử lý xong trên đơn thuê.',
  'inventory.allocation_in_use': 'Món này đang được giữ cho một đơn khác.',

  'catalog.offering_unavailable': 'Sản phẩm hiện không cho thuê.',
  'catalog.not_individually_rentable': 'Món này chỉ cho thuê theo bộ.',
  'catalog.variant_unavailable': 'Size hoặc màu đã chọn không còn.',
  'catalog.package_unavailable': 'Gói thuê đã chọn không còn áp dụng.',
  'catalog.no_price_for_package': 'Sản phẩm chưa có giá cho gói thuê này.',
  'catalog.invalid_selection': 'Lựa chọn size của bộ chưa hợp lệ.',

  'payments.invalid_amount': 'Số tiền không hợp lệ.',
  'payments.exceeds_outstanding': 'Số tiền vượt quá khoản còn phải thu.',
  'payments.exceeds_refundable': 'Số tiền vượt quá khoản được hoàn.',
  'payments.invalid_checkout_url': 'Không mở được trang thanh toán. Vui lòng thử lại.',
  'payments.idempotency_conflict': 'Yêu cầu thanh toán đã thay đổi. Vui lòng thử lại.',
  'payments.approver_must_differ': 'Khoản này cần người khác người tạo duyệt.',
  'payments.gateway_unavailable': 'Hiện chưa hỗ trợ thanh toán trực tuyến. Vui lòng thanh toán tại quầy hoặc chuyển khoản.',
  'payments.invalid_transition': 'Khoản tiền này không thể thực hiện thao tác đó.',
  'payments.reason_required': 'Vui lòng nhập lý do.',
  'payments.already_cancelled': 'Đơn đã được quyết toán hủy.',
  'payments.charge_has_deductions': 'Khoản này đã được khấu trừ nên không thể đảo.',

  'promotions.coupon_not_found': 'Mã giảm giá không hợp lệ.',
  'promotions.coupon_inactive': 'Mã giảm giá chưa đến hạn hoặc đã hết hạn.',
  'promotions.coupon_ineligible': 'Đơn thuê chưa đủ điều kiện dùng mã này.',
  'promotions.usage_limit_reached': 'Mã giảm giá đã hết lượt dùng.',
  'promotions.not_combinable': 'Các ưu đãi này không dùng chung được.',
  'promotions.campaign_invalid': 'Thông tin khuyến mãi chưa hợp lệ. Vui lòng kiểm tra mã, giá trị và thời gian hiệu lực.',

  'support.resolved': 'Yêu cầu này đã được giải quyết.',

  'delivery.out_of_service_area': 'Địa chỉ nằm ngoài khu vực giao nhận.',
  'delivery.invalid_address': 'Địa chỉ chưa đầy đủ hoặc không hợp lệ.',
};

export function messageForCode(code: string): string {
  return messages[code] ?? 'Không thể thực hiện thao tác. Vui lòng thử lại.';
}

/** Translates a backend field reason, keeping numbers that come from config. */
export function fieldReason(reason: string): string {
  const between = /^must be (\d+) to (\d+) characters$/.exec(reason);
  if (between) return `Cần từ ${between[1]} đến ${between[2]} ký tự.`;
  const atMost = /at most (\d+) characters$/.exec(reason);
  if (atMost) return `Nhập tối đa ${atMost[1]} ký tự.`;
  switch (reason) {
    case 'required': return 'Không được để trống.';
    case 'is incorrect': return 'Không chính xác.';
    case 'must be a valid email address': return 'Email không hợp lệ.';
    case 'must be a phone number': return 'Số điện thoại không hợp lệ.';
    case 'must be a date (YYYY-MM-DD)': return 'Ngày không hợp lệ.';
    default: return 'Giá trị chưa hợp lệ.';
  }
}
