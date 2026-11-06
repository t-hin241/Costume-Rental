# Staging: Oracle Cloud + Cloudflare (Free)

Một VM Oracle Always Free chạy toàn bộ stack bằng [compose.staging.yaml](../../compose.staging.yaml). VM không mở cổng nào ra Internet ngoài SSH. Request đi theo đường:

```
trình duyệt ─HTTPS→ Cloudflare Access ─→ Worker webee-staging-edge (*.workers.dev)
            ─Workers VPC→ Cloudflare Tunnel ─→ cloudflared (VM) ─→ nginx (web) ─/api/→ API Go ─→ Postgres
```

Container `cloudflared` chỉ kết nối **ra ngoài** tới Cloudflare. Tunnel không có public hostname, nên chỉ Worker (qua binding VPC) vào được site. Tổng chi phí: 0đ.

| # | Quyết định | Chọn |
|---|---|---|
| S-01 | Hosting | Oracle Cloud Always Free, VM Ampere A1 (ARM64), Ubuntu 24.04, chỉ mở SSH |
| S-02 | Domain | `webee-staging-edge.<account>.workers.dev` |
| S-03 | TLS / edge | Worker [staging-edge](../cloudflare/staging-edge/) + Workers VPC + Cloudflare Tunnel |
| S-04 | Email | Mailtrap Email Sandbox. Cloudflare không có SMTP relay, mà app gửi mail qua SMTP |
| S-05 | Truy cập | Cloudflare Access (OTP qua email, allowlist từng người). Worker kiểm tra lại JWT của Access và từ chối nếu thiếu/sai |
| S-06 | Database | Postgres trong compose |
| S-07 | Backup | [backup.sh](backup.sh) hằng đêm → R2. Diễn tập bằng [restore.sh](restore.sh) |
| S-08 | Giám sát | Cron của Worker mỗi 5 phút: `/readyz` và backup mới nhất trên R2, báo Telegram khi trạng thái đổi |

Staging chỉ chứa dữ liệu giả. Không nhập dữ liệu khách hàng thật, không dùng lại secret của local hay production.

**Rủi ro đã chấp nhận:**
- Workers VPC đang open beta, miễn phí cho mọi gói. Giá sau GA chưa công bố; nếu bị tính phí, xem mục 8.
- Workers Free giới hạn 100k request/ngày, đủ cho vài người thử.
- R2 và Zero Trust có thể đòi thêm phương thức thanh toán dù dùng gói Free.

## 1. Tài khoản cần có

- **Oracle Cloud**, cho VM.
- **Cloudflare**, cho Workers, Zero Trust (Access + Tunnel), R2 và KV.
- **Mailtrap**: Email Testing → tạo inbox → tab SMTP lấy host, port, username, password.
- **Telegram**:
  1. Nhắn `@BotFather` lệnh `/newbot` để lấy **bot token**.
  2. Nhắn một tin bất kỳ cho bot.
  3. Mở `https://api.telegram.org/bot<token>/getUpdates` và lấy `chat.id`.

## 2. VM Oracle (một lần)

1. Tạo instance shape `VM.Standard.A1.Flex`, ví dụ 2 OCPU/12 GB, image Ubuntu 24.04 aarch64, đăng nhập SSH bằng key.
   - **Không** cần mở cổng 80/443: Security List chỉ giữ SSH 22, iptables để mặc định.
   - Oracle có thể thu hồi VM Always Free bị coi là rảnh. Nâng tài khoản lên Pay-As-You-Go thì tránh được việc này mà vẫn 0đ trong hạn mức; nhớ đặt Budget alert (ví dụ 1 USD).
2. Cài Docker Engine và compose plugin theo hướng dẫn chính thức của Docker cho Ubuntu. Sau đó chạy `sudo usermod -aG docker $USER` rồi đăng nhập lại.
3. Cài `git`, `curl` và `rclone` (`sudo apt install rclone`, hoặc bản mới hơn từ rclone.org).

## 3. Cloudflare (một lần)

Làm theo đúng thứ tự, vì mỗi bước cần ID của bước trước.

1. **Zero Trust:** chọn gói Free và đặt team name, ví dụ `webee`. Team domain sẽ là `webee.cloudflareaccess.com`.
2. **Tunnel:** Zero Trust → Networks → Tunnels → Create (loại Cloudflared).
   - Copy token ở bước "install connector" (chuỗi dài sau `--token`); đây là `CLOUDFLARE_TUNNEL_TOKEN`.
   - **Không** thêm Public hostname.
3. **Workers VPC Service:** Workers & Pages → VPC → VPC Services → Create.
   - Type HTTP, chọn tunnel vừa tạo.
   - Host/IP `172.30.78.10` (IP cố định của container `web`), HTTP port `8080`.
   - Copy service ID.

   Hoặc tạo bằng lệnh:

   ```bash
   npx wrangler vpc service create webee-staging-web --type http \
     --tunnel-id <tunnel-id> --ipv4 172.30.78.10 --http-port 8080
   ```
4. **R2:**
   - Tạo bucket `webee-staging-backups`.
   - Settings → Object lifecycle rules: xóa object sau 14 ngày.
   - R2 → Manage API tokens: tạo token *Object Read & Write*, chỉ cho bucket này. Ghi lại Access Key ID, Secret Access Key và endpoint `https://<account-id>.r2.cloudflarestorage.com`.
5. **KV:** trong `deploy/cloudflare/staging-edge/` chạy `npx wrangler kv namespace create STATE`, copy `id`.
6. **Deploy Worker** (từ máy local, cần Node 20+):

   ```bash
   cd deploy/cloudflare/staging-edge
   npm ci
   npx wrangler login
   # sửa wrangler.jsonc: vpc_services[0].service_id, kv_namespaces[0].id, ACCESS_TEAM_DOMAIN
   npx wrangler secret put TELEGRAM_BOT_TOKEN
   npx wrangler secret put TELEGRAM_CHAT_ID
   npm test && npm run typecheck && npm run deploy
   ```

   URL in ra (`https://webee-staging-edge.<account>.workers.dev`) chính là `STAGING_HOST`, bỏ `https://`.
7. **Bật Access:**
   - Workers & Pages → `webee-staging-edge` → tab Access, hoặc Settings → Domains & Routes → Enable Cloudflare Access.
   - Chọn bảo vệ mọi domain của Worker.
   - Manage Cloudflare Access → policy *Allow* cho các email được thử nghiệm.
   - Copy **Application Audience (AUD) tag** vào `ACCESS_AUD` trong `wrangler.jsonc`, rồi `npm run deploy` lại.

   Trước khi có AUD, Worker trả 403 cho mọi request (cố ý, fail closed).

## 4. Lần deploy đầu trên VM

Chỉ deploy bản đã commit: trên máy local, commit hoặc stash thay đổi dở, chạy kiểm tra ở mục 7, rồi push.

```bash
git clone <repo-url> ~/webee_rentor && cd ~/webee_rentor
```

`config/` bị gitignore nên phải copy từ máy local:

```bash
# chạy trên máy local
scp config/policy.yaml config/delivery.yaml ubuntu@<vm-ip>:~/webee_rentor/config/
```

Tạo file secret trên VM:

```bash
cp deploy/staging/env.example .env.staging && chmod 600 .env.staging
openssl rand -hex 24        # POSTGRES_PASSWORD
openssl rand -hex 24        # WEBEE_DB_PASSWORD
openssl rand -base64 32     # AUTH_MFA_ENCRYPTION_KEY
nano .env.staging           # điền thêm STAGING_HOST, CLOUDFLARE_TUNNEL_TOKEN, SMTP_*
```

Cấu hình rclone cho R2:

```bash
rclone config create r2 s3 provider Cloudflare env_auth false \
  access_key_id <key-id> secret_access_key <secret> \
  endpoint https://<account-id>.r2.cloudflarestorage.com acl private no_check_bucket true
rclone lsd r2:                      # thấy webee-staging-backups
```

Chạy stack. Build ngay trên VM ARM; mọi base image đều có bản arm64.

```bash
sh deploy/staging/dc build
sh deploy/staging/dc up -d          # migrate → api → web → cloudflared
sh deploy/staging/dc ps             # api, web healthy; migrate exited (0); cloudflared running
sh deploy/staging/dc logs cloudflared   # thấy "Registered tunnel connection"
```

Kích hoạt policy một lần. Không chạy lại sau khi đã sửa policy trên màn hình admin, vì lệnh sẽ ghi đè.

```bash
sh deploy/staging/dc run --rm policy -file /config/policy.yaml -delivery /config/delivery.yaml
```

Tạo tài khoản admin. Password đọc từ dòng đầu stdin; xóa file ngay sau đó.

```bash
sh deploy/staging/dc run --rm -T bootstrap-admin -email owner@example.test < password.txt && shred -u password.txt
```

Cài backup hằng đêm (`crontab -e`). Cron trên VM chạy theo UTC: 19:15 UTC là 02:15 giờ Việt Nam.

```cron
15 19 * * * sh /home/ubuntu/webee_rentor/deploy/staging/backup.sh >>/home/ubuntu/webee-backup.log 2>&1
```

Chạy `sh deploy/staging/backup.sh` một lần ngay, để monitor không báo "chưa có backup".

## 5. Deploy bản mới

Trên VM:

```bash
cd ~/webee_rentor
sh deploy/staging/backup.sh        # trước mọi lần có migration mới
git pull --ff-only
sh deploy/staging/dc build
sh deploy/staging/dc up -d         # migrate chạy trước, api chỉ lên khi migrate thành công
sh deploy/staging/dc ps
```

Nếu migrate lỗi, api cũ vẫn chạy với schema cũ. Xem lỗi bằng `sh deploy/staging/dc logs migrate`. Không sửa migration đã chạy; tạo migration mới.

Nếu đổi Worker: `cd deploy/cloudflare/staging-edge && npm test && npm run deploy`, từ máy local.

## 6. Backup và restore

- **Backup:** [backup.sh](backup.sh) dump DB và tar media vào `BACKUP_DIR` (mặc định `~/webee-backups/<UTC timestamp>/`).
  - Script kiểm tra dump đọc lại được, ghi `SHA256SUMS` và giữ `BACKUP_KEEP` bản trên VM.
  - Sau đó copy lên R2; `db.dump` được đẩy sau cùng.
  - Monitor coi một backup là có khi `db.dump` đã nằm trên R2. Quá `BACKUP_MAX_AGE_HOURS` (26 giờ) mà không có bản mới thì Telegram báo.
- **Diễn tập restore**, ít nhất một lần và sau mỗi thay đổi schema lớn:

  ```bash
  sh deploy/staging/restore.sh ~/webee-backups/<timestamp>
  ```

  Script nạp backup vào DB tạm `webee_restore_check`, in số dòng các bảng chính rồi xóa DB tạm. DB thật không bị đụng tới.
- **Lấy backup từ R2 về** (khi VM mất): `rclone copy r2:webee-staging-backups/<timestamp> ~/webee-backups/<timestamp>`.
- **Khôi phục thật** (chỉ khi mất dữ liệu staging):
  1. `sh deploy/staging/dc stop api web cloudflared`
  2. Superuser `DROP DATABASE webee_rentor; CREATE DATABASE webee_rentor OWNER webee;`
  3. `pg_restore` dump như trong `restore.sh`, nhưng vào `webee_rentor`.
  4. Giải nén `media.tar` vào volume `media-data`.
  5. `sh deploy/staging/dc up -d`

## 7. Kiểm chứng

Trước khi deploy, trên máy local:

```bash
gofmt -l . && go vet ./... && go test -race ./...
(cd web && npm run typecheck && npm test && npm run build)
(cd deploy/cloudflare/staging-edge && npm test && npm run typecheck && npm run check)
```

Sau khi deploy:

```bash
curl -sI https://<host>/ | grep -iE '^HTTP|^location'      # 302 sang <team>.cloudflareaccess.com
curl -s https://<host>/ -H 'Cf-Access-Jwt-Assertion: x.y.z'  # vẫn bị Access chặn trước khi tới Worker
sudo ss -ltnp | grep -v '127.0.0.1\|::1'                   # trên VM: chỉ còn :22
```

Trên trình duyệt (sau khi đăng nhập Access bằng OTP), kiểm tra:

- Cookie session có `Secure` và `HttpOnly`. Response có `Strict-Transport-Security` và `X-Robots-Tag`.
- Đăng ký khách thì mail xác minh vào Mailtrap, và link trỏ đúng `https://<host>`.
- Đi hết luồng: catalog → báo giá → booking → staff xác nhận → thu tiền tại quầy → bàn giao → nhận trả/kiểm tra.
- Ảnh upload vẫn còn sau `sh deploy/staging/dc restart api`.
- Hold quá hạn được giải phóng (ticker chạy mỗi phút).
- Rate limit theo IP thật: login sai liên tục từ một mạng thì mạng khác (ví dụ 4G) vẫn đăng nhập được. Nếu cả hai cùng bị chặn, xem log `request failed`/rate limit của API. Có thể `cloudflared` đã chèn thêm hop vào `X-Forwarded-For`; khi đó cần chỉnh `HTTP_TRUSTED_PROXIES` hoặc cách Worker đặt header.

Kiểm tra monitor:

- `sh deploy/staging/dc stop api`: trong khoảng 10 phút có tin Telegram 🔴. Sau `sh deploy/staging/dc start api` có đúng một tin 🟢.
- Đặt tạm `BACKUP_MAX_AGE_HOURS` là `"0"` trong `wrangler.jsonc` rồi deploy: có tin 🟠. Nhớ trả lại `"26"`.
- Log của Worker: Workers & Pages → `webee-staging-edge` → Logs.

## 8. Vận hành khác

- **Log:** `sh deploy/staging/dc logs -f api`. Mỗi container giữ tối đa 5 × 10 MB.
- **Thêm hoặc bớt người thử:** sửa policy của Access application, không cần deploy lại.
- **Rotate token tunnel:** Zero Trust → Tunnels → Refresh token. Sửa `CLOUDFLARE_TUNNEL_TOKEN`, rồi `sh deploy/staging/dc up -d cloudflared`.
- **Rotate Telegram/Mailtrap:** `npx wrangler secret put ...`; với Mailtrap thì sửa `SMTP_*` rồi `sh deploy/staging/dc up -d api`.
- **Rotate secret khác:** `AUTH_MFA_ENCRYPTION_KEY` mới làm mọi authenticator đã đăng ký mất hiệu lực. Mật khẩu DB gắn với volume từ lần chạy đầu, nên đổi bằng `ALTER ROLE` hoặc xóa sạch staging.
- **Nghi lộ secret:** rotate ngay giá trị đó.
- **Workers VPC hết miễn phí (đường lui):**
  1. Mua một domain và thêm vào Cloudflare.
  2. Thêm Public hostname cho **cùng tunnel đó**, trỏ `http://172.30.78.10:8080`.
  3. Tạo Access application cho hostname đó.
  4. Đổi `STAGING_HOST` rồi `sh deploy/staging/dc up -d`.

  VM không phải đổi gì thêm. Lúc đó Worker chỉ còn giữ phần cron giám sát.
- **Xóa sạch staging:** `sh deploy/staging/dc down -v`. Lệnh này xóa DB và ảnh, không hoàn tác được.
