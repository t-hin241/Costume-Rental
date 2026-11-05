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
| `GET /catalog/offerings` | `q, category, kind, size, color, sort, page`; trả thêm `facets`. Tìm kiếm không phân biệt dấu. Mỗi mẫu có `categories`, danh sách có thứ tự |
| `GET /catalog/offerings/{kind}/{slug}` | Chi tiết, variant, giá theo gói, thành phần bundle |
| `GET /store/info` | Lấy từ biến môi trường `SHOP_*` |
| `GET /store/calendar?from&to&outbound` | Tối đa 62 ngày. `min_date` là ngày đầu tiên còn slot đặt được. `outbound=COURIER` trả các khung giờ giao thay cho slot quầy (thời gian thuê bắt đầu ở cuối khung); chưa nạp khu vực giao nhận thì không có khung nào |
| `GET /store/fulfillment-options` | `STORE`, cộng `COURIER` khi đã nạp khu vực giao nhận (D43). `zones` liệt kê khu vực đang phục vụ với `wards` (`code`, `name`) và phí `outbound_fee`, `return_fee`, `redelivery_fee` |
| `GET /policies/public` | Phần chính sách khách được xem |
| `GET /media/{name}` | Ảnh đã tải lên, cache lâu dài vì tên ngẫu nhiên và file không đổi |
| `POST /availability` | Kiểm tra tham khảo, có giới hạn tần suất theo IP |
| `POST /quotes` | Báo giá đủ các khoản, có giới hạn tần suất theo IP |

`POST /bookings`, `/availability`, `/quotes` nhận `address` (`recipient_name`, `phone`, `line`, `ward_code`) khi có chiều `COURIER`; thiếu thì `400`, phường ngoài khu vực thì `400 delivery.out_of_service_area`, không có ngoại lệ (D42). Tỉnh lấy theo khu vực của phường.

`Quote` trả riêng tiền thuê, giảm giá, tiền giữ lịch và cách xử lý, tiền bảo đảm, phí từng chiều, `due_to_confirm` (số phải trả để giữ lịch), `due_before_handover` (tổng trước khi nhận hàng), `refundable_total`, `hold_minutes`, `policy_version`.

### Khách hàng

| Phương thức, đường dẫn | Quyền |
|---|---|
| `POST /bookings` (bắt buộc `request_key`, tối đa 64 ký tự) | `booking.create_own`. Gửi lại cùng key, kể cả đồng thời, trả về đơn đã tạo. Đã có `max_unpaid_holds` đơn đang giữ hàng chưa thu đồng nào thì trả `409 rentals.too_many_unpaid_holds`; thanh toán, hủy hoặc hết hạn giữ hàng thì đơn đó thôi được tính |
| `GET /me/bookings?scope=active\|history` | `booking.read_own` |
| `GET /bookings/{id}` | own hoặc `booking.read_all` |
| `GET /bookings/{id}/cancellation-preview?reason=` | own hoặc `booking.cancel_any`. Khách luôn thấy điều khoản của khách; nhân viên truyền `NO_SHOW` hoặc `STAFF_DECISION`. Chạy đúng luật hủy thật, không lưu gì. Thời gian báo trước tính đến ngày nhận sớm nhất đơn từng có, không phải `pickup_at` hiện tại |
| `POST /bookings/{id}/cancel` | `booking.cancel_own`/`_any` |
| `POST /bookings/{id}/reschedule` | `booking.reschedule_own`/`_any`. Không tính phí và không làm giảm phí hủy về sau |
| `GET /bookings/{id}/deliveries` | own (`delivery.read_own`) hoặc `delivery.read_all`. Mỗi vận đơn: chiều, trạng thái, khung giờ, mã vận đơn, địa chỉ. Khách không nhận các trường xử lý nội bộ (`attempts`, `actions` rỗng). Nhân viên thấy đủ địa chỉ và SĐT người nhận theo cùng quy tắc D40, ngoài ra chỉ thấy tên, phường và 3 số cuối (D43) |
| `GET`/`PUT /me/profile` | Đăng nhập |
| `GET`/`POST /me/support-requests` | Đăng nhập. `booking_code` phải là đơn của chính khách |

### Vận hành

| Phương thức, đường dẫn | Quyền |
|---|---|
| `GET /bookings?q&status&from&to&page` | `booking.read_all`. Tìm theo SĐT chỉ ra đơn chưa kết thúc hoặc kết thúc chưa quá 30 ngày (trừ admin). Mọi tóm tắt đơn (`customer.phone`) chỉ hiện 3 số cuối, dạng `***567`, khi đơn đã kết thúc quá 30 ngày và tiền đã quyết toán; khách và admin luôn thấy đủ số (D40) |
| `POST /bookings/{id}/confirm` | `booking.confirm` |
| `POST /bookings/{id}/start-preparation` | `rental.prepare` |
| `POST /bookings/{id}/handover` | `rental.handover` |
| `POST /bookings/{id}/returns` | `rental.return_receive` |
| `POST /bookings/{id}/report-missing` | `rental.return_receive` |
| `POST /bookings/{id}/confirm-lost` | `rental.confirm_lost` |
| `POST /bookings/{id}/inspections` | `rental.return_inspect`. Người không có quyền đọc đơn (quản lý kho) nhận `204`, không có body |
| `POST /bookings/{id}/fees` | `fee.assess` |
| `POST /bookings/{id}/fees/{fee}/decision` | `fee.approve`, `fee.waive` |
| `POST /bookings/{id}/payments` (có `idempotency_key`, tối đa 64 ký tự) | `payment.record` |
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
| `GET /support-requests`, `POST /support-requests/{id}/replies` | `support.handle` |
| `GET /delivery/tasks?open&page` | `delivery.read_all`. Vận đơn theo khung giờ; `open=true` (mặc định) chỉ còn vận đơn cần xử lý. Mỗi vận đơn có `actions` (`RECORD_SHIPMENT`, `MARK_READY`, `RECORD_EVENT`, `CLASSIFY_ATTEMPT`, `CLOSE`, `RECEIVE`, `CONFIRM_CANCELLED`), `awaiting_follow_up` (hãng đã ngừng thử) và `can_collect_at_store` |
| `POST /delivery/tasks/{id}/shipment` `{ shipment_code }` | `delivery.manage`. Chế độ thủ công (D43): nhân viên tạo vận đơn trên ứng dụng của hãng rồi nhập mã. Chiều giao chỉ khi đơn đã xác nhận |
| `POST /delivery/tasks/{id}/ready` | `delivery.manage`. Kiện đã đóng gói |
| `POST /delivery/tasks/{id}/events` `{ status, fault, gave_up, reason, at }` | `delivery.manage`. Chiều giao: `IN_TRANSIT`, `DELIVERED`, `DELIVERY_FAILED`; chiều về: `PICKUP_CONFIRMED`, `PICKUP_FAILED`, `IN_TRANSIT`. `DELIVERED` bắt đầu thời gian dùng của khách và có thể lùi hạn trả (kéo dài lịch giữ hàng nếu còn trống); `PICKUP_CONFIRMED` chấm dứt nghĩa vụ trả của khách. Lần thất bại có `gave_up` thì vận đơn chờ nhân viên chọn hướng xử lý (D42). `at` không được ở tương lai |
| `POST /delivery/tasks/{id}/classify` `{ attempt_id, fault, reason }` | `delivery.manage`. `fault` là `CUSTOMER`, `STORE` hoặc `CARRIER`; phải phân loại lần thất bại cuối trước khi đóng (`409 delivery.attempt_unclassified`) |
| `POST /delivery/tasks/{id}/close` `{ follow_up, reason, date, window_id }` | `delivery.manage`. Chỉ khi hãng đã ngừng thử. `NEW_SHIPMENT` tạo vận đơn mới miễn phí trong khung `window_id` của ngày `date`; `AT_STORE` chờ khách đến quầy; `END_BOOKING` (chiều giao) kết thúc đơn không giao được: lỗi khách giữ 70% tiền thuê và phí chiều giao, lỗi cửa hàng/hãng hoàn hết, hàng về kiểm tra |
| `POST /delivery/tasks/{id}/collected` | `delivery.manage`. Khách nhận tại quầy kiện hãng không giao được |
| `POST /delivery/tasks/{id}/received` | `delivery.manage`. Kiện chiều về đã đến cửa hàng: tài sản sang kiểm tra, tính phí trễ như trả tại quầy |
| `POST /delivery/tasks/{id}/cancellation-confirmed` | `delivery.manage`. Hãng đã hủy vận đơn mà cửa hàng yêu cầu hủy khi đơn bị hủy |

Đơn giao tận nơi: `POST /bookings/{id}/handover` là lúc giao kiện cho shipper và cần vận đơn chiều giao đã có mã (`409 delivery.shipment_required`). Phí giao lại: `POST /bookings/{id}/fees` với `kind: REDELIVERY`, `task_id`, `attempt_id`; số tiền lấy theo khu vực của đơn, tối đa một khoản mỗi vận đơn.

### Quản trị

| Phương thức, đường dẫn | Quyền |
|---|---|
| `GET /admin/dashboard?range_days=` | `revenue.read` hoặc `policy.manage` |
| `GET /admin/users?q&role&page` | `user.role_manage` |
| `GET /admin/profile-retention?page&page_size` | `customer.profile_erase`. Hồ sơ khách của tài khoản không hoạt động 24 tháng, cũ nhất trước, kèm `blockers` (`OPEN_BOOKING`, `MONEY_OPEN`, `OPEN_FEE`, `OPEN_SUPPORT`); rỗng thì xóa được (D39) |
| `POST /admin/users/{id}/profile-erase` (bắt buộc `reason`) | `customer.profile_erase`. Xóa họ tên và SĐT, trả `204`; còn `blockers` hoặc chưa đủ 24 tháng trả `409 retention.profile_kept`; không có hồ sơ trả `404` |
| `GET /admin/audit-events?q&result&page` | `audit.read` |
| `GET`/`POST`/`PUT /admin/catalog/items`, `/bundles` | `catalog.manage`. `category_ids` là danh sách có thứ tự, tối đa 10, danh mục đầu tiên hiện trên thẻ sản phẩm |
| `POST /admin/media/images` | `catalog.manage`, 300 lần/giờ/người. Body là chính file ảnh (không multipart). Trả `201 { url, content_type, size }`; đưa `url` vào `images` của món hoặc bộ. Nội dung file quyết định định dạng: chỉ nhận JPEG, PNG, WebP, lỗi `media.unsupported_image`; quá `MEDIA_MAX_BYTES` hoặc quá 50 triệu điểm ảnh trả `media.file_too_large`. Ảnh tải lên nhưng không được lưu vào sản phẩm vẫn nằm trên ổ đĩa |
| `POST`/`PUT /admin/catalog/categories`, `/packages` | `catalog.manage` |
| `GET /admin/policies/active`, `POST /admin/policies` | `policy.manage`. Luật không có trên màn hình (phí chủ động hủy, giao nhận) được giữ nguyên khi xuất bản |
| `GET`/`POST`/`PUT /admin/promotions` | `promotion.manage`. `valid_until` là ngày cuối còn hiệu lực |
| `GET /admin/tax/profiles` | `revenue.read` hoặc `tax.manage`. Lịch sử hồ sơ thuế, cũ nhất trước (D41) |
| `POST /admin/tax/profiles` | `tax.manage`. `{ version, annual_revenue_threshold, threshold_effective_from, warning_percents, effective_from }`; mốc tăng dần trong 1..99. Phiên bản đầu được bắt đầu trong quá khứ; sau đó phải từ hôm nay (`409 tax.profile_retroactive`); tự đóng phiên bản đang mở. Trả `201` |
| `GET /admin/revenue/year?year=` | `revenue.read`. Mặc định năm hiện tại. `has_profile: false` khi chưa có hồ sơ; `level` `BELOW`/`WARNING`/`EXCEEDED`, `mark` là mốc cao nhất đã chạm, `percent` làm tròn xuống, `pending_bookings` là số đơn đã kết thúc chưa ghi nhận |
| `GET /admin/revenue/report?from&to&group=` | `revenue.read`. `group` `day` (tối đa 366 ngày), `month`, `quarter`, `year`; mỗi dòng là kỳ (ngày đầu kỳ) × nhóm, có `list`, `discount`, `net` |
| `GET /admin/revenue/entries?from&to&category&source&booking&page` | `revenue.read`. Bút toán mới nhất trước; `booking` là mã đơn chính xác |
| `GET /admin/revenue/entries.csv?from&to&category&source&booking` | `revenue.read`. CSV UTF-8 có BOM, tối đa 50.000 dòng (`400 tax.export_too_large`), mỗi lần xuất ghi audit |
| `POST /admin/revenue/entries` | `revenue.record`. Doanh thu ngoài website `{ category, amount, date, channel, note, document_ref }`; ngày tương lai `400 tax.entry_future`, nhóm tiền cọc `400 tax.not_revenue`, chưa có hồ sơ `404 tax.no_profile`. Trả `201` |
| `POST /admin/revenue/entries/{id}/reverse` (bắt buộc `reason`) | `revenue.record`. Chỉ bút toán tay: bút toán đơn thuê `409 tax.entry_not_manual`, đã đảo `409 tax.entry_reversed`. Trả `201` bút toán đảo |
| `GET /admin/finance/cash-flow?from&to` | `revenue.read`. `received`, `refunded` theo phương thức và khoản trong kỳ; `position` là số dư hiện tại trên mọi đơn |
| `GET /admin/finance/reconciliation?from&to` | `revenue.read`. Tối đa 366 ngày. Mỗi mục gồm `items` (tối đa 50) và `total`: `mismatched` (`NOT_RECOGNISED`, `OWED`, `OVERPAID`), `unreconciled`, `attention`, `unrecognised`, `other_charges` |
| `GET /admin/finance/transactions?from&to&kind&reconciled&page` | `revenue.read`. Khoản thu và hoàn đã thành công theo lúc tiền đi; `kind` `PAYMENT`/`REFUND`, `reconciled` `true`/`false` |
| `POST /admin/finance/transactions/{kind}/{id}/reconcile` | `payment.reconcile`. `kind` là `payment` hoặc `refund`; `{ statement_ref, note }`. Chưa thành công `409 payments.not_reconcilable`, đã đối soát `409 payments.already_reconciled`. Trả `201` |
| `POST /admin/finance/reconciliations/{id}/revoke` (bắt buộc `reason`) | `payment.reconcile`. Trả `204`; giao dịch trở lại danh sách chưa đối soát |

## 3. Chưa chạy

| Phần | Hành vi hiện tại | Cần gì để chạy |
|---|---|---|
| Thanh toán trực tuyến | `online_payment_available: false`, `payment_options` rỗng. `POST /bookings/{id}/payment-sessions` và `GET /payment-sessions/{id}` trả `409 payments.gateway_unavailable`. Khách thanh toán tại quầy hoặc chuyển khoản, nhân viên ghi nhận | Chọn nhà cung cấp cổng, viết adapter và webhook (mục 4) |
| Giao nhận tận nơi | Chạy ở chế độ thủ công (D43) khi đã nạp `config/delivery.yaml`; file hiện còn `???` (mã phường, địa chỉ cửa hàng) nên chưa bật. Chưa có webhook, chưa có khiếu nại sau khi nhận (D20) và đối soát cước | Điền cấu hình rồi chạy `cmd/policy -delivery`; adapter GHN và webhook khi có tài khoản sandbox |
| Ưu đãi thành viên | Form không cho tạo `MEMBER_DISCOUNT` vì cần phân khúc khách | Định nghĩa phân khúc |

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

Công thức đã chốt (D37). Con số để quản trị xem, không dùng cho khai thuế. Ngưỡng doanh thu năm lấy từ sổ doanh thu (`GET /admin/revenue/year`, D41), không từ con số này.

## 6. Cảnh báo vận hành

Không có khái niệm "nguy cơ thiếu hàng" riêng (chủ sản phẩm chốt 2026-10-31): `POST /availability` chỉ tính asset `READY` là còn hàng (D33), và lúc đặt hệ thống từ chối khi không đủ tài sản. Màn hình cảnh báo gồm đơn trả trễ và đơn bị ảnh hưởng (`OverdueConflicts`), theo yêu cầu cảnh báo xung đột trả trễ trong `AGENTS.md`. `counters.conflicts` là số đơn bị ảnh hưởng.
