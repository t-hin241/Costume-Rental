-- Decision D22 (A8 confirmed): the permission matrix is a versioned policy
-- document. This seeds the owner-approved reference matrix as the active
-- version; identity.ReferencePermissionMatrix holds the same content and a
-- test keeps the two in sync. Later versions are published by an admin
-- through identityapp.PublishMatrix, never by editing this file.

ALTER TABLE policy_documents DROP CONSTRAINT policy_documents_kind_check;
ALTER TABLE policy_documents ADD CONSTRAINT policy_documents_kind_check
    CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION', 'PERMISSION_MATRIX'));

INSERT INTO policy_documents (kind, version, document, created_at, created_by_kind, created_by_id, active)
VALUES ('PERMISSION_MATRIX', 'reference-2026-10-30', '
{
  "admin": [
    "audit.read",
    "booking.cancel_any",
    "booking.cancel_own",
    "booking.complete",
    "booking.confirm",
    "booking.create_own",
    "booking.modify_any",
    "booking.modify_own",
    "booking.read_all",
    "booking.read_own",
    "booking.reschedule_any",
    "booking.reschedule_own",
    "catalog.manage",
    "charge.create",
    "charge.reverse",
    "complaint.create_own",
    "deduction.approve",
    "deduction.create",
    "delivery.manage",
    "delivery.read_all",
    "delivery.read_own",
    "fee.approve",
    "fee.approve_high",
    "fee.assess",
    "fee.waive",
    "inventory.manage",
    "inventory.read",
    "payment.read",
    "payment.read_own",
    "payment.record",
    "policy.manage",
    "pricing.manual_adjust",
    "promotion.manage",
    "refund.approve",
    "refund.create",
    "rental.confirm_lost",
    "rental.handover",
    "rental.prepare",
    "rental.return_inspect",
    "rental.return_receive",
    "revenue.read",
    "tax.manage",
    "user.lock",
    "user.role_manage"
  ],
  "customer": [
    "booking.cancel_own",
    "booking.create_own",
    "booking.modify_own",
    "booking.read_own",
    "booking.reschedule_own",
    "complaint.create_own",
    "delivery.read_own",
    "payment.read_own"
  ],
  "finance": [
    "audit.read",
    "booking.read_all",
    "charge.reverse",
    "deduction.approve",
    "fee.approve",
    "fee.waive",
    "payment.read",
    "payment.record",
    "refund.approve",
    "refund.create",
    "revenue.read"
  ],
  "inventory_manager": [
    "catalog.manage",
    "fee.assess",
    "inventory.manage",
    "inventory.read",
    "rental.return_inspect"
  ],
  "rental_staff": [
    "booking.cancel_any",
    "booking.complete",
    "booking.confirm",
    "booking.modify_any",
    "booking.read_all",
    "booking.reschedule_any",
    "charge.create",
    "deduction.create",
    "delivery.manage",
    "delivery.read_all",
    "fee.assess",
    "inventory.read",
    "payment.record",
    "refund.create",
    "rental.handover",
    "rental.prepare",
    "rental.return_inspect",
    "rental.return_receive"
  ]
}'::jsonb, '2026-10-30T00:00:00Z', 'system', 'migration-000009', true);
