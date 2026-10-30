package payments

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

// ReduceRent lowers the rent charge for a service failure, e.g. an accepted
// delivery complaint refunded instead of replaced (decision D20). The current
// rent charge is reversed and a new one recorded; what the customer overpaid
// then shows as refundable rental and goes through RequestRefund. Repeating
// the same ref is a no-op.
func (l *Ledger) ReduceRent(ref string, chargeID ChargeID, newRent money.Money, reason string, actor identity.Actor, now time.Time) (bool, error) {
	if strings.TrimSpace(ref) == "" || strings.TrimSpace(reason) == "" {
		return false, ErrReasonRequired
	}
	if slices.Contains(l.RentReductions, ref) {
		return false, nil
	}
	if !actor.IsUser() {
		return false, ErrActorRequired
	}
	current := l.activeCharge(ChargeRent)
	if newRent.Currency() != l.Currency || newRent.IsNegative() || newRent.Amount() >= current {
		return false, fmt.Errorf("new rent %s vs %d: %w", newRent, current, ErrInvalidAmount)
	}
	if chargeID == "" || slices.ContainsFunc(l.Charges, func(c Charge) bool { return c.ID == chargeID }) {
		return false, domainerr.Invalid("charge_id", "required and unique")
	}
	for i := range l.Charges {
		if l.Charges[i].Kind == ChargeRent && l.Charges[i].Reversal == nil {
			l.Charges[i].Reversal = &Reversal{Reason: reason, By: actor, At: now.UTC()}
		}
	}
	if newRent.IsPositive() {
		l.Charges = append(l.Charges, Charge{ID: chargeID, Kind: ChargeRent, Amount: newRent, Reason: reason, CreatedBy: actor, CreatedAt: now.UTC()})
	}
	l.RentReductions = append(l.RentReductions, ref)
	return true, nil
}
