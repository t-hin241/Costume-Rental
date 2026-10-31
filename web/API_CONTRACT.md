# Hợp đồng API cho giao diện Webee Rental

Tài liệu này mô tả API mà giao diện đang dùng và phần còn chờ tích hợp bên ngoài. Kiểu dữ liệu chi tiết nằm ở [src/lib/api/types.ts](src/lib/api/types.ts), danh sách lời gọi ở [src/lib/api/endpoints.ts](src/lib/api/endpoints.ts). Mock trong `src/mocks/` chỉ minh họa hợp đồng, không phải chính sách.

Trạng thái ngày 2026-10-31: backend phục vụ mọi endpoint trong mục 2. Riêng thanh toán trực tuyến và giao nhận tận nơi chưa chạy, xem mục 3.

## 1. Quy ước chung

- Gốc `/api/v1`, xác thực bằng cookie session, CSRF và CORS như `httpapi`.
- Lỗi theo định dạng `{ error: { code, message, fields }, request_id }`. Giao diện hiển thị theo `code`.
- Tiền: `{ "amount": 450000, "currency": "VND" }`, số nguyên. Giao diện không cộng trừ tiền; mọi tổng do backend trả.
- Thời điểm: RFC 3339 UTC. Ngày nghiệp vụ: `YYYY-MM-DD` theo `Asia/Ho_Chi_Minh`.
- Danh sách: `?page=&page_size=`, mặc định 20, tối đa 100, trả `{ items, page, page_size, total }`.
- Enum giữ nguyên giá trị trong package Go (`PENDING_CONFIRMATION`, `NEEDS_INSPECTION`, ...).
- Body có field lạ bị từ chối, nên client không thể tự gửi `customer_id`, giá hay trạng thái.

### Trường `actions`

`BookingDetail.actions`, `Asset.actions`, `FinanceRefund.actions`, `FeeAssessment.actions` liệt kê thao tác người gọi được làm **lúc này**, do backend tính từ trạng thái, quyền và ownership. Giao diện chỉ hiện nút theo danh sách này. Backend vẫn kiểm tra lại khi nhận lệnh.

## 2. Endpoint

Quyền là quyền trong `identity`. "own" là chủ đơn. Truy cập đơn của người khác trả `403 identity.forbidden`.

### Xác thực và tài khoản

| Phương thức, đường dẫn | Màn hình |
|---|---|
| Các route `/auth/*` | Đăng nhập, đăng ký, xác minh email, quên mật khẩu, MFA, bảo mật |
| `GET /admin/users/{id}`, `PUT`/`DELETE /admin/users/{id}/roles/{role}` | Người dùng |
| `POST /admin/users/{id}/lock`, `/unlock`, `/mfa-reset` | Người dùng |
| `GET`/`POST /admin/permission-matrix` | Phân quyền |

### Công khai

| Phương thức, đường dẫn | Mô tả |
|---|---|
| `GET /catalog/categories` | Danh mục kèm số mẫu |
| `GET /catalog/packages` | Gói đang bán; người có `catalog.manage` thấy cả gói ngừng dùng |
| `GET /catalog/offerings` | `q, category, kind, size, color, sort, page`; trả thêm `facets`. Tìm kiếm không phân biệt dấu |
| `GET /catalog/offerings/{kind}/{slug}` | Chi tiết, variant, giá theo gói, thành phần bundle |
| `GET /store/info` | Lấy từ biến môi trường `SHOP_*` |
| `GET /store/calendar?from&to` | Tối đa 62 ngày. `min_date` là ngày đầu tiên còn slot đặt được |
| `GET /store/fulfillment-options` | Hiện chỉ `STORE` |
| `GET /policies/public` | Phần chính sách khách được xem |
| `POST /availability` | Kiểm tra tham khảo, có giới hạn tần suất theo IP |
| `POST /quotes` | Báo giá đủ các khoản, có giới hạn tần suất theo IP |

`Quote` trả riêng tiền thuê, giảm giá, tiền giữ lịch và cách xử lý, tiền bảo đảm, phí từng chiều, `due_to_confirm` (số phải trả để giữ lịch), `due_before_handover` (tổng trước khi nhận hàng), `refundable_total`, `hold_minutes`, `policy_version`.

### Khách hàng

| Phương thức, đường dẫn | Quyền |
|---|---|
| `POST /bookings` (bắt buộc `request_key`) | `booking.create_own` |
| `GET /me/bookings?scope=active\|history` | `booking.read_own` |
| `GET /bookings/{id}` | own hoặc `booking.read_all` |
| `GET /bookings/{id}/cancellation-preview?reason=` | own hoặc `booking.cancel_any`. Khách luôn thấy điều khoản của khách; nhân viên truyền `NO_SHOW` hoặc `STAFF_DECISION`. Chạy đúng luật hủy thật, không lưu gì |
| `POST /bookings/{id}/cancel` | `booking.cancel_own`/`_any` |
| `POST /bookings/{id}/reschedule` | `booking.reschedule_own`/`_any` |
| `GET`/`PUT /me/profile` | Đăng nhập |
| `GET`/`POST /me/support-requests` | Đăng nhập. `booking_code` phải là đơn của chính khách |

### Vận hành

| Phương thức, đường dẫn | Quyền |
|---|---|
| `GET /bookings?q&status&from&to&page` | `booking.read_all` |
| `POST /bookings/{id}/confirm` | `booking.confirm` |
| `POST /bookings/{id}/start-preparation` | `rental.prepare` |
| `POST /bookings/{id}/handover` | `rental.handover` |
| `POST /bookings/{id}/returns` | `rental.return_receive` |
| `POST /bookings/{id}/report-missing` | `rental.return_receive` |
| `POST /bookings/{id}/confirm-lost` | `rental.confirm_lost` |
| `POST /bookings/{id}/inspections` | `rental.return_inspect` |
| `POST /bookings/{id}/fees` | `fee.assess` |
| `POST /bookings/{id}/fees/{fee}/decision` | `fee.approve`, `fee.waive` |
| `POST /bookings/{id}/payments` (có `idempotency_key`) | `payment.record` |
| `POST /bookings/{id}/refunds/{refund}/approve`, `/reject` | `refund.approve` |
| `POST /bookings/{id}/refunds/{refund}/payout` | `payment.record` |
| `POST /bookings/{id}/settle` | `refund.create` |
| `POST /bookings/{id}/complete` | `booking.complete` |
| `GET /ops/overview` | `booking.read_all` |
| `GET /ops/schedule?from&to` | `booking.read_all`. `to` là ngày cuối, được tính. Đơn chưa giao hiện `HANDOVER`, đơn đang thuê hiện `RETURN` |
| `GET /ops/alerts` | `booking.read_all` hoặc `inventory.read` |
| `GET /inventory/assets?q&status&page` | `inventory.read` |
| `GET /inventory/assets/{id}/history` | `inventory.read` |
| `POST /inventory/assets/{id}/status` | Theo `actionPermissions` |
| `POST /inventory/assets` | `inventory.manage`. `variant_id` nhận ID hoặc SKU |
| `GET /support-requests`, `POST /support-requests/{id}/replies` | `booking.read_all` (giả định A40) |

### Quản trị

| Phương thức, đường dẫn | Quyền |
|---|---|
| `GET /admin/dashboard?range_days=` | `revenue.read` hoặc `policy.manage` |
| `GET /admin/users?q&role&page` | `user.role_manage` |
| `GET /admin/audit-events?q&result&page` | `audit.read` |
| `GET`/`POST`/`PUT /admin/catalog/items`, `/bundles` | `catalog.manage` |
| `POST`/`PUT /admin/catalog/categories`, `/packages` | `catalog.manage` |
| `GET /admin/policies/active`, `POST /admin/policies` | `policy.manage`. Luật không có trên màn hình (phí chủ động hủy, giao nhận) được giữ nguyên khi xuất bản |
| `GET`/`POST`/`PUT /admin/promotions` | `promotion.manage`. `valid_until` là ngày cuối còn hiệu lực |

## 3. Chưa chạy

| Phần | Hành vi hiện tại | Cần gì để chạy |
|---|---|---|
| Thanh toán trực tuyến | `online_payment_available: false`, `payment_options` rỗng. `POST /bookings/{id}/payment-sessions` và `GET /payment-sessions/{id}` trả `409 payments.gateway_unavailable`. Khách thanh toán tại quầy hoặc chuyển khoản, nhân viên ghi nhận | Chọn nhà cung cấp cổng, viết adapter và webhook (mục 4) |
| Giao nhận tận nơi | Chỉ trả `STORE`; đặt chiều `COURIER` bị từ chối | Nối `delivery` vào `PlaceBooking`, adapter carrier, khu vực phục vụ |
| Upload ảnh | `images` là đường dẫn `https` hoặc đường dẫn trong site | Chốt nơi lưu và giới hạn dung lượng |
| Ưu đãi thành viên | Form không cho tạo `MEMBER_DISCOUNT` vì cần phân khúc khách | Định nghĩa phân khúc |
| Thuế | `tax` trong `config/policy.yaml` không được nạp, doanh thu dashboard không đi qua `tax.RecognizeLedger` | Kế toán chốt A33 |

## 4. Thanh toán trực tuyến

Giao diện đã làm theo mô hình hosted checkout; nhà cung cấp cổng chưa được chọn. Trong `dev:mock`, trang của cổng được thay bằng `src/mocks/MockGateway.tsx`, không có ô nhập thẻ và không vào bản production.

1. `GET /bookings/{id}` trả `payment_options`: các số tiền khách được trả online lúc này, do backend tính. Rỗng thì giao diện không hiện phần thanh toán.
2. `POST /bookings/{id}/payment-sessions` nhận `purpose`, `channel`, `idempotency_key`, **không nhận số tiền**. Backend tính số tiền, tạo payment `PENDING` và trả `checkout_url`.
3. Trình duyệt chuyển sang `checkout_url` (chỉ chấp nhận `http`, `https`). Khách trả tiền trên trang của cổng.
4. Cổng đưa khách về `/payment/return?session={id}`. Trang này gọi `GET /payment-sessions/{id}` mỗi 2 giây đến khi hết `PENDING`, và bỏ qua mọi tham số kết quả trên URL.
5. Kết quả thật đến qua webhook của cổng: verify chữ ký, chống replay, rồi `ApplyPaymentEvent` kiểm tra reference, số tiền, currency. Sau đó gọi `autoConfirm` như khi thu tại quầy.

Trạng thái session dùng `PaymentStatus` hiện có. Khoản `PENDING` được tính vào số đã thu tạm để chống trả trùng, nên gọi lại với cùng `purpose` khi đang có session mở phải trả session đó.

`GET /store/info` bật tính năng bằng `online_payment_available`, kèm `payment_channels` (`ATM_CARD`, `INTL_CARD`, `E_WALLET`, `BANK_QR`). Khi `false`, giao diện quay về hướng dẫn tại cửa hàng theo `payment_instructions`.

## 5. Cách tính trên dashboard

- **Doanh thu ghi nhận**: các khoản thu chưa đảo, trừ tiền bảo đảm và tiền giữ lịch, của đơn đã hoàn tất hoặc đã quyết toán hủy, tính theo ngày nghiệp vụ.
- **Tỷ lệ tài sản đang thuê**: số asset `RENTED` trên số asset còn phục vụ, không tính `RETIRED`.
- **Khách có đơn**: số khách khác nhau có đơn tạo trong kỳ.

Đây là giả định A41, chờ chủ sản phẩm chốt.

## 6. Cảnh báo vận hành

Không có khái niệm "nguy cơ thiếu hàng" riêng (chủ sản phẩm chốt 2026-10-31): `POST /availability` chỉ tính asset `READY` là còn hàng (D33), và lúc đặt hệ thống từ chối khi không đủ tài sản. Màn hình cảnh báo gồm đơn trả trễ và đơn bị ảnh hưởng (`OverdueConflicts`), theo yêu cầu cảnh báo xung đột trả trễ trong `AGENTS.md`. `counters.conflicts` là số đơn bị ảnh hưởng.
