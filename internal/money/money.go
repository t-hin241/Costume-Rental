// Package money represents monetary amounts as integers in the currency's
// smallest unit. VND has no minor unit, so one unit is one đồng. Floats are
// never used.
package money

import (
	"encoding/json"
	"fmt"
	"math"

	"webee_rentor/internal/domainerr"
)

// Currency is an ISO 4217 code.
type Currency string

// VND is the only currency supported by the MVP.
const VND Currency = "VND"

// MaxAmount bounds any single stored amount so that sums over a booking
// cannot overflow int64. 10^15 VND is far above any realistic rental.
const MaxAmount int64 = 1_000_000_000_000_000

var (
	ErrUnsupportedCurrency = domainerr.New(domainerr.KindInvalid, "money.unsupported_currency", "currency is not supported")
	ErrCurrencyMismatch    = domainerr.New(domainerr.KindInvalid, "money.currency_mismatch", "amounts have different currencies")
	ErrOutOfRange          = domainerr.New(domainerr.KindInvalid, "money.out_of_range", "amount is outside the supported range")
)

// Valid reports whether the currency is supported.
func (c Currency) Valid() bool { return c == VND }

// ParseCurrency validates a currency code.
func ParseCurrency(s string) (Currency, error) {
	c := Currency(s)
	if !c.Valid() {
		return "", fmt.Errorf("parse currency %q: %w", s, ErrUnsupportedCurrency)
	}
	return c, nil
}

// Money is an amount with an explicit currency. The zero value has no
// currency and is rejected by arithmetic; use Zero or New.
type Money struct {
	amount   int64
	currency Currency
}

// New builds an amount, rejecting unsupported currencies and amounts beyond
// ±MaxAmount.
func New(amount int64, c Currency) (Money, error) {
	if !c.Valid() {
		return Money{}, fmt.Errorf("new money: %w", ErrUnsupportedCurrency)
	}
	if amount > MaxAmount || amount < -MaxAmount {
		return Money{}, fmt.Errorf("new money %d: %w", amount, ErrOutOfRange)
	}
	return Money{amount: amount, currency: c}, nil
}

// Zero returns a zero amount in c.
func Zero(c Currency) Money { return Money{currency: c} }

// Amount returns the integer amount in the currency's smallest unit.
func (m Money) Amount() int64 { return m.amount }

// Currency returns the currency code.
func (m Money) Currency() Currency { return m.currency }

func (m Money) IsZero() bool     { return m.amount == 0 }
func (m Money) IsPositive() bool { return m.amount > 0 }
func (m Money) IsNegative() bool { return m.amount < 0 }

// Equal reports whether both amount and currency match.
func (m Money) Equal(o Money) bool { return m == o }

// Add returns m+o.
func (m Money) Add(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	return New(m.amount+o.amount, m.currency)
}

// Sub returns m-o.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	return New(m.amount-o.amount, m.currency)
}

// Mul returns m*n.
func (m Money) Mul(n int64) (Money, error) {
	if !m.currency.Valid() {
		return Money{}, fmt.Errorf("multiply money: %w", ErrUnsupportedCurrency)
	}
	if n != 0 && (m.amount > math.MaxInt64/abs(n) || m.amount < -math.MaxInt64/abs(n)) {
		return Money{}, fmt.Errorf("multiply %d by %d: %w", m.amount, n, ErrOutOfRange)
	}
	return New(m.amount*n, m.currency)
}

// Compare returns -1, 0 or 1 when m is less than, equal to or greater than o.
func (m Money) Compare(o Money) (int, error) {
	if err := m.sameCurrency(o); err != nil {
		return 0, err
	}
	switch {
	case m.amount < o.amount:
		return -1, nil
	case m.amount > o.amount:
		return 1, nil
	}
	return 0, nil
}

func (m Money) String() string {
	return fmt.Sprintf("%d %s", m.amount, m.currency)
}

func (m Money) sameCurrency(o Money) error {
	if !m.currency.Valid() || !o.currency.Valid() {
		return ErrUnsupportedCurrency
	}
	if m.currency != o.currency {
		return fmt.Errorf("%s vs %s: %w", m.currency, o.currency, ErrCurrencyMismatch)
	}
	return nil
}

// Sum adds amounts that must all be in c.
func Sum(c Currency, ms ...Money) (Money, error) {
	total := Zero(c)
	for _, m := range ms {
		var err error
		if total, err = total.Add(m); err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

type moneyJSON struct {
	Amount   int64    `json:"amount"`
	Currency Currency `json:"currency"`
}

// MarshalJSON encodes {"amount": n, "currency": "VND"} for stored snapshots.
// The zero value, which has no currency, encodes as null.
func (m Money) MarshalJSON() ([]byte, error) {
	if m == (Money{}) {
		return []byte("null"), nil
	}
	return json.Marshal(moneyJSON{Amount: m.amount, Currency: m.currency})
}

// UnmarshalJSON decodes and re-validates an amount written by MarshalJSON.
func (m *Money) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*m = Money{}
		return nil
	}
	var v moneyJSON
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("decode money: %w", err)
	}
	n, err := New(v.Amount, v.Currency)
	if err != nil {
		return fmt.Errorf("decode money: %w", err)
	}
	*m = n
	return nil
}
