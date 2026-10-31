# Webee Rental: giao diện web

Giao diện cho khách hàng, nhân viên vận hành và quản trị viên. Dùng React, TypeScript, Vite, React Router, TanStack Query và CSS thuần trong [src/styles/app.css](src/styles/app.css).

## Chạy

Cần Node 20.19 trở lên.

```bash
cd web
npm install

npm run dev:mock   # dữ liệu mô phỏng, không cần backend
npm run dev        # API thật qua proxy, mặc định http://localhost:8080
```

Trang chạy ở `http://localhost:3000`, khớp `APP_BASE_URL` trong `.env.example`. Đổi địa chỉ API khi dev bằng biến `API_PROXY_TARGET`.

Chế độ mock có cổng thanh toán giả lập ở `/mock-gateway/{id}`: chọn kết quả thành công hoặc thất bại để thử luồng thanh toán. Trang này không có ô nhập thẻ.

Chế độ mock có tài khoản `khach@`, `nhanvien@`, `kho@`, `taichinh@`, `admin@webee.test`, mật khẩu `demo-webee-2026`, mã xác thực `123456`. Dữ liệu sống trong `sessionStorage` của tab, đóng tab là về dữ liệu gốc.

## Kiểm tra

```bash
npm run typecheck
npm test
npm run build
```

## Hiện trạng kết nối API

Mọi màn hình dùng API thật trong [API_CONTRACT.md](API_CONTRACT.md). Thanh toán trực tuyến và giao nhận tận nơi chưa chạy: backend báo `online_payment_available: false` và chỉ mở chiều `STORE`, nên giao diện hướng khách thanh toán tại quầy. Nếu một endpoint không tồn tại, API trả `request.not_found` và màn hình báo "chưa được kết nối API", không hiển thị dữ liệu giả.

Chạy với backend thật cần các bước sau ở thư mục gốc, với biến môi trường theo `.env.example`:

```bash
go run ./cmd/migrate
go run ./cmd/policy -file config/policy.yaml
go run ./cmd/bootstrap-admin -email owner@example.vn < password.txt
go run ./cmd/server
```

Mock trong `src/mocks/` chỉ chạy với `npm run dev:mock`. `vite build --mode mock` bị từ chối, và bản build thường không chứa mã mock.

## Quy ước

- Frontend không quyết định giá, quyền, trạng thái hay lịch trống. Tổng tiền lấy từ `POST /quotes`, nút thao tác lấy từ `actions` do backend trả, ngày mở cửa lấy từ `GET /store/calendar`.
- Không hard-code chính sách. Con số trên giao diện đều đến từ API.
- Kiểm tra quyền trên giao diện chỉ để ẩn hiện. API vẫn kiểm tra mọi thao tác.
- Trạng thái luôn có chữ đi kèm màu, qua `StatusBadge` và [src/lib/labels.ts](src/lib/labels.ts). Thêm trạng thái mới ở backend thì thêm nhãn ở đây; TypeScript báo thiếu.
- Mã lỗi backend được dịch trong [src/lib/api/errors.ts](src/lib/api/errors.ts).
- Giờ hiển thị luôn theo `Asia/Ho_Chi_Minh`, bất kể múi giờ của thiết bị.
- Giỏ thuê lưu `localStorage`, chỉ gồm lựa chọn của khách, không có giá. Session nằm trong cookie HttpOnly, không lưu token ở trình duyệt.
- Mã QR của TOTP được vẽ ngay trong trình duyệt, không gửi seed ra dịch vụ ngoài.
- Thanh toán online theo hosted checkout: frontend không nhận thông tin thẻ, không gửi số tiền lên API và không tin kết quả trên URL trả về.

## Triển khai

`npm run build` tạo trang tĩnh trong `dist/`. Máy chủ web cần trả `index.html` cho mọi đường dẫn không phải file.

Frontend phải cùng site với API, vì cookie session là `SameSite=Lax`. Có hai cách: chung origin qua reverse proxy (không cần cấu hình thêm), hoặc khác subdomain thì đặt `VITE_API_BASE_URL` lúc build và thêm origin của frontend vào `HTTP_TRUSTED_ORIGINS`.

Header bảo mật của `httpapi` chỉ áp dụng cho response API. Máy chủ phục vụ `dist/` phải tự cấu hình CSP, HSTS và các header tương ứng.
