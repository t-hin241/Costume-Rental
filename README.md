# Costume-Rental

## Chạy thử toàn bộ website bằng Docker

Cần Docker Desktop (hoặc Docker Engine có Compose), file `.env` và file `config/policy.yaml` đã điền. Hai file này không nằm trong git. Các port `3000` và `8025` phải còn trống. Postgres trong stack không mở ra host, nên không đụng Postgres đang chạy trên máy.

Stack dùng chung `.env` với lúc chạy `go run`. Chưa có file này thì chép từ `.env.example`. Ba biến sau phải có giá trị thật, các biến còn lại giữ nguyên:

| Biến | Tạo bằng |
|---|---|
| `POSTGRES_PASSWORD` | `openssl rand -hex 24` |
| `WEBEE_DB_PASSWORD` | `openssl rand -hex 24` |
| `AUTH_MFA_ENCRYPTION_KEY` | `openssl rand -base64 32` |

Container chỉ nhận các biến được liệt kê trong `compose.yaml`: ba biến trên và `SHOP_*`. Các địa chỉ `localhost` trong `.env` không ảnh hưởng tới stack.

Lần đầu, chạy ở thư mục gốc:

```bash
docker compose up -d --build --wait  # build image, migrate, chạy website
docker compose run --rm policy       # nạp config/policy.yaml
docker compose run --rm bootstrap-admin -email admin@webee.test   # gõ mật khẩu, ít nhất 12 ký tự
```

| Địa chỉ | Dùng để |
|---|---|
| http://localhost:3000 | Website: cửa hàng, vận hành (`/ops`), quản trị (`/admin`) |
| http://localhost:8025 | Mailpit: mọi email hệ thống gửi đi, gồm link xác minh và đặt lại mật khẩu |

Database mới không có sản phẩm. Thứ tự để có hàng cho thuê:

1. Đăng nhập admin ở `/login`. Lần đầu phải quét mã QR bằng ứng dụng xác thực (Google Authenticator, Authy...) và lưu mã khôi phục.
2. Vào `/admin/catalog` tạo danh mục, gói thuê, rồi món. Gói thuê và món mặc định là "Bản nháp"; phải chuyển sang "Đang bán" thì cửa hàng mới hiện. Điền giá cho từng gói. "Nhóm buffer" của món ghi `default` nếu không có nhóm riêng trong policy.
3. Vào `/ops/assets`, bấm "Nhập tài sản" với mã SKU của món, rồi bấm "Đạt kiểm tra". Asset mới ở trạng thái "Cần kiểm tra" và chưa cho thuê được.
4. Đăng ký tài khoản khách trong cửa sổ ẩn danh, mở link xác minh trong Mailpit, rồi đặt thuê. Lịch chỉ mở các ngày đủ thời gian đặt trước theo policy.
5. Quay lại `/ops/bookings` bằng tài khoản admin, mở đơn và bấm "Ghi nhận thu tiền" trước khi đơn hết hạn giữ hàng.

Thanh toán trực tuyến và giao nhận tận nơi chưa chạy, nên thử luồng thanh toán tại quầy.

### Lệnh thường dùng

```bash
docker compose up -d --build --wait  # build lại sau khi sửa code
docker compose logs -f api           # xem log API
docker compose down                  # dừng, giữ dữ liệu
docker compose down -v               # dừng và xóa toàn bộ dữ liệu, kể cả ảnh đã tải lên
docker compose run --rm policy       # nạp lại sau khi đổi config/policy.yaml; đổi giá trị thì phải đổi version
```

Lệnh `policy` kích hoạt phiên bản trong file, nên sẽ thay thế policy đã publish trên màn hình admin. Vì vậy nó không chạy tự động khi `up`.

Sau `docker compose down -v`, chạy lại `policy` và `bootstrap-admin`. Hai mật khẩu Postgres được ghi vào volume ở lần chạy đầu, nên đổi chúng thì phải `down -v`. Đổi `AUTH_MFA_ENCRYPTION_KEY` làm mọi ứng dụng xác thực đã đăng ký mất hiệu lực.

Stack này chỉ để thử trên máy cá nhân: chạy HTTP, cookie không `Secure`, email không gửi ra ngoài. Production cần TLS, HSTS, SMTP thật và `HTTP_COOKIE_SECURE=true`. Hai image `webee-rentor-api:local` và `webee-rentor-web:local` không chứa cấu hình hay credential, mọi thứ lấy từ biến môi trường lúc chạy.
