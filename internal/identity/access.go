// Package identity owns users, roles, permissions and the authenticated
// principal. Authorization is deny-by-default RBAC plus ownership checks.
package identity

import (
	"fmt"
	"slices"

	"webee_rentor/internal/domainerr"
)

// UserID identifies a user. It is always derived from the authenticated
// session, never taken from a client payload.
type UserID string

// Role groups permissions. A user may hold several roles.
type Role string

const (
	RoleCustomer         Role = "customer"
	RoleRentalStaff      Role = "rental_staff"
	RoleInventoryManager Role = "inventory_manager"
	RoleFinance          Role = "finance"
	RoleAdmin            Role = "admin" // owner/admin
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleCustomer, RoleRentalStaff, RoleInventoryManager, RoleFinance, RoleAdmin:
		return true
	}
	return false
}

// Permission is a single action. Role checks never replace ownership checks.
type Permission string

const (
	PermCatalogManage Permission = "catalog.manage"

	PermInventoryRead   Permission = "inventory.read"
	PermInventoryManage Permission = "inventory.manage"

	PermBookingCreateOwn     Permission = "booking.create_own"
	PermBookingReadOwn       Permission = "booking.read_own"
	PermBookingReadAll       Permission = "booking.read_all"
	PermBookingCancelOwn     Permission = "booking.cancel_own"
	PermBookingCancelAny     Permission = "booking.cancel_any"
	PermBookingRescheduleOwn Permission = "booking.reschedule_own"
	PermBookingRescheduleAny Permission = "booking.reschedule_any"
	PermBookingModifyOwn     Permission = "booking.modify_own" // size/bundle/package change requests
	PermBookingModifyAny     Permission = "booking.modify_any"
	PermBookingConfirm       Permission = "booking.confirm"
	PermBookingComplete      Permission = "booking.complete"

	PermRentalPrepare       Permission = "rental.prepare"
	PermRentalHandover      Permission = "rental.handover"
	PermRentalReturnReceive Permission = "rental.return_receive"
	PermRentalReturnInspect Permission = "rental.return_inspect"
	// PermRentalConfirmLost approves turning a missing item into a loss
	// after the verification window.
	PermRentalConfirmLost Permission = "rental.confirm_lost"

	// Delivery tasks.
	PermDeliveryReadOwn Permission = "delivery.read_own"
	PermDeliveryReadAll Permission = "delivery.read_all"
	PermDeliveryManage  Permission = "delivery.manage" // retry, cancel, manual events, classify attempts, handle complaints
	// PermComplaintCreateOwn lets a customer report a delivery problem.
	PermComplaintCreateOwn Permission = "complaint.create_own"

	// Fee assessments (late return, cleaning, damage, loss).
	PermFeeAssess      Permission = "fee.assess"       // record/propose fees and disputes
	PermFeeApprove     Permission = "fee.approve"      // approve/reject fees, resolve disputes
	PermFeeApproveHigh Permission = "fee.approve_high" // approve fees above the manager threshold
	PermFeeWaive       Permission = "fee.waive"        // waive or reduce an approved fee

	PermPaymentReadOwn   Permission = "payment.read_own"
	PermPaymentRead      Permission = "payment.read"
	PermPaymentRecord    Permission = "payment.record" // cash/bank transfer at the counter
	PermChargeCreate     Permission = "charge.create"  // assess late/damage/loss fees
	PermChargeReverse    Permission = "charge.reverse"
	PermRefundCreate     Permission = "refund.create"
	PermRefundApprove    Permission = "refund.approve"
	PermDeductionCreate  Permission = "deduction.create"
	PermDeductionApprove Permission = "deduction.approve"

	// Pricing, promotions and tax.
	PermPromotionManage     Permission = "promotion.manage"      // create/change discount campaigns and coupons
	PermPricingManualAdjust Permission = "pricing.manual_adjust" // manual price adjustments
	PermTaxManage           Permission = "tax.manage"            // tax profile, rates, category treatment
	PermRevenueRead         Permission = "revenue.read"

	PermUserRoleManage Permission = "user.role_manage"
	PermUserLock       Permission = "user.lock"
	PermAuditRead      Permission = "audit.read"
	PermPolicyManage   Permission = "policy.manage"
)

// PermissionMatrix maps roles to the permissions they grant.
type PermissionMatrix map[Role][]Permission

// allPermissions lists every permission the system knows; a matrix may
// only grant these.
var allPermissions = []Permission{
	PermCatalogManage, PermInventoryRead, PermInventoryManage,
	PermBookingCreateOwn, PermBookingReadOwn, PermBookingReadAll,
	PermBookingCancelOwn, PermBookingCancelAny,
	PermBookingRescheduleOwn, PermBookingRescheduleAny,
	PermBookingModifyOwn, PermBookingModifyAny,
	PermBookingConfirm, PermBookingComplete,
	PermRentalPrepare, PermRentalHandover, PermRentalReturnReceive, PermRentalReturnInspect,
	PermRentalConfirmLost, PermDeliveryReadOwn, PermDeliveryReadAll, PermDeliveryManage, PermComplaintCreateOwn, PermFeeAssess, PermFeeApprove, PermFeeApproveHigh, PermFeeWaive,
	PermPaymentReadOwn, PermPaymentRead, PermPaymentRecord,
	PermChargeCreate, PermChargeReverse,
	PermRefundCreate, PermRefundApprove, PermDeductionCreate, PermDeductionApprove,
	PermPromotionManage, PermPricingManualAdjust, PermTaxManage, PermRevenueRead,
	PermUserRoleManage, PermUserLock, PermAuditRead, PermPolicyManage,
}

// ReferencePermissionMatrix is the owner-approved default matrix (decision
// D22, based on docs/about_costume_rental.md §10). Production reads the
// active, versioned matrix from configuration; migration 000009 seeds it
// with exactly this content.
func ReferencePermissionMatrix() PermissionMatrix {
	return PermissionMatrix{
		RoleCustomer: {
			PermBookingCreateOwn, PermBookingReadOwn, PermBookingCancelOwn,
			PermBookingRescheduleOwn, PermBookingModifyOwn, PermPaymentReadOwn, PermDeliveryReadOwn, PermComplaintCreateOwn,
		},
		RoleRentalStaff: {
			PermBookingReadAll, PermBookingConfirm, PermBookingCancelAny,
			PermBookingRescheduleAny, PermBookingModifyAny, PermBookingComplete,
			PermRentalPrepare, PermRentalHandover, PermRentalReturnReceive, PermRentalReturnInspect,
			PermInventoryRead, PermPaymentRecord, PermChargeCreate,
			PermRefundCreate, PermDeductionCreate, PermFeeAssess, PermDeliveryReadAll, PermDeliveryManage,
		},
		RoleInventoryManager: {
			PermCatalogManage, PermInventoryRead, PermInventoryManage, PermRentalReturnInspect,
			PermFeeAssess,
		},
		RoleFinance: {
			PermBookingReadAll, PermPaymentRead, PermPaymentRecord, PermChargeReverse,
			PermRefundCreate, PermRefundApprove, PermDeductionApprove,
			PermFeeApprove, PermFeeWaive, PermRevenueRead, PermAuditRead,
		},
		RoleAdmin: slices.Clone(allPermissions),
	}
}

var (
	ErrForbidden    = domainerr.New(domainerr.KindForbidden, "identity.forbidden", "actor is not allowed to perform this action")
	ErrUnknownRole  = domainerr.New(domainerr.KindInvalid, "identity.unknown_role", "role is not recognised")
	ErrNotPrincipal = domainerr.New(domainerr.KindForbidden, "identity.unauthenticated", "an authenticated user is required")
)

// Principal is the authenticated user making a request.
type Principal struct {
	UserID UserID
	Roles  []Role
	perms  map[Permission]struct{}
}

// NewPrincipal resolves the permissions granted to roles by matrix.
func NewPrincipal(userID UserID, roles []Role, matrix PermissionMatrix) (Principal, error) {
	if userID == "" {
		return Principal{}, ErrNotPrincipal
	}
	perms := make(map[Permission]struct{})
	for _, r := range roles {
		if !r.Valid() {
			return Principal{}, fmt.Errorf("principal role %q: %w", r, ErrUnknownRole)
		}
		for _, p := range matrix[r] {
			perms[p] = struct{}{}
		}
	}
	return Principal{UserID: userID, Roles: slices.Clone(roles), perms: perms}, nil
}

// Can reports whether the principal holds perm.
func (p Principal) Can(perm Permission) bool {
	_, ok := p.perms[perm]
	return ok
}

// Permissions lists the granted permissions, sorted. Clients may use it to
// shape the UI; the server still checks every action.
func (p Principal) Permissions() []Permission {
	out := make([]Permission, 0, len(p.perms))
	for perm := range p.perms {
		out = append(out, perm)
	}
	slices.Sort(out)
	return out
}

// HasRole reports whether the principal holds r.
func (p Principal) HasRole(r Role) bool { return slices.Contains(p.Roles, r) }

// Require fails with ErrForbidden unless the principal holds perm.
func (p Principal) Require(perm Permission) error {
	if p.UserID == "" {
		return ErrNotPrincipal
	}
	if !p.Can(perm) {
		return fmt.Errorf("missing %s: %w", perm, ErrForbidden)
	}
	return nil
}

// Scope tells a use case whether the principal acts on its own resource or
// with staff-wide permission, which changes the business rules applied.
type Scope string

const (
	ScopeOwn Scope = "own"
	ScopeAny Scope = "any"
)

// RequireOwnOrAny allows access with the "any" permission, or with the "own"
// permission when the principal owns the resource.
func (p Principal) RequireOwnOrAny(own, anyPerm Permission, owner UserID) (Scope, error) {
	if p.UserID == "" {
		return "", ErrNotPrincipal
	}
	if p.Can(anyPerm) {
		return ScopeAny, nil
	}
	if p.Can(own) && owner != "" && owner == p.UserID {
		return ScopeOwn, nil
	}
	return "", fmt.Errorf("missing %s or ownership: %w", anyPerm, ErrForbidden)
}

// Actor returns the audit actor for this principal.
func (p Principal) Actor() Actor { return UserActor(p.UserID) }

// ActorKind distinguishes humans from background jobs and webhooks.
type ActorKind string

const (
	ActorUser   ActorKind = "user"
	ActorSystem ActorKind = "system"
)

// Actor is who performed an action, recorded on every state change.
type Actor struct {
	Kind ActorKind
	// ID is the user ID for users or a job/integration name for systems.
	ID string
}

// UserActor builds an actor for a user.
func UserActor(id UserID) Actor { return Actor{Kind: ActorUser, ID: string(id)} }

// SystemActor builds an actor for a job or integration, e.g. "hold-expiry".
func SystemActor(name string) Actor { return Actor{Kind: ActorSystem, ID: name} }

// Valid reports whether the actor is fully specified.
func (a Actor) Valid() bool {
	return (a.Kind == ActorUser || a.Kind == ActorSystem) && a.ID != ""
}

func (a Actor) IsUser() bool   { return a.Kind == ActorUser && a.ID != "" }
func (a Actor) IsSystem() bool { return a.Kind == ActorSystem && a.ID != "" }
