# Agents Guide: Costume Rental Website

> File này là guardrail chung cho mọi coding agent. Đọc `about.md` để hiểu domain, ví dụ và các quyết định nghiệp vụ chưa chốt. Không duy trì bộ quy tắc riêng theo từng công cụ.

## Project Context

- Website cho thuê costume/quần áo bằng Go; MVP ưu tiên một cửa hàng, hỗ trợ thuê nguyên bộ hoặc từng món. Luồng chính: catalog → availability → booking → thu tiền/cọc → bàn giao → nhận trả/kiểm tra → đối soát/hoàn tiền.
- Ưu tiên vận hành thật, chống đặt trùng, đúng tài chính và bảo vệ dữ liệu; chưa thêm multi-vendor, multi-tenant, nhiều chi nhánh hoặc microservices nếu chưa có nhu cầu rõ.
- Mặc định VND và `Asia/Ho_Chi_Minh`. Mọi chính sách thương mại chưa được duyệt phải được đánh dấu là giả định/cấu hình, không âm thầm hard-code.

## Context Forwarding

- Trước khi sửa code, đọc file này, `about.md`, tài liệu liên quan trong `docs/`, cấu trúc repo, migration và test. Đọc mọi `AGENTS.md` áp dụng trong thư mục đích.
- Ưu tiên hướng dẫn gần code nhất, nhưng file con không được phá guardrail bảo mật, booking, inventory, payment hay production của root. Khi xung đột, nêu rõ và chọn phương án an toàn; hỏi chủ sản phẩm trước thay đổi nghiệp vụ/dữ liệu quan trọng.
- Khi giao việc cho agent con, chuyển tiếp đúng quy tắc liên quan. Nếu cần `CLAUDE.md`, chỉ để `@AGENTS.md`; không fork chính sách theo tool.

## Working Principles

- Kiểm tra kiến trúc hiện tại trước; ưu tiên thay đổi nhỏ, rõ, dễ review/test thay vì viết lại hoặc thêm abstraction/dependency không cần thiết.
- Business rules chỉ có một nguồn triển khai ở backend/domain; frontend không quyết định giá, quyền, trạng thái hay availability. Handler/API/job không được duplicate logic.
- Đặt tên theo domain; tránh package `utils` mơ hồ. Không dùng `panic` cho lỗi nghiệp vụ hoặc nuốt lỗi repository/provider/transaction/job; wrap lỗi đủ ngữ cảnh.
- Không bịa schema, endpoint, quyền, trạng thái, policy hay kết quả kiểm thử. Khi chưa rõ, đọc docs; nếu vẫn thiếu, ghi rõ giả định/rủi ro và không biến ví dụ trong `about.md` thành policy đã duyệt.
- Bàn giao phải nêu thay đổi, tác động nghiệp vụ, migration/config, các lệnh test đã chạy và kết quả thật, cùng phần chưa kiểm chứng.

## Architecture Standards

- Mặc định **modular monolith** cho MVP; chia module theo domain (identity, catalog, inventory, rentals, payments, audit, notifications). Chỉ tách service khi có bounded context và lý do vận hành/ownership dữ liệu rõ ràng.
- Handler parse/validate request và trả response; use case/service điều phối domain và transaction; repository truy cập dữ liệu; adapter bọc provider/tích hợp ngoài. Domain không phụ thuộc HTTP framework, driver DB hay SDK payment.
- Transaction phải bao trọn nghiệp vụ cần thành công/thất bại cùng nhau, nhất là tạo booking + snapshot + allocation; đổi lịch + allocation; và ghi nhận tài chính.

## Backend Defaults And Boundaries

- Dùng Go; nếu repo chưa chọn stack, ưu tiên PostgreSQL cho dữ liệu giao dịch. Tôn trọng framework, ORM/driver, migration tool và frontend hiện có; không tự đổi stack nếu thiếu lý do.
- Dùng `context.Context`, timeout cho I/O/provider/job và graceful shutdown. Log có cấu trúc với request/correlation ID; không ghi secret hoặc PII không cần thiết.
- Tiền lưu bằng số nguyên VND (`int64` khi phù hợp), không dùng float; payment phải có currency rõ. Lưu timestamp rõ nghĩa, dùng UTC hoặc kiểu phù hợp ở DB và diễn giải nghiệp vụ theo `Asia/Ho_Chi_Minh`.
- Config lấy từ environment/secret manager; thêm biến thì cập nhật `.env.example` bằng placeholder giả. Migration phải version hóa; không sửa migration đã chạy ở môi trường dùng chung/production.
- Ranh giới domain: Identity (user/session/role); Catalog (product/variant/bundle/pricing); Inventory (asset/stock/allocation/movement/cleaning/repair); Rentals (booking/lifecycle); Payments (thu/cọc/fee/refund/webhook); Audit/Notification (lịch sử và thông báo).
- Catalog không tự kết luận payment thành công; Payment không tự bỏ qua điều kiện booking; Notification không đổi trạng thái booking/payment; admin không bypass domain rules nếu không có use case và audit rõ.

## Identity And Access

- Role MVP: `customer`, `rental_staff`, `inventory_manager`, `finance`, `admin/owner`; guest chỉ xem dữ liệu công khai. Có thể cấp nhiều role cho một người, nhưng permission phải tách theo hành động.
- Dùng RBAC + ownership/object-level authorization, mặc định deny. Kiểm tra quyền ở cả endpoint và use case; ẩn nút ở frontend không phải bảo mật.
- Customer chỉ truy cập dữ liệu của mình. Không tin `user_id`, `owner_id`, `role`, `is_admin`, giá hoặc trạng thái tài chính do client gửi; derive identity từ session/token đã xác thực và dùng request DTO allowlist.
- Tách quyền xác nhận booking, bàn giao/nhận trả, chỉnh kho, xem giao dịch, tạo/duyệt refund, quản lý role và xem audit. Tác vụ nhạy cảm/giá trị lớn cần hạn mức hoặc phê duyệt độc lập khi phù hợp.
- Thay đổi role/credential, khóa tài khoản hoặc logout phải revoke/refresh session phù hợp và ghi audit.

## Catalog, Bundle And Inventory

- Phân biệt offering khách đặt, SKU/variant (size/màu), asset/unit vật lý và bundle component. Variant không thể thay thế phải có nguồn tồn riêng.
- Mặc định bundle dùng chung tồn kho với món lẻ, không phải stock cộng thêm. Booking bundle phải allocate đủ từng component/variant; booking món lẻ dùng đúng nguồn hàng đó. Không đếm một asset hai lần. Bundle là asset độc lập chỉ khi mô hình được quy định rõ để tránh trừ component trùng.
- Bundle component phải có SKU/variant và quantity; cấu hình theo size phải rõ. Chỉ cho thuê lẻ component khi policy cho phép.
- Chỉ asset ở trạng thái phù hợp mới được cấp phát; hàng đang kiểm tra/vệ sinh, sửa chữa, mất hoặc ngừng dùng không sẵn sàng. Nếu dùng quantity-based stock cho hàng hoán đổi, phải có movement/ledger, audit và concurrency control; stock không âm.

## Booking And Availability

- Tách lifecycle booking, payment/refund, tình trạng asset và lịch allocation. State transition phải kiểm tra trạng thái nguồn, quyền actor, điều kiện nghiệp vụ, tác động inventory và audit; không cho client set status tùy ý.
- Phân biệt thời gian khách thuê và thời gian khóa hàng. Dùng trường rõ nghĩa như `pickup_at`, `due_at`, `handed_over_at`, `returned_at`, `blocked_from`, `blocked_until`; quy ước overlap nhất quán, mặc định khoảng nửa mở `[start, end)`.
- Availability phải xét mọi component bundle, allocation/hold, asset đang thuê, buffer chuẩn bị/vệ sinh/kiểm tra và tình trạng thực tế. UI chỉ hiển thị tham khảo; server kiểm tra lại khi tạo/xác nhận booking.
- Chống overbooking bằng transaction và locking/constraint đã kiểm thử; hai yêu cầu tranh món cuối không thể cùng thành công. Tạo booking + allocation phải nguyên tử.
- Hold có hạn phải có `expires_at`; cancel/expire/payment failure release allocation an toàn và idempotent. Đổi ngày/size/bundle phải kiểm tra lịch mới và cập nhật allocation nguyên tử; nếu không khả dụng, giữ nguyên lịch cũ.
- Trả hàng không tự khiến asset sẵn sàng. Asset chỉ `READY` sau các bước kiểm tra/vệ sinh/sửa chữa bắt buộc; buffer hết không thay thế xác nhận thực tế. Trả trễ gây xung đột phải được cảnh báo và xử lý theo workflow, không double-allocate.

## Pricing, Deposit And Finance

- Backend là nguồn sự thật về giá. Lưu snapshot giá, thời lượng/gói, bundle composition, discount, fee, mức cọc và phiên bản policy tại thời điểm booking; cập nhật catalog/policy không sửa đơn lịch sử.
- Tách tiền thuê, tiền bảo đảm hoàn lại, tiền đặt cọc giữ lịch, phí giao nhận/trễ/hỏng/mất, tiền đã thu, khoản chờ hoàn và khoản đã hoàn. Không suy ra tất cả từ một `deposit` hay `is_paid`.
- Gói thuê, cách tính/làm tròn thời lượng, giá bundle/món lẻ, cọc, hủy/no-show, trễ/hư hỏng, thuế, phí và hoàn tiền phải được chủ sản phẩm chốt hoặc cấu hình. Xem `about.md`; không tự đặt policy.
- Thu/hoàn tiền và webhook phải idempotent; verify signature khi provider hỗ trợ, chống replay, kiểm tra event/transaction ID, amount, currency, reference và state trước khi cập nhật. Refund pending/failed/partial là trạng thái/giao dịch rõ ràng.
- Không lưu số thẻ/CVV hoặc credential thanh toán; dùng cơ chế tokenization/hosted checkout phù hợp. Khoản khấu trừ cọc cần lý do, actor, bằng chứng khi phù hợp và audit.

## API Standards

- REST dùng noun và HTTP method đúng nghĩa; `GET` không đổi dữ liệu. Validate kiểu, độ dài, range, pagination, date/time, quantity, content type và body ở server.
- Dùng error format thống nhất, mã lỗi ổn định, field validation và request ID. Không trả stack trace, SQL, secret hoặc đường dẫn nội bộ. DTO không lộ PII/field nội bộ không cần thiết.
- Mọi endpoint đọc/ghi phải kiểm tra function-level và object-level authorization. Danh sách có pagination/page-size limit. Không hard-delete booking/payment/audit trong luồng thông thường.

## Security, Privacy And Secrets

- Password phải được hash bằng Argon2id hoặc bcrypt phù hợp. Bảo vệ login/register/reset bằng rate limit và token random, có hạn, single-use; không để lộ email/tài khoản có tồn tại.
- Với cookie session: `Secure`, `HttpOnly`, `SameSite` phù hợp, rotate sau login/nâng quyền, revoke khi logout và CSRF protection cho request thay đổi trạng thái. Tránh lưu token trong `localStorage`/`sessionStorage`; cân nhắc MFA cho admin.
- Production bắt buộc HTTPS; CORS theo allowlist; cấu hình security headers, timeout, payload/concurrency limit và rate limit cho endpoint nhạy cảm. Dùng parameterized SQL, allowlist cho sort/field động; xem xét XSS, IDOR/BOLA, mass assignment, SSRF và resource exhaustion.
- Upload cần giới hạn size/count, kiểm tra nội dung, đặt tên an toàn, không thực thi file và lưu ngoài webroot/object storage; file riêng tư cần authorization hoặc URL hết hạn.
- Thu thập tối thiểu PII, giới hạn access/log/retention và rà soát yêu cầu pháp lý hiện hành; agent không tự kết luận pháp lý. DB user ít quyền; health endpoint không lộ config hoặc dữ liệu nhạy cảm; backup phải được thử restore.
- Không hard-code/commit secret thật trong code, test, seed, docs, migration, log hoặc config. Đọc qua env/secret manager, inject qua config layer và log ID/mask thay vì giá trị. Test/seed dùng dữ liệu giả; nếu nghi secret bị lộ, báo người dùng và đề xuất rotate/revoke.

## Background Jobs, Cache And Audit

- Job expiry/notification/provider sync/reconciliation cần timeout, retry hữu hạn, logging và idempotency; retry không được tạo charge/refund/release trùng. Notification failure không rollback booking/payment đã commit.
- Cache không phải source of truth cho booking/inventory/payment; quyết định allocation và tài chính phải đọc trạng thái transactional. Audit các thay đổi nhạy cảm của booking, inventory, role, payment/refund và kiểm tra trả hàng.

## Testing Requirements

- Bổ sung test theo rủi ro: unit cho domain/use case, integration cho repository/transaction, API cho auth/permission. Không xóa/làm yếu test để build xanh.
- Bắt buộc bao phủ bundle + món lẻ dùng chung kho, overlap/concurrent last item, hold expiry/cancel/reschedule, timezone/buffer, pricing snapshot, cọc/refund, state transition, webhook lặp/chữ ký sai và truy cập chéo object/role.
- Với module Go bị ảnh hưởng, chạy sau lần sửa cuối: `gofmt`, `go test ./...`, `go vet ./...`, `go test -race ./...`; chạy lint/integration/`govulncheck` nếu repo hoặc CI yêu cầu/hỗ trợ.
- Báo đúng lệnh và kết quả; nếu không chạy được, nêu rõ lý do. Không tuyên bố test/CI/security scan pass khi chưa xác minh.

## Production And Repository Boundary

- Dev phải tái lập được; production container chạy non-root khi khả thi, không hot reload và không chứa credential. Có health/readiness check, request ID, cảnh báo lỗi booking/payment/refund, backup và restore đã thử nghiệm.
- Migration chạy có kiểm soát trước traffic; lập kế hoạch tương thích/rollback khi đổi schema/payment. Không sửa migration đã triển khai; tạo migration mới.
- Mọi thay đổi code/config/migration/script/file chỉ trong repository. Không sửa global config, hệ thống hoặc dịch vụ ngoài repo, không cài/gỡ phần mềm nếu chưa được cho phép; chẩn đoán read-only ngoài repo được phép khi cần.

## Final Guardrails

- Không phức tạp hóa trước khi luồng thuê end-to-end ổn định. Ưu tiên đúng allocation, booking lifecycle, tài chính, authorization và audit hơn tốc độ sinh code.
- `about.md` chứa giải thích, ví dụ và checklist quyết định chưa chốt. Khi thiếu policy, mâu thuẫn với tài liệu mới hơn hoặc ảnh hưởng dữ liệu/tài chính, báo lại và xác minh trước khi triển khai.
