// Package identityapp runs the identity use cases against PostgreSQL:
// resolving the principal of a request and publishing permission matrix
// versions.
package identityapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
)

// Service holds the dependencies of the identity use cases.
type Service struct {
	pool  *pgxpool.Pool
	now   func() time.Time
	newID func() string
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New builds the service.
func New(pool *pgxpool.Pool, opts ...Option) *Service {
	s := &Service{pool: pool, now: time.Now, newID: ids.New}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Principal resolves the authenticated user's permissions from the active
// matrix and the user's current roles. Nothing is cached, so a role change,
// a lock or a new matrix applies to the very next request.
func (s *Service) Principal(ctx context.Context, id identity.UserID) (identity.Principal, error) {
	var p identity.Principal
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		m, _, err := identitypg.ActiveMatrix(ctx, tx)
		if err != nil {
			return err
		}
		p, err = identitypg.LoadPrincipal(ctx, tx, id, m)
		return err
	})
	if err != nil {
		return identity.Principal{}, fmt.Errorf("principal %s: %w", id, err)
	}
	return p, nil
}

// requireMatrixAdmin: changing who may do what needs both policy and role
// management, which the guardrails keep admin-only.
func requireMatrixAdmin(p identity.Principal) error {
	if err := p.Require(identity.PermPolicyManage); err != nil {
		return err
	}
	return p.Require(identity.PermUserRoleManage)
}

// PermissionMatrix returns the active matrix and its version.
func (s *Service) PermissionMatrix(ctx context.Context, p identity.Principal) (identity.PermissionMatrix, string, error) {
	if err := requireMatrixAdmin(p); err != nil {
		return nil, "", err
	}
	return identitypg.ActiveMatrix(ctx, s.pool)
}

// PublishMatrix stores m as a new immutable version and activates it. The
// guardrails are checked first; the audit event lists, per role, the
// permissions before and after.
func (s *Service) PublishMatrix(ctx context.Context, p identity.Principal, version string, m identity.PermissionMatrix, reason string) error {
	if err := requireMatrixAdmin(p); err != nil {
		return err
	}
	if strings.TrimSpace(version) == "" {
		return domainerr.Invalid("version", "required")
	}
	if strings.TrimSpace(reason) == "" {
		return identity.ErrReasonRequired
	}
	if err := m.Validate(); err != nil {
		return err
	}
	m = m.Normalized()
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise publishers on the active row.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM policy_documents WHERE kind = $1 AND active FOR UPDATE`, postgres.DocPermissionMatrix); err != nil {
			return fmt.Errorf("lock active matrix: %w", err)
		}
		old, oldVersion, err := identitypg.ActiveMatrix(ctx, tx)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := postgres.SaveDocument(ctx, tx, postgres.DocPermissionMatrix, version, m, p.Actor(), now, true); err != nil {
			return err
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "identity.permission_matrix_publish",
			ResourceType: "permission_matrix", ResourceID: version, Result: audit.ResultSuccess, Reason: reason,
			References: map[string]string{"previous_version": oldVersion},
			Changes:    diff(old.Normalized(), m),
		})
	})
	if err != nil {
		return fmt.Errorf("publish permission matrix %s: %w", version, err)
	}
	return nil
}

// diff lists the roles whose permissions changed.
func diff(old, cur identity.PermissionMatrix) map[string]audit.Change {
	out := map[string]audit.Change{}
	for role, perms := range cur {
		if !slices.Equal(old[role], perms) {
			out["role:"+string(role)] = audit.Change{From: join(old[role]), To: join(perms)}
		}
	}
	return out
}

func join(perms []identity.Permission) string {
	s := make([]string, len(perms))
	for i, p := range perms {
		s[i] = string(p)
	}
	return strings.Join(s, ",")
}
