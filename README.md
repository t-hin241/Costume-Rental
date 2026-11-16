# Webee Rental

Website cho thuê trang phục (costume) cho một cửa hàng. Khách thuê nguyên bộ hoặc từng món lẻ. Hệ thống bao trọn luồng vận hành: catalog, kiểm tra lịch trống, đặt thuê, thu tiền và cọc, bàn giao, nhận trả và kiểm tra, rồi đối soát và hoàn tiền.

Dự án ưu tiên vận hành đúng hơn tính năng rộng: không đặt trùng hàng, tiền bạc chính xác, phân quyền chặt và có audit cho mọi thao tác nhạy cảm. Tiền tệ mặc định là VND, giờ nghiệp vụ theo `Asia/Ho_Chi_Minh`.

## Tính năng

**Khách hàng**

- Xem catalog theo danh mục, món lẻ và bộ (bundle), chọn size và gói thuê.
- Kiểm tra lịch trống và báo giá do backend tính, kể cả giảm giá, phí và tiền cọc.
- Đặt thuê, giữ hàng có thời hạn, đổi lịch, hủy đơn có xem trước số tiền hoàn.
- Quản lý đơn thuê, hồ sơ cá nhân, yêu cầu hỗ trợ.
- Đăng ký có xác minh email, quên mật khẩu, xác thực hai lớp (TOTP) tùy chọn.

**Vận hành** (`/ops`)

- Bảng việc trong ngày, lịch thuê, cảnh báo trả trễ và xung đột lịch.
- Xác nhận đơn, chuẩn bị hàng, bàn giao, nhận trả, kiểm tra tình trạng, báo mất.
- Quản lý tài sản vật lý: nhập kho, kiểm tra, vệ sinh, sửa chữa, ngừng sử dụng, lịch sử từng món.
- Ghi nhận thu tiền tại quầy, đề xuất phí hư hỏng hoặc trễ, xử lý hoàn tiền.
- Giao nhận thủ công và hàng đợi hỗ trợ khách.

**Quản trị** (`/admin`)

- Tổng quan, doanh thu, xuất CSV, đối soát giao dịch, dòng tiền.
- Quản lý catalog, gói thuê, ảnh sản phẩm, chương trình khuyến mãi.
- Chính sách thuê và cọc dạng phiên bản: đổi chính sách không làm thay đổi đơn cũ.
- Người dùng, vai trò, khóa tài khoản, ma trận phân quyền, nhật ký audit, xóa dữ liệu cá nhân theo hạn lưu trữ.

**Hiện chưa chạy**

- Thanh toán trực tuyến: giao diện đã làm theo mô hình hosted checkout, nhưng chưa chọn cổng thanh toán. Khách trả tại quầy hoặc chuyển khoản, nhân viên ghi nhận.
- Giao nhận tận nơi chỉ ở chế độ thủ công, chưa tích hợp hãng vận chuyển.

Chi tiết từng endpoint và hiện trạng nằm trong [web/API_CONTRACT.md](web/API_CONTRACT.md).

## Nguyên tắc nghiệp vụ

- Backend là nguồn sự thật duy nhất về giá, quyền, trạng thái và lịch trống. Frontend chỉ hiển thị.
- Bundle dùng chung tồn kho với món lẻ. Đặt một bộ sẽ giữ đủ từng món thành phần, không đếm một tài sản hai lần.
- Tạo đơn và giữ hàng nằm trong cùng một transaction, có ràng buộc exclusion chống chồng lịch ở database. Hai người tranh món cuối cùng thì chỉ một người thành công.
- Hàng trả về không tự sẵn sàng. Tài sản chỉ cho thuê lại sau khi được kiểm tra, vệ sinh hoặc sửa xong.
- Mỗi đơn lưu snapshot giá, gói thuê, thành phần bộ, giảm giá, phí, mức cọc và phiên bản chính sách tại lúc đặt.
- Tiền lưu bằng số nguyên VND. Tiền thuê, tiền cọc, các loại phí, tiền đã thu, chờ hoàn và đã hoàn được tách riêng.
- Thu tiền, hoàn tiền và các job nền đều idempotent: gửi lại một yêu cầu không tạo giao dịch trùng.

## Kiến trúc

Modular monolith viết bằng Go, chia module theo domain. Mỗi module tách lớp domain (quy tắc nghiệp vụ, không phụ thuộc HTTP hay database), `*app` (use case, transaction, kiểm tra quyền) và `*pg` (truy cập PostgreSQL).

```
trình duyệt ─→ nginx (trang tĩnh React) ─/api/→ API Go ─→ PostgreSQL
                                                   └──→ SMTP (email)
```

| Module | Phạm vi |
|---|---|
| `identity` | Tài khoản, session, vai trò, phân quyền, MFA |
| `catalog` | Sản phẩm, biến thể, bộ, gói thuê, báo giá |
| `inventory` | Tài sản vật lý, cấp phát, trạng thái và lịch sử |
| `rentals` | Đơn thuê và vòng đời, đổi lịch, phí, trả hàng |
| `payments` | Thu tiền, cọc, phí, hoàn tiền, đối soát |
| `promotions`, `tax` | Khuyến mãi, ghi nhận doanh thu |
| `delivery` | Giao nhận |
| `support`, `notifications` | Hỗ trợ khách, email |
| `audit`, `retention` | Nhật ký thao tác, xóa dữ liệu hết hạn lưu trữ |
| `httpapi` | Routing, middleware, DTO, định dạng lỗi thống nhất |

Vai trò: `customer`, `rental_staff`, `inventory_manager`, `finance`, `admin`. Một người có thể có nhiều vai trò. Quyền được kiểm tra theo từng hành động và theo chủ sở hữu dữ liệu, ở cả endpoint lẫn use case.

## Công nghệ

- **Backend:** Go 1.27, PostgreSQL 16, pgx, golang-migrate, Argon2id.
- **Frontend:** React 19, TypeScript, Vite, React Router, TanStack Query, CSS thuần. MSW cho chế độ dữ liệu giả.
- **Hạ tầng:** Docker Compose, nginx, Caddy (HTTPS tự động), Cloudflare Tunnel, Access và Workers cho staging.
- **CI:** GitHub Actions chạy toàn bộ test với race detector trên PostgreSQL thật.

## Cấu trúc thư mục

```
cmd/
  server/            HTTP API
  migrate/           chạy migration
  policy/            nạp chính sách từ file YAML vào database
  bootstrap-admin/   tạo tài khoản quản trị đầu tiên
internal/            các module domain và hạ tầng (platform/)
  platform/postgres/migrations/   migration SQL có đánh số phiên bản
web/                 giao diện React
docker/
  nginx/             cấu hình web server và header bảo mật của image web
  postgres/          script tạo role ứng dụng khi khởi tạo database
compose.yaml         stack chạy thử trên máy cá nhân
```

Cấu hình triển khai (`deploy/`, `compose.staging.yaml`, `compose.production.yaml`) chỉ có trên branch `develop`, xem mục [Triển khai](#triển-khai).

## Chạy thử trên máy

Cần Docker có Compose. Hai file sau không nằm trong git và phải tự tạo:

- `.env`: chép từ [.env.example](.env.example). Điền giá trị thật cho `POSTGRES_PASSWORD`, `WEBEE_DB_PASSWORD` (`openssl rand -hex 24`) và `AUTH_MFA_ENCRYPTION_KEY` (`openssl rand -base64 32`).
- `config/policy.yaml`: chính sách thuê, cọc, giờ mở cửa do chủ cửa hàng duyệt.

```bash
docker compose up -d --build --wait
docker compose run --rm policy
docker compose run --rm bootstrap-admin -email admin@example.test
```

Website ở `http://localhost:3000`. Mọi email hệ thống gửi đi được Mailpit giữ lại ở `http://localhost:8025`. Stack này chỉ để thử: chạy HTTP thường và cookie không có cờ `Secure`.

Chỉ cần xem giao diện thì chạy frontend với dữ liệu giả, không cần backend:

```bash
cd web
npm install
npm run dev:mock
```

Tài khoản mẫu và cách chạy với API thật xem trong [web/README.md](web/README.md).

## Kiểm thử

```bash
go vet ./...
go test ./...
go test -race ./...
```

Test tích hợp cần biến `TEST_DATABASE_URL` trỏ tới một role PostgreSQL có quyền `CREATEDB`. Mỗi package tự tạo và xóa database riêng. Không đặt biến này thì các test đó được bỏ qua.

```bash
cd web
npm run typecheck
npm test
npm run build
```

## Triển khai

Cấu hình và runbook triển khai nằm trên branch `develop`, không có trên `main`:

- [deploy/production/README.md](https://github.com/t-hin241/Costume-Rental/blob/develop/deploy/production/README.md): một server Linux sau router, HTTPS bằng Let's Encrypt cho IP public hoặc domain, backup và diễn tập khôi phục.
- [deploy/staging/README.md](https://github.com/t-hin241/Costume-Rental/blob/develop/deploy/staging/README.md): VM Oracle Cloud không mở cổng ra ngoài, truy cập qua Cloudflare Tunnel và Access, có giám sát và backup lên R2.

Migration chạy bằng `cmd/migrate` trước khi nhận traffic. Không sửa migration đã triển khai, chỉ thêm migration mới.

## Bảo mật

- Mật khẩu băm bằng Argon2id. Session nằm trong cookie `HttpOnly`, `Secure`, `SameSite=Lax`, tiền tố `__Host-`, được xoay vòng khi đăng nhập và thu hồi khi đăng xuất hoặc đổi quyền.
- Chống CSRF theo origin, CORS theo allowlist. Rate limit cho đăng nhập, đăng ký, đặt lại mật khẩu, MFA và báo giá công khai. Đăng ký và quên mật khẩu không tiết lộ email đã tồn tại hay chưa.
- MFA bắt buộc với vai trò `admin` và `finance`, các vai trò khác bật tùy chọn. Secret TOTP được mã hóa khi lưu.
- Request body được đọc theo allowlist, từ chối field lạ. SQL luôn tham số hóa. Lỗi trả về chỉ có mã ổn định và request ID, không lộ SQL hay stack trace.
- Ảnh tải lên được giới hạn dung lượng, kiểm tra nội dung thật, đặt tên ngẫu nhiên và lưu ngoài web root.
- Image không chứa cấu hình hay credential. Trong stack production, API và web chạy non-root; các container ứng dụng có root filesystem chỉ đọc và bỏ capability không cần thiết; database nằm trong mạng nội bộ không có cổng ra host.
- Không commit secret. Cấu hình lấy từ biến môi trường; các file `.env*` (trừ `.env.example`) và `config/` được git bỏ qua. Mọi file mẫu chỉ chứa giá trị giả.

Nếu phát hiện lỗ hổng, vui lòng báo riêng cho người duy trì thay vì mở issue công khai.
