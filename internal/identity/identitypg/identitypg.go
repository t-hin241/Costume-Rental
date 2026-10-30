// Package identitypg reads users, roles and the permission matrix from
// PostgreSQL.
package identitypg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
)

// ActiveMatrix returns the active permission matrix and its version. A
// stored matrix that breaks the guardrails (e.g. edited by hand in the
// database) is refused rather than trusted.
func ActiveMatrix(ctx context.Context, q postgres.Querier) (identity.PermissionMatrix, string, error) {
	var m identity.PermissionMatrix
	version, err := postgres.ActiveDocument(ctx, q, postgres.DocPermissionMatrix, &m)
	if err != nil {
		return nil, "", err
	}
	if err := m.Validate(); err != nil {
		return nil, "", fmt.Errorf("permission matrix %s: %w", version, err)
	}
	return m, version, nil
}

// LoadPrincipal resolves a user's current roles under matrix. Unknown users
// are unauthenticated; locked users are refused.
func LoadPrincipal(ctx context.Context, q postgres.Querier, id identity.UserID, matrix identity.PermissionMatrix) (identity.Principal, error) {
	var status identity.UserStatus
	err := q.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Principal{}, identity.ErrNotPrincipal
	}
	if err != nil {
		return identity.Principal{}, fmt.Errorf("load user %s: %w", id, err)
	}
	if status != identity.UserActive {
		return identity.Principal{}, fmt.Errorf("user %s: %w", id, identity.ErrAccountLocked)
	}
	rows, err := q.Query(ctx, `SELECT role FROM user_roles WHERE user_id = $1 ORDER BY role`, id)
	if err != nil {
		return identity.Principal{}, fmt.Errorf("load roles of %s: %w", id, err)
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[identity.Role])
	if err != nil {
		return identity.Principal{}, fmt.Errorf("load roles of %s: %w", id, err)
	}
	return identity.NewPrincipal(id, roles, matrix)
}
