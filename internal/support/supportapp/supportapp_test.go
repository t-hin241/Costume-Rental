package supportapp_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/support"
	"webee_rentor/internal/support/supportapp"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

// D36: support.handle stands on its own. Reading bookings no longer lets anyone
// into the conversations, and answering them needs no access to bookings.
func TestSupportHasItsOwnPermission(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	svc := supportapp.New(f.Pool)
	asked, err := svc.Create(ctx, f.User("customer", identity.RoleCustomer), supportapp.CreateInput{Topic: support.TopicOther, Subject: "Hỏi", Message: "Xin chào"})
	if err != nil {
		t.Fatal(err)
	}

	matrix := identity.ReferencePermissionMatrix()
	matrix[identity.RoleFinance] = slices.DeleteFunc(matrix[identity.RoleFinance], func(p identity.Permission) bool { return p == identity.PermSupportHandle })
	matrix[identity.RoleInventoryManager] = append(matrix[identity.RoleInventoryManager], identity.PermSupportHandle)
	who := func(name string, role identity.Role) identity.Principal {
		p, err := identity.NewPrincipal(f.User(name, role).UserID, []identity.Role{role}, matrix)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	bookingsOnly := who("bookings", identity.RoleFinance)
	if !bookingsOnly.Can(identity.PermBookingReadAll) {
		t.Fatal("the role must still read bookings for this to prove anything")
	}
	if _, err := svc.All(ctx, bookingsOnly); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("list without support.handle: err = %v", err)
	}
	if _, err := svc.Reply(ctx, bookingsOnly, asked.ID, "Trả lời", false); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("reply without support.handle: err = %v", err)
	}

	agent := who("agent", identity.RoleInventoryManager)
	if agent.Can(identity.PermBookingReadAll) {
		t.Fatal("the role must not read bookings for this to prove anything")
	}
	if all, err := svc.All(ctx, agent); err != nil || !slices.ContainsFunc(all, func(v supportapp.View) bool { return v.ID == asked.ID }) {
		t.Fatalf("list with support.handle = %+v, %v", all, err)
	}
	if _, err := svc.Reply(ctx, agent, asked.ID, "Trả lời", false); err != nil {
		t.Fatalf("reply with support.handle: %v", err)
	}
}
