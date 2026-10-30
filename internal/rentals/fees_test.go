package rentals_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/rentals"
)

func principal(t *testing.T, id identity.UserID, roles ...identity.Role) identity.Principal {
	t.Helper()
	p, err := identity.NewPrincipal(id, roles, identity.ReferencePermissionMatrix())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Fee policy §8 boundaries on the jacket (daily rate 50,000đ).
func TestLateFeeTable(t *testing.T) {
	cases := []struct {
		late         time.Duration
		affectedNext bool
		percent      int
		fee          int64
	}{
		{0, false, 0, 0},
		{30 * time.Minute, false, 0, 0},
		{30 * time.Minute, true, 25, 12_500},
		{31 * time.Minute, false, 25, 12_500},
		{4 * time.Hour, false, 25, 12_500},
		{4*time.Hour + time.Minute, false, 50, 25_000},
		{8 * time.Hour, false, 50, 25_000},
		{8*time.Hour + time.Minute, false, 75, 37_500},
		{12 * time.Hour, false, 75, 37_500},
		{12*time.Hour + time.Minute, false, 100, 50_000},
		{24 * time.Hour, false, 100, 50_000},
		{25 * time.Hour, false, 200, 100_000},
		{48 * time.Hour, false, 200, 100_000},
		{48*time.Hour + time.Minute, false, 300, 150_000},
	}
	for _, c := range cases {
		b, assets := handedOver(t)
		if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:1], Now: b.Schedule.DueAt.Add(c.late)}); err != nil {
			t.Fatal(err)
		}
		q, err := b.QuoteLateFee(assets[0], c.affectedNext)
		if err != nil {
			t.Fatal(err)
		}
		if q.Percent != c.percent || q.Amount.Amount() != c.fee {
			t.Errorf("late %s affected=%v: %d%% %d, want %d%% %d", c.late, c.affectedNext, q.Percent, q.Amount.Amount(), c.percent, c.fee)
		}
	}
}

func TestLateFeeIsPerItem(t *testing.T) {
	b, assets := handedOver(t)
	due := b.Schedule.DueAt
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:1], Now: due})
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[1:2], Now: due.Add(5 * time.Hour)})
	jacket, _ := b.QuoteLateFee(assets[0], false)
	pants, _ := b.QuoteLateFee(assets[1], false)
	if !jacket.Amount.IsZero() || pants.Amount.Amount() != 15_000 {
		t.Fatalf("jacket=%d pants=%d; want 0 and 50%% of 30,000 (pants daily rate, not the bundle)", jacket.Amount.Amount(), pants.Amount.Amount())
	}
	if _, err := b.QuoteLateFee(assets[2], false); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Fatalf("quote for an item still out err = %v", err)
	}
}

func TestLateFeeStopsAtLossConfirmation(t *testing.T) {
	b, assets := handedOver(t)
	due := b.Schedule.DueAt
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:2], Now: due})
	_, _ = b.ReportMissing(staff, assets[2], due.Add(time.Hour))
	if _, err := b.ConfirmLost(staff, assets[2], "không tìm thấy", due.Add(49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	q, err := b.QuoteLateFee(assets[2], false)
	if err != nil {
		t.Fatal(err)
	}
	// 49h late at confirmation → 300% of the belt's 10,000đ, and no more.
	if q.Lateness != 49*time.Hour || q.Amount.Amount() != 30_000 {
		t.Fatalf("quote = %+v", q)
	}
}

func TestAssessLateFee(t *testing.T) {
	b, assets := handedOver(t)
	due := b.Schedule.DueAt
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:1], Now: due.Add(10 * time.Minute)})
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[1:2], Now: due.Add(2 * time.Hour)})
	if _, created, err := b.AssessLateFee(nil, "f0", assets[0], false, system, now); err != nil || created {
		t.Fatalf("within grace = %v, %v; want nothing recorded", created, err)
	}
	a, created, err := b.AssessLateFee(nil, "f1", assets[1], false, system, now)
	if err != nil || !created || a.Status != rentals.AssessmentApproved || a.Amount.Amount() != 7_500 || a.PolicyVersion != "test-v1" {
		t.Fatalf("assessment = %+v, %v, %v", a, created, err)
	}
	if _, _, err := b.AssessLateFee([]rentals.FeeAssessment{a}, "f2", assets[1], false, system, now); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Fatalf("second late fee err = %v", err)
	}
}

func returnedAndInspected(t *testing.T, findings map[int]rentals.InspectionFinding, preexisting string) (*rentals.Booking, []inventory.AssetID) {
	t.Helper()
	p := mustPlace(t)
	b := p.Booking
	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	assets := allocatedAssets(p)
	d := delivered(assets...)
	d[0].PreexistingIssues = preexisting
	if _, err := b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: d,
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets, Now: b.Schedule.DueAt}); err != nil {
		t.Fatal(err)
	}
	for i, a := range assets {
		f := findings[i]
		if _, err := b.RecordInspection(staff, a, f, b.Schedule.DueAt); err != nil {
			t.Fatal(err)
		}
	}
	return b, assets
}

var photo = []string{"evidence/return-1.jpg"}

func TestProposeFeeFollowsInspection(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, assets := returnedAndInspected(t, map[int]rentals.InspectionFinding{
		0: {Conditions: []rentals.Condition{rentals.CondRoutineSoiling, rentals.CondNormalWear}},
		1: {Conditions: []rentals.Condition{rentals.CondAbnormalSoiling, rentals.CondMinorRepairable}, Evidence: photo},
	}, "")
	propose := func(existing []rentals.FeeAssessment, id string, asset inventory.AssetID, kind rentals.FeeKind, amount int64) (rentals.FeeAssessment, error) {
		return b.ProposeFee(existing, rentals.ProposeFeeRequest{ID: rentals.AssessmentID(id), Asset: asset, Kind: kind, Amount: vnd(amount), Basis: "bảng giá công khai"}, clerk, now)
	}
	// Normal wear and routine soiling are included in the rent.
	if _, err := propose(nil, "f1", assets[0], rentals.FeeCleaning, 50_000); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Errorf("cleaning fee for routine soiling err = %v", err)
	}
	if _, err := propose(nil, "f1", assets[0], rentals.FeeDamageRepair, 50_000); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Errorf("repair fee for normal wear err = %v", err)
	}
	cleaning, err := propose(nil, "f2", assets[1], rentals.FeeCleaning, 40_000)
	if err != nil || cleaning.Status != rentals.AssessmentProposed {
		t.Fatalf("cleaning = %+v, %v", cleaning, err)
	}
	repair, err := propose([]rentals.FeeAssessment{cleaning}, "f3", assets[1], rentals.FeeDamageRepair, 80_000)
	if err != nil {
		t.Fatalf("stain and repair can both be charged: %v", err)
	}
	existing := []rentals.FeeAssessment{cleaning, repair}
	if _, err := propose(nil, "f4", assets[1], rentals.FeeDamageReplacement, 300_000); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Errorf("replacement without an irreparable finding err = %v", err)
	}
	if _, err := propose(existing, "f4", assets[1], rentals.FeeDamageRepair, 10_000); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Errorf("second repair fee err = %v", err)
	}
	if _, err := propose(nil, "f5", assets[2], rentals.FeeLostItem, 100_000); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Errorf("loss fee for a returned item err = %v", err)
	}
	customer := principal(t, customer, identity.RoleCustomer)
	if _, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f6", Asset: assets[1], Kind: rentals.FeeCleaning, Amount: vnd(1), Basis: "x"}, customer, now); !errors.Is(err, identity.ErrForbidden) {
		t.Errorf("customer proposing a fee err = %v", err)
	}
}

func TestRepairAndReplacementAreExclusive(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, assets := returnedAndInspected(t, map[int]rentals.InspectionFinding{
		0: {Conditions: []rentals.Condition{rentals.CondIrreparable, rentals.CondAbnormalSoiling}, Evidence: photo},
	}, "")
	replace, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Asset: assets[0], Kind: rentals.FeeDamageReplacement, Amount: vnd(400_000), Basis: "giá thay thế tương đương"}, clerk, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []rentals.FeeKind{rentals.FeeCleaning, rentals.FeeDamageRepair} {
		if _, err := b.ProposeFee([]rentals.FeeAssessment{replace}, rentals.ProposeFeeRequest{ID: "f2", Asset: assets[0], Kind: kind, Amount: vnd(1), Basis: "x", Evidence: photo}, clerk, now); !errors.Is(err, rentals.ErrDuplicateFee) {
			t.Errorf("%s on a replaced item err = %v", kind, err)
		}
	}
}

func TestPreexistingDamageNeedsNewEvidence(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, assets := returnedAndInspected(t, map[int]rentals.InspectionFinding{
		0: {Conditions: []rentals.Condition{rentals.CondMinorRepairable}, Evidence: photo},
	}, "sút 1 nút tay trái")
	req := rentals.ProposeFeeRequest{ID: "f1", Asset: assets[0], Kind: rentals.FeeDamageRepair, Amount: vnd(30_000), Basis: "đơn giá đính nút"}
	if _, err := b.ProposeFee(nil, req, clerk, now); !errors.Is(err, rentals.ErrNewDamageUnproven) {
		t.Fatalf("damage on an item with handover notes err = %v", err)
	}
	req.ConfirmedNewDamage, req.Evidence = true, []string{"evidence/handover-vs-return.jpg"}
	if _, err := b.ProposeFee(nil, req, clerk, now); err != nil {
		t.Fatalf("new damage with evidence: %v", err)
	}
}

func TestLostItemFee(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, assets := handedOver(t)
	due := b.Schedule.DueAt
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:2], Now: due})
	_, _ = b.ReportMissing(staff, assets[2], due)
	_, _ = b.ConfirmLost(staff, assets[2], "không tìm thấy", due.Add(48*time.Hour))
	late, _, err := b.AssessLateFee(nil, "f1", assets[2], false, system, now)
	if err != nil {
		t.Fatal(err)
	}
	loss, err := b.ProposeFee([]rentals.FeeAssessment{late}, rentals.ProposeFeeRequest{ID: "f2", Asset: assets[2], Kind: rentals.FeeLostItem, Amount: vnd(90_000), Basis: "giá mua thắt lưng tương đương"}, clerk, now)
	if err != nil {
		t.Fatalf("late fee before loss and the loss itself are separate: %v", err)
	}
	if _, err := b.ProposeFee([]rentals.FeeAssessment{late, loss}, rentals.ProposeFeeRequest{ID: "f3", Asset: assets[2], Kind: rentals.FeeLostItem, Amount: vnd(1), Basis: "x"}, clerk, now); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Fatalf("second loss fee err = %v", err)
	}
}

func TestFeeApprovalAuthority(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	finance := principal(t, "fin1", identity.RoleFinance)
	owner := principal(t, "owner1", identity.RoleAdmin)
	owner2 := principal(t, "owner2", identity.RoleAdmin)
	b, assets := returnedAndInspected(t, map[int]rentals.InspectionFinding{
		0: {Conditions: []rentals.Condition{rentals.CondMajorRepairable}, Evidence: photo},
		1: {Conditions: []rentals.Condition{rentals.CondIrreparable}, Evidence: photo},
	}, "")
	small, _ := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Asset: assets[0], Kind: rentals.FeeDamageRepair, Amount: vnd(80_000), Basis: "báo giá thợ"}, clerk, now)
	if err := small.Approve(b, clerk, now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff approving their own fee err = %v", err)
	}
	if err := small.Approve(b, finance, now); err != nil || small.Status != rentals.AssessmentApproved {
		t.Fatalf("finance approval = %v", err)
	}

	big, _ := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f2", Asset: assets[1], Kind: rentals.FeeDamageReplacement, Amount: vnd(600_000), Basis: "định giá"}, owner, now)
	if err := big.Approve(b, finance, now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("finance approving above the threshold err = %v", err)
	}
	if err := big.Approve(b, owner, now); !errors.Is(err, rentals.ErrApproverMustDiffer) {
		t.Fatalf("proposer approving a high fee err = %v", err)
	}
	if err := big.Approve(b, owner2, now); err != nil {
		t.Fatalf("second manager approval: %v", err)
	}
}

func TestDisputeWaiveAndAdjust(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	finance := principal(t, "fin1", identity.RoleFinance)
	b, assets := handedOver(t)
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:1], Now: b.Schedule.DueAt.Add(6 * time.Hour)})
	fee, _, _ := b.AssessLateFee(nil, "f1", assets[0], false, system, now) // 50% of 50,000

	if err := fee.Dispute(clerk, "khách nói đã gọi báo trước", now); err != nil {
		t.Fatal(err)
	}
	if rentals.CountOpenAssessments([]rentals.FeeAssessment{fee}) != 1 {
		t.Fatal("a disputed fee is open")
	}
	if err := fee.UpholdDispute(finance, "không có ghi nhận cuộc gọi", now); err != nil || fee.Status != rentals.AssessmentApproved {
		t.Fatalf("uphold = %v", err)
	}
	if _, err := fee.Adjust("f2", vnd(25_000), finance, "x", now); !errors.Is(err, rentals.ErrAdjustmentInvalid) {
		t.Fatalf("adjusting to the same amount err = %v", err)
	}
	if _, err := fee.Adjust("f2", vnd(10_000), clerk, "x", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff without fee.waive adjusting err = %v", err)
	}
	adjusted, err := fee.Adjust("f2", vnd(10_000), finance, "giảm vì kẹt xe đã báo", now)
	if err != nil || fee.Status != rentals.AssessmentSuperseded || adjusted.Supersedes != "f1" || adjusted.Status != rentals.AssessmentApproved {
		t.Fatalf("adjust = %+v / %+v, %v", fee, adjusted, err)
	}
	if err := adjusted.Waive(finance, "", now); !errors.Is(err, rentals.ErrReasonRequired) {
		t.Fatalf("waive without reason err = %v", err)
	}
	if err := adjusted.Waive(finance, "lỗi cửa hàng đóng cửa sớm", now); err != nil || adjusted.Status != rentals.AssessmentWaived {
		t.Fatalf("waive = %v", err)
	}
}

func TestLateFeeUsesSnapshotPolicy(t *testing.T) {
	p := testPolicy()
	placed := mustPlace(t, func(r *rentals.PlaceRequest) { r.Policy = p })
	p.LateFee.Tiers[0].Percent = 90 // a later, stricter table
	b := placed.Booking
	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	assets := allocatedAssets(placed)
	_, _ = b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(placed), Delivered: delivered(assets...),
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt})
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:1], Now: b.Schedule.DueAt.Add(time.Hour)})
	q, _ := b.QuoteLateFee(assets[0], false)
	if q.Percent != 25 {
		t.Fatalf("percent = %d, want the booked 25%%", q.Percent)
	}
}
