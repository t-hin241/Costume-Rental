// Package inventory owns physical assets (one tracked unit per garment or
// accessory), their physical condition, and allocations that block an asset
// for a time range.
//
// Physical status and allocation are separate axes: a READY asset may already
// be allocated to a future booking, and an expired buffer never makes an
// asset READY by itself.
package inventory

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

type AssetID string

// AssetStatus is the physical condition of an asset.
type AssetStatus string

const (
	AssetReady           AssetStatus = "READY"
	AssetRented          AssetStatus = "RENTED"
	AssetNeedsInspection AssetStatus = "NEEDS_INSPECTION"
	AssetCleaning        AssetStatus = "CLEANING"
	AssetRepair          AssetStatus = "REPAIR"
	AssetMissing         AssetStatus = "MISSING"
	AssetRetired         AssetStatus = "RETIRED"
)

// AssetAction is an intent-named physical transition.
type AssetAction string

const (
	ActionIntake           AssetAction = "asset.intake"
	ActionHandOver         AssetAction = "asset.hand_over"
	ActionReceiveReturn    AssetAction = "asset.receive_return"
	ActionPassInspection   AssetAction = "asset.pass_inspection"
	ActionRequireCleaning  AssetAction = "asset.require_cleaning"
	ActionCompleteCleaning AssetAction = "asset.complete_cleaning"
	ActionSendToRepair     AssetAction = "asset.send_to_repair"
	ActionCompleteRepair   AssetAction = "asset.complete_repair"
	ActionReportMissing    AssetAction = "asset.report_missing"
	ActionRecordFound      AssetAction = "asset.record_found"
	ActionRetire           AssetAction = "asset.retire"
)

type assetRule struct {
	from           []AssetStatus
	to             AssetStatus
	reasonRequired bool
}

// assetRules is the full physical state machine. READY is only reachable by
// passing inspection or completing cleaning; repair and found items go back
// through inspection.
var assetRules = map[AssetAction]assetRule{
	ActionHandOver:         {from: []AssetStatus{AssetReady}, to: AssetRented},
	ActionReceiveReturn:    {from: []AssetStatus{AssetRented}, to: AssetNeedsInspection},
	ActionPassInspection:   {from: []AssetStatus{AssetNeedsInspection}, to: AssetReady},
	ActionRequireCleaning:  {from: []AssetStatus{AssetNeedsInspection}, to: AssetCleaning},
	ActionCompleteCleaning: {from: []AssetStatus{AssetCleaning}, to: AssetReady},
	ActionSendToRepair:     {from: []AssetStatus{AssetReady, AssetNeedsInspection, AssetCleaning}, to: AssetRepair, reasonRequired: true},
	ActionCompleteRepair:   {from: []AssetStatus{AssetRepair}, to: AssetNeedsInspection},
	ActionReportMissing: {
		from:           []AssetStatus{AssetReady, AssetRented, AssetNeedsInspection, AssetCleaning, AssetRepair},
		to:             AssetMissing,
		reasonRequired: true,
	},
	ActionRecordFound: {from: []AssetStatus{AssetMissing}, to: AssetNeedsInspection, reasonRequired: true},
	ActionRetire: {
		from:           []AssetStatus{AssetReady, AssetNeedsInspection, AssetCleaning, AssetRepair, AssetMissing},
		to:             AssetRetired,
		reasonRequired: true,
	},
}

var (
	ErrInvalidAssetTransition = domainerr.New(domainerr.KindInvalidState, "inventory.invalid_asset_transition", "asset cannot make this transition from its current status")
	ErrAssetOnRental          = domainerr.New(domainerr.KindInvalidState, "inventory.asset_on_rental", "asset is with a customer; record what happened to it on the booking")
	ErrReasonRequired         = domainerr.New(domainerr.KindInvalid, "inventory.reason_required", "a reason is required for this change")
	ErrChecklistIncomplete    = domainerr.New(domainerr.KindInvalid, "inventory.checklist_incomplete", "inspection checklist must be completed")
	ErrActorRequired          = domainerr.New(domainerr.KindInvalid, "inventory.actor_required", "a valid actor is required")
)

// Asset is one physical unit, e.g. jacket CST-00042, of exactly one variant.
type Asset struct {
	ID              AssetID
	Code            string
	VariantID       catalog.VariantID
	Status          AssetStatus
	StatusChangedAt time.Time
}

// StatusChange records a physical transition for audit and the movement log.
type StatusChange struct {
	AssetID AssetID
	Action  AssetAction
	From    AssetStatus
	To      AssetStatus
	Actor   identity.Actor
	At      time.Time
	Reason  string
}

// NewAsset registers a unit. New stock starts NEEDS_INSPECTION and must pass
// inspection before it is READY (owner decision D18).
func NewAsset(id AssetID, code string, variant catalog.VariantID, actor identity.Actor, now time.Time) (Asset, StatusChange, error) {
	switch {
	case id == "":
		return Asset{}, StatusChange{}, domainerr.Invalid("id", "required")
	case strings.TrimSpace(code) == "":
		return Asset{}, StatusChange{}, domainerr.Invalid("code", "required")
	case variant == "":
		return Asset{}, StatusChange{}, domainerr.Invalid("variant_id", "required")
	case !actor.Valid():
		return Asset{}, StatusChange{}, ErrActorRequired
	}
	a := Asset{ID: id, Code: code, VariantID: variant, Status: AssetNeedsInspection, StatusChangedAt: now.UTC()}
	return a, StatusChange{AssetID: id, Action: ActionIntake, To: AssetNeedsInspection, Actor: actor, At: now.UTC()}, nil
}

// HandOver marks the asset as with the customer. Callers must also mark the
// allocation handed over in the same transaction.
func (a *Asset) HandOver(actor identity.Actor, at time.Time) (StatusChange, error) {
	return a.apply(ActionHandOver, actor, at, "")
}

// ReceiveReturn records the asset physically back in the store. It is not
// READY until inspected and, when needed, cleaned.
func (a *Asset) ReceiveReturn(actor identity.Actor, at time.Time) (StatusChange, error) {
	return a.apply(ActionReceiveReturn, actor, at, "")
}

// Allows reports whether the action can happen from the asset's status.
func (a Asset) Allows(action AssetAction) bool {
	rule, ok := assetRules[action]
	return ok && slices.Contains(rule.from, a.Status)
}

// InspectionChecklist captures the inspection result. The criteria behind
// each flag are store policy (pending owner decision).
type InspectionChecklist struct {
	Completed bool
	Notes     string
}

// PassInspection marks the asset READY without cleaning.
func (a *Asset) PassInspection(actor identity.Actor, at time.Time, c InspectionChecklist) (StatusChange, error) {
	if !c.Completed {
		return StatusChange{}, ErrChecklistIncomplete
	}
	return a.apply(ActionPassInspection, actor, at, c.Notes)
}

// RequireCleaning sends an inspected asset to cleaning.
func (a *Asset) RequireCleaning(actor identity.Actor, at time.Time, c InspectionChecklist) (StatusChange, error) {
	if !c.Completed {
		return StatusChange{}, ErrChecklistIncomplete
	}
	return a.apply(ActionRequireCleaning, actor, at, c.Notes)
}

// CompleteCleaning is the actor's confirmation that cleaning is done.
func (a *Asset) CompleteCleaning(actor identity.Actor, at time.Time) (StatusChange, error) {
	return a.apply(ActionCompleteCleaning, actor, at, "")
}

func (a *Asset) SendToRepair(actor identity.Actor, at time.Time, reason string) (StatusChange, error) {
	return a.apply(ActionSendToRepair, actor, at, reason)
}

// CompleteRepair returns the asset to inspection, not directly to READY.
func (a *Asset) CompleteRepair(actor identity.Actor, at time.Time) (StatusChange, error) {
	return a.apply(ActionCompleteRepair, actor, at, "")
}

func (a *Asset) ReportMissing(actor identity.Actor, at time.Time, reason string) (StatusChange, error) {
	return a.apply(ActionReportMissing, actor, at, reason)
}

func (a *Asset) RecordFound(actor identity.Actor, at time.Time, reason string) (StatusChange, error) {
	return a.apply(ActionRecordFound, actor, at, reason)
}

func (a *Asset) Retire(actor identity.Actor, at time.Time, reason string) (StatusChange, error) {
	return a.apply(ActionRetire, actor, at, reason)
}

func (a *Asset) apply(action AssetAction, actor identity.Actor, at time.Time, reason string) (StatusChange, error) {
	if !actor.Valid() {
		return StatusChange{}, ErrActorRequired
	}
	rule := assetRules[action]
	if !slices.Contains(rule.from, a.Status) {
		return StatusChange{}, fmt.Errorf("asset %s %s from %s: %w", a.ID, action, a.Status, ErrInvalidAssetTransition)
	}
	if rule.reasonRequired && strings.TrimSpace(reason) == "" {
		return StatusChange{}, fmt.Errorf("asset %s %s: %w", a.ID, action, ErrReasonRequired)
	}
	change := StatusChange{AssetID: a.ID, Action: action, From: a.Status, To: rule.to, Actor: actor, At: at.UTC(), Reason: reason}
	a.Status, a.StatusChangedAt = rule.to, at.UTC()
	return change, nil
}
