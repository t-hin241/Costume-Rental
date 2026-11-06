# Public: một server Linux sau router nhà

Toàn bộ stack chạy trên một server bằng [compose.production.yaml](../../compose.production.yaml). Server nằm sau router của bạn, router forward cổng 80/443 về server. Bạn chỉ điền `PUBLIC_HOST` (IP public hoặc domain) vào `.env.production` rồi build. HTTPS tự động.

```
Internet ─TCP 80/443, UDP 443→ router ─port forward→ server
  └ caddy (TLS Let's Encrypt) ─edge→ web (nginx) ─app→ api (Go) ─db→ postgres
```

- Chỉ container `caddy` publish cổng ra ngoài. Postgres, API và nginx không có cổng nào trên host.
- Mạng `db` là `internal`: Postgres không có đường ra Internet. Caddy không với tới API hay DB.
- API, nginx và Caddy chạy với `no-new-privileges`, root filesystem read-only. Cả ba drop hết capability; riêng Caddy giữ `NET_BIND_SERVICE` để bind 80/443.
- Cookie `Secure` + `HttpOnly`, CSRF theo origin `https://PUBLIC_HOST`. Rate limit theo IP thật của khách: Caddy ghi IP vào `X-Forwarded-For`, API chỉ tin hai proxy có IP cố định.

| # | Quyết định | Chọn |
|---|---|---|
| P-01 | Máy chạy | Linux (Ubuntu 24.04 LTS) + Docker Engine, máy thật hoặc VM. Windows + Docker Desktop chỉ để chạy thử (xem mục 9) |
| P-02 | Địa chỉ | Một biến `PUBLIC_HOST`: IPv4 public hoặc domain. Đổi qua lại không cần sửa source |
| P-03 | TLS | Caddy 2.11 + Let's Encrypt, profile `shortlived` (cert 6 ngày, Caddy tự gia hạn). Profile này là cách duy nhất có cert cho IP |
| P-04 | Cấu hình | `.env.production` ở gốc repo (gitignore, `chmod 600`), chạy qua [dc](dc). `.env` vẫn để dev local |
| P-05 | Email | SMTP thật có TLS, bắt buộc: API không chạy với `SMTP_TLS=none` khi cookie Secure |
| P-06 | Mục đích | Demo, chỉ dữ liệu giả. `X-Robots-Tag: noindex, nofollow` (`ROBOTS_TAG`) |
| P-07 | Backup | [backup.sh](backup.sh) hằng ngày vào ổ local, tùy chọn copy ra ngoài bằng rclone. Diễn tập bằng [restore.sh](restore.sh) |

**Rủi ro đã chấp nhận khi dùng IP thay domain:**
- Phải mở cả 80 và 443 từ Internet: Let's Encrypt kiểm tra qua cổng 80 (HTTP-01) hoặc 443 (TLS-ALPN-01).
- Cert chỉ 6 ngày. Nếu Let's Encrypt hoặc cổng 80/443 bị chặn quá vài ngày, site sẽ báo lỗi cert.
- IP đổi thì phải sửa `PUBLIC_HOST` và chạy lại. Link trong các email đã gửi trước đó sẽ chết.
- Link `https://1.2.3.4/...` trong email dễ bị bộ lọc đánh dấu spam/phishing.

Khi có domain (kể cả DDNS miễn phí như DuckDNS cho IP động), xem mục 7.

## 1. Kiểm tra đường truyền trước (5 phút)

1. **IP public thật hay CGNAT.** Vào trang quản trị router, xem địa chỉ WAN, rồi so với `curl -4 https://ifconfig.me` chạy trên server.
   - Hai giá trị giống nhau là được.
   - Nếu WAN là `100.64.x.x`–`100.127.x.x`, `10.x`, `172.16–31.x` hoặc `192.168.x`, bạn đang sau NAT của nhà mạng. Port forward sẽ không có tác dụng: gọi nhà mạng xin IP public (thường gọi là "IP tĩnh" hoặc "mở NAT").
2. **IP tĩnh hay động.** IP động thì nên dùng DDNS + domain (mục 7). Nếu vẫn dùng IP, mỗi lần IP đổi phải làm lại mục 5.
3. **Nhà mạng có chặn cổng 80/443 chiều vào không.** Sau khi forward (mục 2), thử từ 4G (không dùng Wi-Fi nhà): `http://<IP>/` phải trả về redirect. Nhiều gói Internet gia đình chặn sẵn hai cổng này.

## 2. Router và tường lửa

**Router:**
- Đặt DHCP reservation để server luôn có cùng IP LAN, ví dụ `192.168.1.50`.
- Port forward về IP LAN đó:
  - TCP 80 → 80
  - TCP 443 → 443
  - UDP 443 → 443 (HTTP/3, không bắt buộc)
- **Không** forward cổng nào khác: không 5432, không 8080, không 2375/2376 (Docker API).
- SSH chỉ nên dùng trong LAN. Nếu buộc phải mở SSH ra ngoài, chỉ dùng key (`PasswordAuthentication no`) và cân nhắc đổi cổng hoặc dùng `fail2ban`.
- Tắt UPnP trên router để không thiết bị nào tự mở cổng. Đổi mật khẩu admin router nếu vẫn là mặc định.

**Server (Ubuntu):**

```bash
sudo ufw default deny incoming
sudo ufw allow from 192.168.1.0/24 to any port 22 proto tcp   # SSH chỉ từ LAN
sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw allow 443/udp
sudo ufw enable
sudo apt install unattended-upgrades                      # vá bảo mật tự động
```

Lưu ý: cổng do Docker publish đi qua iptables của Docker, **không qua ufw**. Vì vậy compose này cố ý chỉ publish 80/443 của Caddy. Đừng thêm `ports:` cho service khác.

## 3. Cài đặt (một lần)

1. Cài Docker Engine + compose plugin theo hướng dẫn chính thức của Docker cho Ubuntu (không dùng bản snap).
2. Chạy `sudo usermod -aG docker $USER` rồi đăng nhập lại. Cài thêm `git`, `openssl`, và `rclone` nếu cần backup ra ngoài.
3. Lấy source. Chỉ deploy bản đã commit:

   ```bash
   git clone <repo-url> ~/webee_rentor && cd ~/webee_rentor
   ```

4. `config/` bị gitignore nên phải copy từ máy dev:

   ```bash
   # chạy trên máy dev
   scp config/policy.yaml config/delivery.yaml <user>@<server-lan-ip>:~/webee_rentor/config/
   ```

5. Tạo file cấu hình trên server:

   ```bash
   cp deploy/production/env.example .env.production && chmod 600 .env.production
   openssl rand -hex 24        # → POSTGRES_PASSWORD
   openssl rand -hex 24        # → WEBEE_DB_PASSWORD
   openssl rand -base64 32     # → AUTH_MFA_ENCRYPTION_KEY
   nano .env.production        # điền PUBLIC_HOST, các secret trên, SMTP_*
   ```

   - `PUBLIC_HOST` chỉ ghi địa chỉ, ví dụ `PUBLIC_HOST=203.0.113.10` hoặc `PUBLIC_HOST=shop.example.com`. Không ghi `https://`, cổng hay đường dẫn.
   - [dc](dc) từ chối chạy nếu giá trị sai dạng, là IP private/CGNAT, hoặc là IPv6.
   - Lần đầu nên đặt `ACME_CA` là CA staging của Let's Encrypt (dòng gợi ý trong file). Như vậy các lần thử router/firewall lỗi không làm bạn bị rate limit. Xem mục 4 bước 3.

## 4. Chạy lần đầu

1. Build và chạy:

   ```bash
   sh deploy/production/dc build
   sh deploy/production/dc up -d        # postgres → migrate → api → web → caddy
   sh deploy/production/dc ps           # api, web healthy; migrate exited (0); caddy running
   sh deploy/production/dc logs -f caddy
   ```

2. Trong log Caddy, tìm `certificate obtained successfully`.
   - Nếu thấy lỗi `connection refused`, `timeout` hoặc `unauthorized`, Let's Encrypt không vào được cổng 80/443. Kiểm tra lại mục 1–2.
3. Nếu đang dùng CA staging và đã lấy được cert, chuyển sang cert thật:
   1. Xóa giá trị `ACME_CA` trong `.env.production`.
   2. Chạy:

      ```bash
      sh deploy/production/dc rm -sf caddy
      docker volume rm webee-production_caddy-data
      sh deploy/production/dc up -d
      ```

4. Kích hoạt policy **một lần**. Không chạy lại sau khi đã sửa policy trên màn hình admin, vì lệnh sẽ ghi đè.

   ```bash
   sh deploy/production/dc run --rm policy -file /config/policy.yaml -delivery /config/delivery.yaml
   ```

5. Tạo tài khoản admin. Password đọc từ dòng đầu stdin; xóa file ngay sau đó:

   ```bash
   sh deploy/production/dc run --rm -T bootstrap-admin -email owner@example.com < password.txt && shred -u password.txt
   ```

   Đăng nhập rồi bật MFA cho admin và mọi tài khoản staff.

6. Cài backup hằng ngày (`crontab -e`). Server để múi giờ UTC thì 19:15 UTC là 02:15 giờ Việt Nam:

   ```cron
   15 19 * * * sh /home/<user>/webee_rentor/deploy/production/backup.sh >>/home/<user>/webee-backup.log 2>&1
   ```

## 5. Đổi IP, deploy bản mới

**IP public đổi:** sửa `PUBLIC_HOST`, rồi chạy `sh deploy/production/dc up -d`. Lệnh này tạo lại `api` (link email, CSRF origin) và `caddy` (xin cert mới cho IP mới).

**Bản code mới:**

```bash
cd ~/webee_rentor
sh deploy/production/backup.sh      # trước mọi lần có migration mới
git pull --ff-only
sh deploy/production/dc build
sh deploy/production/dc up -d       # migrate chạy trước; api chỉ lên khi migrate thành công
sh deploy/production/dc ps
```

Nếu migrate lỗi, api cũ vẫn chạy với schema cũ. Xem lỗi bằng `sh deploy/production/dc logs migrate`. Không sửa migration đã chạy; tạo migration mới.

## 6. Backup và restore

- [backup.sh](backup.sh) dump DB và tar ảnh vào `BACKUP_DIR` (mặc định `~/webee-backups/<UTC timestamp>/`).
  - Script kiểm tra dump đọc lại được, ghi `SHA256SUMS` và giữ `BACKUP_KEEP` bản gần nhất.
  - Nếu đặt `BACKUP_RCLONE_REMOTE`, backup được copy ra ngoài server. Nên copy ra ngoài: hỏng ổ là mất cả DB lẫn backup.
- Diễn tập restore ít nhất một lần, và sau mỗi thay đổi schema lớn. Lệnh nạp vào DB tạm rồi xóa, DB thật không bị đụng:

  ```bash
  sh deploy/production/restore.sh ~/webee-backups/<timestamp>
  ```

- Khôi phục thật:
  1. `sh deploy/production/dc stop caddy web api`
  2. Superuser chạy `DROP DATABASE webee_rentor; CREATE DATABASE webee_rentor OWNER webee;`
  3. `pg_restore` dump như trong `restore.sh`, nhưng vào `webee_rentor`.
  4. Giải nén `media.tar` vào volume `webee-production_media-data`.
  5. `sh deploy/production/dc up -d`

## 7. Chuyển sang domain (khuyến nghị khi có thể)

1. Trỏ bản ghi A của domain về IP public. Với IP động, dùng một dịch vụ DDNS (DuckDNS, No-IP, hoặc DDNS có sẵn trong router) để tự cập nhật.
2. Đặt `PUBLIC_HOST=shop.example.com`, rồi chạy `sh deploy/production/dc up -d`.

Cert domain cũng lấy theo profile `shortlived` và tự gia hạn. Sau khi chạy ổn định với domain, HSTS có hiệu lực trên trình duyệt (với IP thì trình duyệt bỏ qua HSTS).

## 8. Kiểm chứng

Trước khi deploy, trên máy dev:

```bash
gofmt -l . && go vet ./... && go test -race ./...
(cd web && npm run typecheck && npm test && npm run build)
```

Sau khi deploy, chạy từ một mạng **ngoài** (4G):

```bash
curl -sI http://<host>/ | grep -iE '^HTTP|^location'     # 308 → https://<host>/
curl -sI https://<host>/ | grep -iE '^HTTP|strict|x-robots|content-security'
echo | openssl s_client -connect <host>:443 2>/dev/null | openssl x509 -noout -issuer -dates   # Let's Encrypt, ~6 ngày
curl -s https://<host>/readyz                            # chỉ báo up/down
nc -vz -w 3 <host> 5432; nc -vz -w 3 <host> 8080          # đều phải thất bại
```

Trên server, chạy `sudo ss -ltnp`. Ngoài SSH, chỉ được thấy `docker-proxy` trên :80 và :443.

Trên trình duyệt:
- Cookie session có `Secure` và `HttpOnly`.
- Đăng ký khách thì mail xác minh tới, và link trỏ đúng `https://<host>`.
- Đi hết luồng: catalog → báo giá → booking → staff xác nhận → thu tiền → bàn giao → nhận trả.
- Ảnh upload vẫn còn sau `sh deploy/production/dc restart api`.

Kiểm tra rate limit theo IP thật: login sai liên tục từ 4G, sau đó từ Wi-Fi nhà vẫn đăng nhập được. Nếu cả hai cùng bị chặn, API không thấy IP thật; xem mục 9.

## 9. Chạy thử trên Windows + Docker Desktop

Dùng được, với cùng lệnh qua Git Bash (`sh deploy/production/dc ...`), nhưng chỉ nên để thử:

- Docker Desktop đưa traffic qua VM của nó, nên mọi kết nối từ Internet tới container đều mang một IP nội bộ.
  - Rate limit login/đăng ký/quên mật khẩu theo IP thành một giới hạn chung cho tất cả mọi người. Một người dò mật khẩu có thể làm cả site không đăng nhập được.
  - Log và audit cũng không có IP thật.
- Windows Defender Firewall phải cho phép inbound TCP 80/443 (và UDP 443) cho Docker Desktop. Router forward về IP LAN của máy Windows.
- Máy phải luôn bật, không sleep, và Docker Desktop phải tự chạy khi đăng nhập.

## 10. Vận hành khác

- **Log:** `sh deploy/production/dc logs -f api` hoặc `... caddy` (access log JSON, Cookie/Authorization đã bị Caddy che). Mỗi container giữ tối đa 5 × 10 MB.
- **Sửa Caddyfile:** `sh deploy/production/dc restart caddy` (admin API tắt nên không có reload nóng).
- **Rotate secret:**
  - `AUTH_MFA_ENCRYPTION_KEY` mới làm mọi authenticator đã đăng ký mất hiệu lực.
  - Mật khẩu DB gắn với volume từ lần chạy đầu, nên đổi bằng `ALTER ROLE`.
  - SMTP: sửa `SMTP_*` rồi `sh deploy/production/dc up -d api`.
- **Nghi lộ secret:** rotate ngay giá trị đó và đổi mật khẩu các tài khoản staff.
- **Lên production với khách thật:** đặt `ROBOTS_TAG=all`, chuyển sang domain, bật backup ra ngoài server, và rà soát yêu cầu pháp lý về dữ liệu cá nhân. Đây là quyết định của chủ sản phẩm, không làm tự động.
- **Xóa sạch:** `sh deploy/production/dc down -v`. Lệnh này xóa DB, ảnh và cert, không hoàn tác được.
