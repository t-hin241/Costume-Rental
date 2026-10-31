package identityapp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/postgres"
)

// Profile reads the principal's own contact details.
func (s *Service) Profile(ctx context.Context, p identity.Principal) (identity.Contact, error) {
	people, err := identitypg.Contacts(ctx, s.pool, []identity.UserID{p.UserID})
	if err != nil {
		return identity.Contact{}, err
	}
	c, ok := people[p.UserID]
	if !ok {
		return identity.Contact{}, fmt.Errorf("profile of %s: %w", p.UserID, postgres.ErrNotFound)
	}
	return c, nil
}

// SaveProfile updates the principal's own name and phone. Only that the
// change happened is audited: the values are personal data.
func (s *Service) SaveProfile(ctx context.Context, p identity.Principal, name, phone string) (identity.Contact, error) {
	name, phone = strings.TrimSpace(name), strings.TrimSpace(phone)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		return identity.Contact{}, domainerr.Invalid("full_name", "required, at most 120 characters")
	}
	if !identity.ValidPhone(phone) {
		return identity.Contact{}, domainerr.Invalid("phone", "must be a phone number")
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		if err := identitypg.SaveProfile(ctx, tx, p.UserID, name, phone, now); err != nil {
			return err
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "user.profile_updated",
			ResourceType: "user", ResourceID: string(p.UserID), Result: audit.ResultSuccess,
		})
	})
	if err != nil {
		return identity.Contact{}, fmt.Errorf("save profile: %w", err)
	}
	return s.Profile(ctx, p)
}

// Users lists accounts for an administrator.
func (s *Service) Users(ctx context.Context, p identity.Principal, f identitypg.UserFilter) ([]identitypg.UserRow, int, error) {
	if err := p.Require(identity.PermUserRoleManage); err != nil {
		return nil, 0, err
	}
	return identitypg.UserPage(ctx, s.pool, f)
}
