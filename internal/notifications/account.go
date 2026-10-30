package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
)

// AccountMail writes the account emails (verification, password reset,
// duplicate registration) and queues them. It satisfies
// identityapp.Notifier.
//
// Links point at frontend pages (decision D24): /verify-email,
// /reset-password, /forgot-password and /login under BaseURL. The token
// travels in the URL fragment, which browsers never send to servers, so it
// does not land in access logs or Referer headers; the page reads it and
// posts it to the API.
type AccountMail struct {
	queue *Queue
	base  *url.URL
	shop  string
	loc   *time.Location
}

// NewAccountMail validates the frontend base URL (https unless
// allowInsecure, for local development).
func NewAccountMail(q *Queue, baseURL, shopName string, allowInsecure bool) (*AccountMail, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(allowInsecure && u.Scheme == "http")) {
		return nil, errors.New("notifications: APP_BASE_URL must be an absolute https URL without query or fragment")
	}
	if q == nil || strings.TrimSpace(shopName) == "" {
		return nil, errors.New("notifications: AccountMail needs a queue and a shop name")
	}
	loc, err := businesstime.LoadLocation()
	if err != nil {
		return nil, err
	}
	return &AccountMail{queue: q, base: u, shop: shopName, loc: loc}, nil
}

func (m *AccountMail) link(page, token string) string {
	u := *m.base
	u.Path += page
	if token != "" {
		u.Fragment = "token=" + token
	}
	return u.String()
}

func (m *AccountMail) local(t time.Time) string {
	return t.In(m.loc).Format("15:04 02/01/2006")
}

// VerifyEmail sends the link that activates a new account.
func (m *AccountMail) VerifyEmail(_ context.Context, email, token string, expiresAt time.Time) error {
	return m.queue.Enqueue(Message{Kind: "verify-email", To: email,
		Subject: fmt.Sprintf("[%s] Xác minh email đăng ký", m.shop),
		Body: fmt.Sprintf(`Xin chào,

Bạn (hoặc ai đó) vừa đăng ký tài khoản %s bằng email này.
Bấm vào liên kết sau để xác minh email và kích hoạt tài khoản:

%s

Liên kết dùng được một lần và hết hạn lúc %s (giờ Việt Nam).
Nếu bạn không đăng ký, hãy bỏ qua email này; tài khoản sẽ không được kích hoạt.
`, m.shop, m.link("/verify-email", token), m.local(expiresAt))})
}

// PasswordReset sends a single-use reset link.
func (m *AccountMail) PasswordReset(_ context.Context, email, token string, expiresAt time.Time) error {
	return m.queue.Enqueue(Message{Kind: "password-reset", To: email,
		Subject: fmt.Sprintf("[%s] Đặt lại mật khẩu", m.shop),
		Body: fmt.Sprintf(`Xin chào,

Chúng tôi nhận được yêu cầu đặt lại mật khẩu cho tài khoản %s của bạn.
Bấm vào liên kết sau để đặt mật khẩu mới:

%s

Liên kết dùng được một lần và hết hạn lúc %s (giờ Việt Nam).
Đặt lại mật khẩu sẽ đăng xuất bạn khỏi mọi thiết bị.
Nếu bạn không yêu cầu, hãy bỏ qua email này; mật khẩu hiện tại vẫn giữ nguyên.
`, m.shop, m.link("/reset-password", token), m.local(expiresAt))})
}

// AccountExists tells a verified owner that someone tried to register the
// address again.
func (m *AccountMail) AccountExists(_ context.Context, email string) error {
	return m.queue.Enqueue(Message{Kind: "account-exists", To: email,
		Subject: fmt.Sprintf("[%s] Email này đã có tài khoản", m.shop),
		Body: fmt.Sprintf(`Xin chào,

Ai đó vừa thử đăng ký tài khoản %s bằng email này, nhưng email đã có tài khoản.
Không có gì thay đổi với tài khoản của bạn.

Đăng nhập: %s
Quên mật khẩu: %s

Nếu không phải bạn, hãy bỏ qua email này.
`, m.shop, m.link("/login", ""), m.link("/forgot-password", ""))})
}
