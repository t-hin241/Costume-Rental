// Package support owns customer support requests: a subject and the messages
// exchanged about it. It never changes bookings or payments.
package support

import (
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

// Status is where a request stands.
type Status string

const (
	StatusOpen       Status = "OPEN"
	StatusInProgress Status = "IN_PROGRESS"
	StatusResolved   Status = "RESOLVED"
)

// Topic is what a request is about.
type Topic string

const (
	TopicBooking  Topic = "BOOKING"
	TopicPayment  Topic = "PAYMENT"
	TopicProduct  Topic = "PRODUCT"
	TopicDelivery Topic = "DELIVERY"
	TopicOther    Topic = "OTHER"
)

func (t Topic) valid() bool {
	switch t {
	case TopicBooking, TopicPayment, TopicProduct, TopicDelivery, TopicOther:
		return true
	}
	return false
}

// Sender says which side wrote a message.
type Sender string

const (
	SenderCustomer Sender = "CUSTOMER"
	SenderStaff    Sender = "STAFF"
)

const (
	maxSubject = 160
	maxBody    = 2000
)

// ErrResolved means a resolved request takes no more replies.
var ErrResolved = domainerr.New(domainerr.KindInvalidState, "support.resolved", "this support request is already resolved")

// Message is one entry of the conversation.
type Message struct {
	Sender Sender
	Author identity.UserID
	Body   string
	At     time.Time
}

// Request is one customer question.
type Request struct {
	ID          string
	Code        string
	CustomerID  identity.UserID
	Topic       Topic
	Subject     string
	Status      Status
	BookingCode string
	Messages    []Message
	CreatedAt   time.Time
}

func checkBody(body string) error {
	if strings.TrimSpace(body) == "" || utf8.RuneCountInString(body) > maxBody {
		return domainerr.Invalid("message", "required, at most 2000 characters")
	}
	return nil
}

// New opens a request with the customer's first message.
func New(id, code string, customer identity.UserID, topic Topic, subject, body, bookingCode string, now time.Time) (Request, error) {
	subject = strings.TrimSpace(subject)
	switch {
	case !topic.valid():
		return Request{}, domainerr.Invalid("topic", "unknown topic")
	case subject == "" || utf8.RuneCountInString(subject) > maxSubject:
		return Request{}, domainerr.Invalid("subject", "required, at most 160 characters")
	}
	if err := checkBody(body); err != nil {
		return Request{}, err
	}
	at := now.UTC()
	return Request{
		ID: id, Code: code, CustomerID: customer, Topic: topic, Subject: subject, Status: StatusOpen, BookingCode: bookingCode,
		Messages: []Message{{Sender: SenderCustomer, Author: customer, Body: body, At: at}}, CreatedAt: at,
	}, nil
}

// Reply adds a staff answer and moves the request on; resolve closes it.
func (r *Request) Reply(author identity.UserID, body string, resolve bool, now time.Time) (Message, error) {
	if r.Status == StatusResolved {
		return Message{}, ErrResolved
	}
	if err := checkBody(body); err != nil {
		return Message{}, err
	}
	m := Message{Sender: SenderStaff, Author: author, Body: body, At: now.UTC()}
	r.Messages = append(r.Messages, m)
	r.Status = StatusInProgress
	if resolve {
		r.Status = StatusResolved
	}
	return m, nil
}
