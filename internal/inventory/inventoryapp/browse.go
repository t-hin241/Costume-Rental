package inventoryapp

import (
	"context"

	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
)

// AssetView is a list row with what the reader may do to the asset.
type AssetView struct {
	inventorypg.AssetRow
	Actions []inventory.AssetAction
}

// HistoryView is an asset movement with who made it.
type HistoryView struct {
	inventorypg.HistoryRow
	ActorName string
}

// actionOrder is how actions are offered, most common first.
var actionOrder = []inventory.AssetAction{
	inventory.ActionPassInspection, inventory.ActionRequireCleaning, inventory.ActionCompleteCleaning, inventory.ActionCompleteRepair,
	inventory.ActionRecordFound, inventory.ActionSendToRepair, inventory.ActionReportMissing, inventory.ActionRetire,
}

// actionsFor lists what p may do to the asset now. A rented asset offers
// nothing here: its booking owns what happens to it.
func actionsFor(a inventory.Asset, p identity.Principal) []inventory.AssetAction {
	out := []inventory.AssetAction{}
	if a.Status == inventory.AssetRented {
		return out
	}
	for _, action := range actionOrder {
		if a.Allows(action) && authorize(p, action) == nil {
			out = append(out, action)
		}
	}
	return out
}

// Assets lists assets for anyone with inventory.read.
func (s *Service) Assets(ctx context.Context, p identity.Principal, f inventorypg.AssetFilter) ([]AssetView, int, error) {
	if err := p.Require(identity.PermInventoryRead); err != nil {
		return nil, 0, err
	}
	rows, total, err := inventorypg.AssetPage(ctx, s.pool, f)
	if err != nil {
		return nil, 0, err
	}
	out := make([]AssetView, len(rows))
	for i, r := range rows {
		out[i] = AssetView{AssetRow: r, Actions: actionsFor(r.Asset, p)}
	}
	return out, total, nil
}

// History reads an asset's movements.
func (s *Service) History(ctx context.Context, p identity.Principal, id inventory.AssetID) ([]HistoryView, error) {
	if err := p.Require(identity.PermInventoryRead); err != nil {
		return nil, err
	}
	rows, err := inventorypg.History(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	var users []identity.UserID
	for _, r := range rows {
		if r.Actor.IsUser() {
			users = append(users, identity.UserID(r.Actor.ID))
		}
	}
	people, err := identitypg.Contacts(ctx, s.pool, users)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryView, len(rows))
	for i, r := range rows {
		name := r.Actor.ID
		if r.Actor.IsUser() {
			name = people[identity.UserID(r.Actor.ID)].Label()
		}
		out[i] = HistoryView{HistoryRow: r, ActorName: name}
	}
	return out, nil
}

// RegisterByRef adds an asset, naming the variant by id or SKU.
func (s *Service) RegisterByRef(ctx context.Context, p identity.Principal, code, variantRef string) (inventory.Asset, error) {
	if err := p.Require(identity.PermInventoryManage); err != nil {
		return inventory.Asset{}, err
	}
	id, err := catalogpg.VariantIDByRef(ctx, s.pool, variantRef)
	if err != nil {
		return inventory.Asset{}, err
	}
	return s.RegisterAsset(ctx, p, code, id)
}
