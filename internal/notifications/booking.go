package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/rentals"
)

// BookingMail writes the emails customers get about their bookings and queues
// them. It satisfies rentalsapp.Notifier.
type BookingMail struct {
	queue   *Queue
	shop    string
	loc     *time.Location
	emailOf func(ctx context.Context, id identity.UserID) (string, error)
}

// NewBookingMail builds the mailer. emailOf resolves the customer's address
// at send time, so no address is copied onto bookings.
func NewBookingMail(q *Queue, shopName string, emailOf func(ctx context.Context, id identity.UserID) (string, error)) (*BookingMail, error) {
	if q == nil || strings.TrimSpace(shopName) == "" || emailOf == nil {
		return nil, errors.New("notifications: BookingMail needs a queue, a shop name and an email lookup")
	}
	loc, err := businesstime.LoadLocation()
	if err != nil {
		return nil, err
	}
	return &BookingMail{queue: q, shop: shopName, loc: loc, emailOf: emailOf}, nil
}

// BookingChanged sends the email for a booking event. Events without a
// template send nothing.
func (m *BookingMail) BookingChanged(ctx context.Context, customer identity.UserID, code string, action rentals.Action, deadline time.Time) error {
	subject, body := "", ""
	switch action {
	case rentals.ActionConfirm:
		subject, body = "Đơn đã được xác nhận", fmt.Sprintf("Đơn %s của bạn đã được xác nhận.\n", code)
	case rentals.ActionCancel:
		subject, body = "Đơn đã bị hủy", fmt.Sprintf("Đơn %s đã bị hủy. Khoản được hoàn (nếu có) sẽ được xử lý sau khi cửa hàng duyệt.\n", code)
	case rentals.ActionExpire:
		subject, body = "Đơn đã hết hạn giữ hàng", fmt.Sprintf("Đơn %s đã hết thời gian giữ hàng nên không còn hiệu lực. Khoản đã thanh toán (nếu có) sẽ được hoàn sau khi cửa hàng duyệt.\n", code)
	case rentals.ActionReportMissing:
		subject = "Đơn còn món chưa trả"
		body = fmt.Sprintf("Đơn %s còn món chưa được trả. Vui lòng tìm lại và liên hệ cửa hàng trước %s (giờ Việt Nam).\n", code, deadline.In(m.loc).Format("15:04 02/01/2006"))
	case rentals.ActionComplete:
		subject, body = "Đơn đã hoàn tất", fmt.Sprintf("Đơn %s đã hoàn tất. Cảm ơn bạn đã thuê tại %s.\n", code, m.shop)
	default:
		return nil
	}
	email, err := m.emailOf(ctx, customer)
	if err != nil {
		return fmt.Errorf("booking %s customer email: %w", code, err)
	}
	return m.queue.Enqueue(Message{Kind: string(action), To: email, Subject: fmt.Sprintf("[%s] %s", m.shop, subject), Body: "Xin chào,\n\n" + body})
}
