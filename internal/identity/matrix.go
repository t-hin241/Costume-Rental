package identity

import (
	"errors"
	"fmt"
	"slices"

	"webee_rentor/internal/domainerr"
)

var (
	ErrInvalidMatrix = domainerr.New(domainerr.KindInvalid, "identity.invalid_permission_matrix", "permission matrix violates a security guardrail")
	ErrAccountLocked = domainerr.New(domainerr.KindForbidden, "identity.account_locked", "the account is locked")
)

// customerPermissions are the only permissions the customer role may hold:
// actions on the customer's own records.
var customerPermissions = []Permission{
	PermBookingCreateOwn, PermBookingReadOwn, PermBookingCancelOwn, PermBookingRescheduleOwn,
	PermBookingModifyOwn, PermPaymentReadOwn, PermDeliveryReadOwn, PermComplaintCreateOwn,
}

// adminOnly are account, role, policy and tax management. Only admin may
// hold them, and admin must keep them so the matrix can never lock every
// administrator out.
var adminOnly = []Permission{PermUserRoleManage, PermUserLock, PermPolicyManage, PermTaxManage}

// approvals are money decisions front-line roles never hold, so whoever
// records a refund, deduction or fee is not the one who approves, waives
// or reverses it (decision D22: only finance and admin approve).
var approvals = []Permission{PermRefundApprove, PermDeductionApprove, PermFeeApprove, PermFeeApproveHigh, PermFeeWaive, PermChargeReverse}

var frontLine = []Role{RoleRentalStaff, RoleInventoryManager}

// Validate checks the guardrails no configuration may relax: known roles
// and permissions only, customers limited to their own records, admin-only
// management, and no approval rights for front-line roles.
func (m PermissionMatrix) Validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format+": %w", append(args, ErrInvalidMatrix)...))
	}
	for role, perms := range m {
		if !role.Valid() {
			fail("role %q is unknown", role)
			continue
		}
		seen := map[Permission]bool{}
		for _, p := range perms {
			switch {
			case !slices.Contains(allPermissions, p):
				fail("role %s: permission %q is unknown", role, p)
			case seen[p]:
				fail("role %s: permission %s is listed twice", role, p)
			case role == RoleCustomer && !slices.Contains(customerPermissions, p):
				fail("customer may only act on own records, not %s", p)
			case role != RoleAdmin && slices.Contains(adminOnly, p):
				fail("role %s: %s is admin-only", role, p)
			case slices.Contains(frontLine, role) && slices.Contains(approvals, p):
				fail("role %s must not approve (%s)", role, p)
			}
			seen[p] = true
		}
	}
	for _, p := range adminOnly {
		if !slices.Contains(m[RoleAdmin], p) {
			fail("admin must keep %s", p)
		}
	}
	return errors.Join(errs...)
}

// Normalized returns a copy with every role present and permissions sorted,
// so equal matrices store and compare identically.
func (m PermissionMatrix) Normalized() PermissionMatrix {
	out := PermissionMatrix{}
	for _, r := range []Role{RoleCustomer, RoleRentalStaff, RoleInventoryManager, RoleFinance, RoleAdmin} {
		perms := slices.Clone(m[r])
		slices.Sort(perms)
		if perms == nil {
			perms = []Permission{}
		}
		out[r] = perms
	}
	return out
}
