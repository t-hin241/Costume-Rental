package auditpg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
)

// Filter narrows the audit list; zero fields do not filter.
type Filter struct {
	Term     string
	Result   audit.Result
	Page     int
	PageSize int
}

// Page lists events, newest first, with the total matching.
func Page(ctx context.Context, q postgres.Querier, f Filter) ([]audit.Event, int, error) {
	term := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Term)
	rows, err := q.Query(ctx, `
		SELECT e.id, e.occurred_at, e.actor_kind, e.actor_id, e.action, e.resource_type, e.resource_id, e.result, e.reason, e.refs, e.changes,
			count(*) OVER ()
		FROM audit_events e
		LEFT JOIN users u ON e.actor_kind = 'user' AND u.id = e.actor_id
		LEFT JOIN customer_profiles p ON p.user_id = u.id
		WHERE ($1 = '' OR e.result = $1)
			AND ($2 = '' OR e.action ILIKE '%' || $2 || '%' OR e.resource_id ILIKE '%' || $2 || '%'
				OR u.email ILIKE '%' || $2 || '%' OR p.full_name ILIKE '%' || $2 || '%')
		ORDER BY e.occurred_at DESC, e.id
		LIMIT $3 OFFSET $4`, string(f.Result), term, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []audit.Event
	total := 0
	for rows.Next() {
		var e audit.Event
		var refs, changes []byte
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Actor.Kind, &e.Actor.ID, &e.Action, &e.ResourceType, &e.ResourceID, &e.Result, &e.Reason, &refs, &changes, &total); err != nil {
			return nil, 0, fmt.Errorf("list audit events: %w", err)
		}
		e.OccurredAt = e.OccurredAt.UTC()
		if err := json.Unmarshal(refs, &e.References); err != nil {
			return nil, 0, fmt.Errorf("audit %s refs: %w", e.ID, err)
		}
		if err := json.Unmarshal(changes, &e.Changes); err != nil {
			return nil, 0, fmt.Errorf("audit %s changes: %w", e.ID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}
	return out, total, nil
}

// ActorIDs lists the users behind the events.
func ActorIDs(events []audit.Event) []identity.UserID {
	out := make([]identity.UserID, 0, len(events))
	for _, e := range events {
		if e.Actor.IsUser() {
			out = append(out, identity.UserID(e.Actor.ID))
		}
	}
	return out
}
