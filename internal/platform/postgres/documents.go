package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"webee_rentor/internal/identity"
)

// DocumentKind names a versioned policy document in policy_documents.
type DocumentKind string

const (
	DocRentalPolicy     DocumentKind = "RENTAL"
	DocAllocationPolicy DocumentKind = "INVENTORY_ALLOCATION"
	DocPermissionMatrix DocumentKind = "PERMISSION_MATRIX"
	DocPaymentApproval  DocumentKind = "PAYMENT_APPROVAL"
)

// SaveDocument stores a new immutable policy version and, when activate is
// set, makes it the active one in the same transaction. Saving an existing
// version again is a no-op if the body is the same and ErrDuplicate if not.
func SaveDocument(ctx context.Context, q Querier, kind DocumentKind, version string, doc any, actor identity.Actor, now time.Time, activate bool) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode %s policy %s: %w", kind, version, err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO policy_documents (kind, version, document, created_at, created_by_kind, created_by_id)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (kind, version) DO NOTHING`,
		kind, version, string(body), now.UTC(), actor.Kind, actor.ID); err != nil {
		return fmt.Errorf("save %s policy %s: %w", kind, version, err)
	}
	var same bool
	if err := q.QueryRow(ctx, `SELECT document = $3::jsonb FROM policy_documents WHERE kind = $1 AND version = $2`,
		kind, version, string(body)).Scan(&same); err != nil {
		return fmt.Errorf("check %s policy %s: %w", kind, version, err)
	}
	if !same {
		return fmt.Errorf("%s policy %s already exists with another body: %w", kind, version, ErrDuplicate)
	}
	if !activate {
		return nil
	}
	if _, err := q.Exec(ctx, `UPDATE policy_documents SET active = false WHERE kind = $1 AND active AND version <> $2`, kind, version); err != nil {
		return fmt.Errorf("deactivate %s policies: %w", kind, err)
	}
	if _, err := q.Exec(ctx, `UPDATE policy_documents SET active = true WHERE kind = $1 AND version = $2`, kind, version); err != nil {
		return fmt.Errorf("activate %s policy %s: %w", kind, version, err)
	}
	return nil
}

// ActiveDocument decodes the active version of kind into dst and returns
// the version.
func ActiveDocument(ctx context.Context, q Querier, kind DocumentKind, dst any) (string, error) {
	var version string
	var body []byte
	err := q.QueryRow(ctx, `SELECT version, document FROM policy_documents WHERE kind = $1 AND active`, kind).Scan(&version, &body)
	if err != nil {
		return "", NotFound(err, fmt.Sprintf("active %s policy", kind))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return "", fmt.Errorf("decode %s policy %s: %w", kind, version, err)
	}
	return version, nil
}
