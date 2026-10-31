package rentals

import (
	"fmt"

	"webee_rentor/internal/identity"
)

type actionPermissions struct {
	own identity.Permission // empty when the action is staff-only
	any identity.Permission
}

var bookingPermissions = map[Action]actionPermissions{
	ActionRead:             {own: identity.PermBookingReadOwn, any: identity.PermBookingReadAll},
	ActionCancel:           {own: identity.PermBookingCancelOwn, any: identity.PermBookingCancelAny},
	ActionReschedule:       {own: identity.PermBookingRescheduleOwn, any: identity.PermBookingRescheduleAny},
	ActionModify:           {own: identity.PermBookingModifyOwn, any: identity.PermBookingModifyAny},
	ActionConfirm:          {any: identity.PermBookingConfirm},
	ActionStartPreparation: {any: identity.PermRentalPrepare},
	ActionReplaceAsset:     {any: identity.PermRentalPrepare},
	ActionHandover:         {any: identity.PermRentalHandover},
	ActionReturn:           {any: identity.PermRentalReturnReceive},
	ActionReportMissing:    {any: identity.PermRentalReturnReceive},
	ActionConfirmLost:      {any: identity.PermRentalConfirmLost},
	ActionDelivery:         {any: identity.PermDeliveryManage},
	ActionComplain:         {own: identity.PermComplaintCreateOwn, any: identity.PermDeliveryManage},
	ActionInspectAsset:     {any: identity.PermRentalReturnInspect},
	ActionComplete:         {any: identity.PermBookingComplete},
}

// AuthorizeCreate checks a principal may book for themself. The customer of
// the new booking is always the principal; it is never read from input.
func AuthorizeCreate(p identity.Principal) error {
	return p.Require(identity.PermBookingCreateOwn)
}

// Authorize checks function- and object-level access for an action on b and
// returns the scope the use case must pass to scope-aware methods.
func Authorize(p identity.Principal, action Action, b *Booking) (identity.Scope, error) {
	perms, ok := bookingPermissions[action]
	if !ok {
		return "", fmt.Errorf("action %s: %w", action, identity.ErrForbidden)
	}
	if perms.own == "" {
		if err := p.Require(perms.any); err != nil {
			return "", err
		}
		return identity.ScopeAny, nil
	}
	return p.RequireOwnOrAny(perms.own, perms.any, b.CustomerID)
}
