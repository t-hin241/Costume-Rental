// Package auditpg appends audit events to PostgreSQL. The table rejects
// updates and deletes, so the log cannot be edited through the application.
package auditpg

import (
	"context"
	"encoding/json"
	"fmt"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/platform/postgres"
)

// Append validates and writes one event in the caller's transaction, so the
// audit record commits or rolls back with the change it describes.
func Append(ctx context.Context, q postgres.Querier, e audit.Event) error {
	e, err := audit.NewEvent(e)
	if err != nil {
		return err
	}
	refs, err := json.Marshal(nonNil(e.References))
	if err != nil {
		return fmt.Errorf("audit %s refs: %w", e.ID, err)
	}
	changes := e.Changes
	if changes == nil {
		changes = map[string]audit.Change{}
	}
	ch, err := json.Marshal(changes)
	if err != nil {
		return fmt.Errorf("audit %s changes: %w", e.ID, err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO audit_events (id, occurred_at, actor_kind, actor_id, action, resource_type, resource_id,
			result, reason, refs, changes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		e.ID, e.OccurredAt, e.Actor.Kind, e.Actor.ID, e.Action, e.ResourceType, e.ResourceID,
		e.Result, e.Reason, string(refs), string(ch))
	if err != nil {
		return fmt.Errorf("append audit %s: %w", e.Action, err)
	}
	return nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
