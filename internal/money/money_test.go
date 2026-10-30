package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"webee_rentor/internal/money"
)

func vnd(t *testing.T, v int64) money.Money {
	t.Helper()
	m, err := money.New(v, money.VND)
	if err != nil {
		t.Fatalf("money.New(%d): %v", v, err)
	}
	return m
}

func TestArithmetic(t *testing.T) {
	sum, err := money.Sum(money.VND, vnd(t, 300_000), vnd(t, 150_000), vnd(t, 100_000))
	if err != nil || sum.Amount() != 550_000 {
		t.Fatalf("Sum = %v, %v; want 550000", sum, err)
	}
	diff, err := sum.Sub(vnd(t, 120_000))
	if err != nil || diff.Amount() != 430_000 {
		t.Fatalf("Sub = %v, %v; want 430000", diff, err)
	}
	prod, err := vnd(t, 80_000).Mul(3)
	if err != nil || prod.Amount() != 240_000 {
		t.Fatalf("Mul = %v, %v; want 240000", prod, err)
	}
}

func TestRejectsUnsupportedOrMissingCurrency(t *testing.T) {
	if _, err := money.New(1, "USD"); !errors.Is(err, money.ErrUnsupportedCurrency) {
		t.Fatalf("New USD err = %v", err)
	}
	if _, err := vnd(t, 1).Add(money.Money{}); err == nil {
		t.Fatal("adding zero-value Money without currency must fail")
	}
}

func TestRangeAndOverflow(t *testing.T) {
	if _, err := money.New(money.MaxAmount+1, money.VND); !errors.Is(err, money.ErrOutOfRange) {
		t.Fatalf("New above max err = %v", err)
	}
	if _, err := vnd(t, money.MaxAmount).Add(vnd(t, 1)); !errors.Is(err, money.ErrOutOfRange) {
		t.Fatalf("Add above max err = %v", err)
	}
	if _, err := vnd(t, 10).Mul(math.MaxInt64); !errors.Is(err, money.ErrOutOfRange) {
		t.Fatalf("Mul overflow err = %v", err)
	}
}

func TestJSONRoundTripRevalidates(t *testing.T) {
	m, _ := money.New(550_000, money.VND)
	b, err := json.Marshal(struct {
		Set   money.Money
		Unset money.Money
		Ptr   *money.Money
	}{Set: m})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"Set":{"amount":550000,"currency":"VND"},"Unset":null,"Ptr":null}` {
		t.Fatalf("json = %s", b)
	}
	var back struct {
		Set   money.Money
		Unset money.Money
	}
	if err := json.Unmarshal(b, &back); err != nil || !back.Set.Equal(m) || back.Unset != (money.Money{}) {
		t.Fatalf("back = %+v, %v", back, err)
	}
	var bad money.Money
	if err := json.Unmarshal([]byte(`{"amount":1,"currency":"USD"}`), &bad); !errors.Is(err, money.ErrUnsupportedCurrency) {
		t.Fatalf("USD err = %v", err)
	}
}
