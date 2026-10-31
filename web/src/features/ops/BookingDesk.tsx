import { useQuery } from '@tanstack/react-query';
import { useMemo, useRef, useState, type FormEvent } from 'react';
import { useParams } from 'react-router-dom';
import { Modal, QueryState, ReasonDialog, useAction } from '../../components/feedback';
import { Button, Card, Checkbox, DefinitionList, Field, FormError, Input, Notice, PageHeader, Select, Skeleton, StatusBadge, Textarea } from '../../components/ui';
import { bookingApi, storeApi, type PaymentInput } from '../../lib/api/endpoints';
import type { BookingAction, BookingDetail, BookingItem, Bucket, Condition, FeeAction, FeeAssessment, FeeKind, FinanceRefund, PaymentMethod } from '../../lib/api/types';
import { formatDateTime, formatMoney } from '../../lib/format';
import {
  assessmentStatus, assetStatus, bookingActionLabel, bookingItemState, bookingStatus, bucket, condition, feeKind, paymentMethod, paymentState,
} from '../../lib/labels';
import { BookingLines, BookingStepper, ChargeList, FinanceSummary, HoldCountdown, PaymentRows, RefundRow, TransitionList, scheduleFacts } from '../bookings/parts';

type Dialog =
  | { kind: 'handover' | 'return' | 'inspect' | 'fee' | 'payment' | 'cancel' | 'no_show' }
  | { kind: 'lost'; item: BookingItem }
  | { kind: 'fee_decision'; fee: FeeAssessment; action: FeeAction }
  | { kind: 'refund_reject' | 'refund_payout'; refund: FinanceRefund }
  | null;

const invalidate = [['bookings'], ['ops']];

const feeActionLabel: Record<FeeAction, string> = { approve: 'Duyệt', reject: 'Từ chối', waive: 'Miễn phí' };
const feeDecision: Record<FeeAction, 'APPROVE' | 'REJECT' | 'WAIVE'> = { approve: 'APPROVE', reject: 'REJECT', waive: 'WAIVE' };

const primaryActions: BookingAction[] = ['confirm', 'start_preparation', 'handover', 'record_return', 'inspect', 'record_payment', 'settle', 'complete'];
const secondaryActions: BookingAction[] = ['propose_fee', 'no_show', 'cancel'];

export default function BookingDesk() {
  const { id = '' } = useParams();
  const booking = useQuery({ queryKey: ['bookings', 'detail', id], queryFn: () => bookingApi.get(id) });
  return (
    <QueryState query={booking} skeleton={<Skeleton height={480} />}>
      {(b) => <Desk booking={b} />}
    </QueryState>
  );
}

function Desk({ booking }: { booking: BookingDetail }) {
  const [dialog, setDialog] = useState<Dialog>(null);
  const close = () => setDialog(null);
  const id = booking.id;
  const has = (a: BookingAction) => booking.actions.includes(a);

  const confirm = useAction(() => bookingApi.confirm(id), { success: 'Đã xác nhận đơn.', invalidate });
  const prepare = useAction(() => bookingApi.startPreparation(id), { success: 'Đã chuyển sang chuẩn bị.', invalidate });
  const settle = useAction(() => bookingApi.settle(id), { success: 'Đã quyết toán đơn.', invalidate });
  const complete = useAction(() => bookingApi.complete(id), { success: 'Đã hoàn tất đơn.', invalidate });
  const missing = useAction((assetId: string) => bookingApi.reportMissing(id, assetId), { success: 'Đã ghi nhận món thiếu.', invalidate });
  const approveRefund = useAction((refundId: string) => bookingApi.approveRefund(id, refundId), { success: 'Đã duyệt hoàn tiền.', invalidate });

  const run: Record<BookingAction, () => void> = {
    confirm: () => confirm.mutate(undefined),
    start_preparation: () => prepare.mutate(undefined),
    settle: () => settle.mutate(undefined),
    complete: () => complete.mutate(undefined),
    handover: () => setDialog({ kind: 'handover' }),
    record_return: () => setDialog({ kind: 'return' }),
    inspect: () => setDialog({ kind: 'inspect' }),
    propose_fee: () => setDialog({ kind: 'fee' }),
    record_payment: () => setDialog({ kind: 'payment' }),
    cancel: () => setDialog({ kind: 'cancel' }),
    no_show: () => setDialog({ kind: 'no_show' }),
    report_missing: () => undefined,
    confirm_lost: () => undefined,
    reschedule: () => undefined,
  };
  const busy = confirm.isPending || prepare.isPending || settle.isPending || complete.isPending;
  const main = primaryActions.filter(has);
  const other = secondaryActions.filter(has);

  return (
    <>
      <PageHeader
        back={{ to: '/ops/bookings', label: 'Đơn thuê' }}
        title={booking.code}
        subtitle={`${booking.customer.name} · ${booking.customer.phone}`}
        actions={(
          <div className="row">
            <StatusBadge status={bookingStatus[booking.status]} />
            {booking.is_overdue && <StatusBadge status={{ label: 'Quá hạn', tone: 'danger' }} />}
            <StatusBadge status={paymentState[booking.payment_state]} />
          </div>
        )}
      />
      <div className="stack">
        <Card><BookingStepper status={booking.status} /></Card>
        {booking.status === 'PENDING_CONFIRMATION' && booking.hold_expires_at && <HoldCountdown expiresAt={booking.hold_expires_at} />}

        {(main.length > 0 || other.length > 0) && (
          <Card title="Thao tác">
            <div className="action-bar">
              {main.map((a, i) => <Button key={a} variant={i === 0 ? 'primary' : 'dark'} disabled={busy} onClick={run[a]}>{bookingActionLabel[a]}</Button>)}
              {other.map((a) => <Button key={a} variant="outline" disabled={busy} onClick={run[a]}>{bookingActionLabel[a]}</Button>)}
            </div>
          </Card>
        )}

        <div className="split">
          <div className="stack">
            <Card title={`Tài sản của đơn (${booking.items.length})`}>
              {booking.items.length === 0 && <p className="small muted">Đơn chưa giữ tài sản nào.</p>}
              {booking.items.map((item) => (
                <div key={item.asset_id} className="item-row">
                  <div>
                    <strong>{item.name}</strong> <span className="small muted">{item.variant_label}</span>
                    <span className="cell-sub"><span className="mono">{item.asset_code}</span>{item.returned_at ? ` · Trả ${formatDateTime(item.returned_at)}` : ''}</span>
                    {item.preexisting_issues && <span className="cell-sub">Lỗi lúc bàn giao: {item.preexisting_issues}</span>}
                    {item.conditions.length > 0 && <span className="cell-sub">Kiểm tra: {item.conditions.map((c) => condition[c].label).join(', ')}</span>}
                  </div>
                  <div className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
                    <StatusBadge status={bookingItemState[item.state]} />
                    <StatusBadge status={assetStatus[item.asset_status]} />
                    {has('report_missing') && item.state === 'WITH_CUSTOMER' && (
                      <Button variant="ghost" size="sm" disabled={missing.isPending} onClick={() => missing.mutate(item.asset_id)}>Báo thiếu</Button>
                    )}
                    {has('confirm_lost') && item.state === 'MISSING_PENDING' && (
                      <Button variant="ghost" size="sm" onClick={() => setDialog({ kind: 'lost', item })}>Xác nhận mất</Button>
                    )}
                  </div>
                </div>
              ))}
            </Card>
            <Card title="Trang phục đã đặt"><BookingLines booking={booking} /></Card>
            {booking.fees.length > 0 && (
              <Card title="Phí phát sinh">
                {booking.fees.map((f) => (
                  <div key={f.id} className="item-row">
                    <div><strong>{feeKind[f.kind]}</strong><span className="cell-sub"><span className="mono">{f.asset_code}</span> · {f.basis}</span></div>
                    <div className="row" style={{ justifyContent: 'flex-end' }}>
                      <span className="tnum strong">{formatMoney(f.amount)}</span>
                      <StatusBadge status={assessmentStatus[f.status]} />
                      {f.actions.map((a) => (
                        <Button key={a} variant="outline" size="sm" onClick={() => setDialog({ kind: 'fee_decision', fee: f, action: a })}>{feeActionLabel[a]}</Button>
                      ))}
                    </div>
                  </div>
                ))}
              </Card>
            )}
            <Card title="Lịch sử đơn"><TransitionList transitions={booking.transitions} /></Card>
          </div>
          <div className="stack">
            <Card title="Lịch thuê"><DefinitionList items={[...scheduleFacts(booking), { label: 'Chính sách áp dụng', value: <span className="mono">{booking.policy_version}</span> }]} /></Card>
            <Card title="Tài chính"><FinanceSummary finance={booking.finance} /></Card>
            <Card title="Khoản thu"><ChargeList finance={booking.finance} /></Card>
            <Card title="Giao dịch">
              <div className="stack-sm">
                <PaymentRows finance={booking.finance} />
                {booking.finance.refunds.map((r) => (
                  <div key={r.id} className="stack-sm">
                    <RefundRow refund={r} />
                    {r.actions.length > 0 && (
                      <div className="action-bar">
                        {r.actions.includes('approve') && <Button size="sm" variant="outline" disabled={approveRefund.isPending} onClick={() => approveRefund.mutate(r.id)}>Duyệt hoàn</Button>}
                        {r.actions.includes('reject') && <Button size="sm" variant="ghost" onClick={() => setDialog({ kind: 'refund_reject', refund: r })}>Từ chối</Button>}
                        {r.actions.includes('payout') && <Button size="sm" onClick={() => setDialog({ kind: 'refund_payout', refund: r })}>Ghi nhận đã chi</Button>}
                      </div>
                    )}
                  </div>
                ))}
                {booking.finance.payments.length === 0 && booking.finance.refunds.length === 0 && <p className="small muted">Chưa có giao dịch nào.</p>}
              </div>
            </Card>
          </div>
        </div>
      </div>

      {dialog?.kind === 'handover' && <HandoverDialog booking={booking} onClose={close} />}
      {dialog?.kind === 'return' && <ReturnDialog booking={booking} onClose={close} />}
      {dialog?.kind === 'inspect' && <InspectDialog booking={booking} onClose={close} />}
      {dialog?.kind === 'fee' && <FeeDialog booking={booking} onClose={close} />}
      {dialog?.kind === 'payment' && <PaymentDialog booking={booking} onClose={close} />}
      {(dialog?.kind === 'cancel' || dialog?.kind === 'no_show') && <CancelDialog booking={booking} noShow={dialog.kind === 'no_show'} onClose={close} />}
      {dialog?.kind === 'lost' && <LostDialog booking={booking} item={dialog.item} onClose={close} />}
      {dialog?.kind === 'fee_decision' && <FeeDecisionDialog booking={booking} fee={dialog.fee} action={dialog.action} onClose={close} />}
      {dialog?.kind === 'refund_reject' && <RefundRejectDialog booking={booking} refund={dialog.refund} onClose={close} />}
      {dialog?.kind === 'refund_payout' && <RefundPayoutDialog booking={booking} refund={dialog.refund} onClose={close} />}
    </>
  );
}

function HandoverDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const [checked, setChecked] = useState(false);
  const [issues, setIssues] = useState<Record<string, string>>({});
  const handover = useAction(
    () => bookingApi.handover(booking.id, {
      checklist_completed: checked,
      delivered: booking.items.map((i) => ({ asset_id: i.asset_id, preexisting_issues: issues[i.asset_id]?.trim() ?? '' })),
    }),
    { inline: true, success: 'Đã ghi nhận bàn giao.', invalidate, onSuccess: onClose },
  );
  return (
    <Modal open wide title="Bàn giao cho khách" onClose={onClose}>
      <div className="stack">
        <p className="small muted">Ghi lại lỗi có sẵn của từng món. Lỗi đã ghi lúc bàn giao không bị tính cho khách nếu không có bằng chứng hư hỏng mới.</p>
        {booking.items.map((i) => (
          <Field key={i.asset_id} label={`${i.name} · ${i.variant_label} (${i.asset_code})`}>
            <Input placeholder="Lỗi có sẵn, nếu có" maxLength={300} value={issues[i.asset_id] ?? ''}
              onChange={(e) => setIssues({ ...issues, [i.asset_id]: e.target.value })} />
          </Field>
        ))}
        <Checkbox label="Đã kiểm đếm đủ món, đúng size và tình trạng cùng khách" checked={checked} onChange={(e) => setChecked(e.target.checked)} />
        <FormError error={handover.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={!checked} loading={handover.isPending} onClick={() => handover.mutate(undefined)}>Xác nhận bàn giao</Button>
        </div>
      </div>
    </Modal>
  );
}

function ReturnDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const out = booking.items.filter((i) => i.state === 'WITH_CUSTOMER' || i.state === 'MISSING_PENDING');
  const [picked, setPicked] = useState<Set<string>>(new Set(out.map((i) => i.asset_id)));
  const toggle = (id: string, on: boolean) => {
    const next = new Set(picked);
    if (on) next.add(id); else next.delete(id);
    setPicked(next);
  };
  const receive = useAction(() => bookingApi.recordReturn(booking.id, [...picked]), {
    inline: true, success: 'Đã ghi nhận trả hàng. Tài sản chuyển sang chờ kiểm tra.', invalidate, onSuccess: onClose,
  });
  return (
    <Modal open title="Nhận trả hàng" onClose={onClose}>
      <div className="stack">
        <p className="small muted">Bỏ chọn món khách chưa trả; có thể nhận trả từng phần.</p>
        {out.map((i) => (
          <Checkbox key={i.asset_id} label={<>{i.name} · {i.variant_label} <span className="mono small">{i.asset_code}</span></>}
            checked={picked.has(i.asset_id)} onChange={(e) => toggle(i.asset_id, e.target.checked)} />
        ))}
        <FormError error={receive.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={picked.size === 0} loading={receive.isPending} onClick={() => receive.mutate(undefined)}>Xác nhận nhận trả</Button>
        </div>
      </div>
    </Modal>
  );
}

function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

function InspectDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const returned = booking.items.filter((i) => i.state === 'RETURNED');
  const [assetId, setAssetId] = useState(returned.find((i) => i.conditions.length === 0)?.asset_id ?? returned[0]?.asset_id ?? '');
  const [conditions, setConditions] = useState<Condition[]>([]);
  const [notes, setNotes] = useState('');
  const [evidence, setEvidence] = useState('');
  const toggle = (c: Condition, on: boolean) => setConditions(on ? [...conditions, c] : conditions.filter((x) => x !== c));

  const inspect = useAction(
    () => bookingApi.inspect(booking.id, { asset_id: assetId, conditions, notes: notes.trim(), evidence: lines(evidence) }),
    { inline: true, success: 'Đã lưu kết quả kiểm tra.', invalidate, onSuccess: onClose },
  );

  return (
    <Modal open wide title="Kiểm tra hàng trả" onClose={onClose}>
      <div className="stack">
        <Field label="Món kiểm tra" required>
          <Select value={assetId} onChange={(e) => setAssetId(e.target.value)}>
            {returned.map((i) => <option key={i.asset_id} value={i.asset_id}>{i.name} · {i.variant_label} ({i.asset_code}){i.conditions.length ? ' · đã kiểm tra' : ''}</option>)}
          </Select>
        </Field>
        <fieldset className="stack-sm" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="field-label">Tình trạng</legend>
          <div className="form-grid">
            {(Object.keys(condition) as Condition[]).map((c) => (
              <Checkbox key={c} label={condition[c].label} checked={conditions.includes(c)} onChange={(e) => toggle(c, e.target.checked)} />
            ))}
          </div>
        </fieldset>
        <Field label="Ghi chú"><Textarea value={notes} maxLength={500} onChange={(e) => setNotes(e.target.value)} /></Field>
        <Field label="Bằng chứng" hint="Mã ảnh hoặc chứng từ, mỗi dòng một mục. Bắt buộc với phân loại có thể tính phí.">
          <Textarea value={evidence} onChange={(e) => setEvidence(e.target.value)} />
        </Field>
        <FormError error={inspect.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={!assetId || conditions.length === 0} loading={inspect.isPending} onClick={() => inspect.mutate(undefined)}>Lưu kết quả</Button>
        </div>
      </div>
    </Modal>
  );
}

// Late fees are calculated by the backend from the policy, so they are not offered here.
const proposableFees: FeeKind[] = ['CLEANING', 'DAMAGE_REPAIR', 'DAMAGE_REPLACEMENT', 'LOST_ITEM'];

function FeeDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const [assetId, setAssetId] = useState(booking.items[0]?.asset_id ?? '');
  const [kind, setKind] = useState<FeeKind>('CLEANING');
  const [amount, setAmount] = useState('');
  const [basis, setBasis] = useState('');
  const [evidence, setEvidence] = useState('');
  const [newDamage, setNewDamage] = useState(false);
  const item = booking.items.find((i) => i.asset_id === assetId);

  const propose = useAction(
    () => bookingApi.proposeFee(booking.id, {
      asset_id: assetId, kind, amount: { amount: Number(amount), currency: 'VND' }, basis: basis.trim(), evidence: lines(evidence), confirmed_new_damage: newDamage,
    }),
    { inline: true, success: 'Đã đề xuất phí, chờ người có quyền duyệt.', invalidate, onSuccess: onClose },
  );

  const submit = (e: FormEvent) => {
    e.preventDefault();
    propose.mutate(undefined);
  };

  return (
    <Modal open title="Đề xuất phí phát sinh" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label="Món" required>
          <Select value={assetId} onChange={(e) => setAssetId(e.target.value)}>
            {booking.items.map((i) => <option key={i.asset_id} value={i.asset_id}>{i.name} · {i.variant_label} ({i.asset_code})</option>)}
          </Select>
        </Field>
        <Field label="Loại phí" required>
          <Select value={kind} onChange={(e) => setKind(e.target.value as FeeKind)}>
            {proposableFees.map((k) => <option key={k} value={k}>{feeKind[k]}</option>)}
          </Select>
        </Field>
        <Field label="Số tiền (VND)" required>
          <Input type="number" inputMode="numeric" min={1} step={1} value={amount} required onChange={(e) => setAmount(e.target.value)} />
        </Field>
        <Field label="Căn cứ" required hint="Bảng giá, báo giá hoặc chứng từ.">
          <Textarea value={basis} required maxLength={500} onChange={(e) => setBasis(e.target.value)} />
        </Field>
        <Field label="Bằng chứng" hint="Mỗi dòng một mục."><Textarea value={evidence} onChange={(e) => setEvidence(e.target.value)} /></Field>
        {item?.preexisting_issues && (
          <Checkbox label={`Xác nhận hư hỏng mới, khác lỗi lúc giao (${item.preexisting_issues})`} checked={newDamage} onChange={(e) => setNewDamage(e.target.checked)} />
        )}
        <FormError error={propose.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={propose.isPending}>Đề xuất</Button>
        </div>
      </form>
    </Modal>
  );
}

const counterBuckets: Bucket[] = ['RENTAL', 'SECURITY', 'RESERVATION'];

function PaymentDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const store = useQuery({ queryKey: ['store-info'], queryFn: storeApi.info, staleTime: 5 * 60_000 });
  const methods = useMemo(() => (store.data?.payment_methods ?? []).filter((m) => m !== 'GATEWAY'), [store.data]);
  const [method, setMethod] = useState<PaymentMethod>('CASH');
  const [amounts, setAmounts] = useState<Partial<Record<Bucket, string>>>({});
  // Stays the same while this dialog is open, so double submits count once.
  const idempotencyKey = useRef(crypto.randomUUID());

  const portions: PaymentInput['portions'] = counterBuckets
    .filter((b) => Number(amounts[b]) > 0)
    .map((b) => ({ bucket: b, amount: { amount: Number(amounts[b]), currency: 'VND' } }));

  const record = useAction(() => bookingApi.recordPayment(booking.id, { idempotency_key: idempotencyKey.current, method, portions }), {
    inline: true, success: 'Đã ghi nhận thu tiền.', invalidate, onSuccess: onClose,
  });

  return (
    <Modal open title="Ghi nhận thu tiền tại quầy" onClose={onClose}>
      <div className="stack">
        <DefinitionList items={[{ label: 'Còn phải thanh toán', value: formatMoney(booking.finance.outstanding) }]} />
        <Field label="Hình thức" required>
          <Select value={method} onChange={(e) => setMethod(e.target.value as PaymentMethod)}>
            {methods.map((m) => <option key={m} value={m}>{paymentMethod[m]}</option>)}
          </Select>
        </Field>
        {counterBuckets.map((b) => (
          <Field key={b} label={`${bucket[b]} (VND)`}>
            <Input type="number" inputMode="numeric" min={0} step={1} value={amounts[b] ?? ''} onChange={(e) => setAmounts({ ...amounts, [b]: e.target.value })} />
          </Field>
        ))}
        {method === 'BANK_TRANSFER' && <Notice tone="info">Chỉ ghi nhận sau khi đã thấy tiền về tài khoản.</Notice>}
        <FormError error={record.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={portions.length === 0} loading={record.isPending} onClick={() => record.mutate(undefined)}>Ghi nhận</Button>
        </div>
      </div>
    </Modal>
  );
}

function CancelDialog({ booking, noShow, onClose }: { booking: BookingDetail; noShow: boolean; onClose: () => void }) {
  const reason = noShow ? 'NO_SHOW' : 'STAFF_DECISION';
  const preview = useQuery({ queryKey: ['cancellation-preview', booking.id, reason], queryFn: () => bookingApi.cancellationPreview(booking.id, reason), staleTime: 0 });
  const cancel = useAction((note: string) => bookingApi.cancel(booking.id, reason, note), {
    inline: true, success: noShow ? 'Đã ghi nhận khách không đến.' : 'Đã hủy đơn.', invalidate, onSuccess: onClose,
  });
  return (
    <ReasonDialog open title={noShow ? 'Ghi nhận khách không đến' : 'Hủy đơn thuê'} confirmLabel="Xác nhận" danger onClose={onClose}
      pending={cancel.isPending} error={cancel.error} onConfirm={(note) => cancel.mutate(note)}
      description={(
        <QueryState query={preview} skeleton={<Skeleton height={60} />}>
          {(p) => <DefinitionList items={[{ label: 'Phí áp dụng', value: `${formatMoney(p.fee)} (${p.fee_percent}%)` }, { label: 'Số tiền được hoàn', value: formatMoney(p.refund) }]} />}
        </QueryState>
      )} />
  );
}

function LostDialog({ booking, item, onClose }: { booking: BookingDetail; item: BookingItem; onClose: () => void }) {
  const lost = useAction((reason: string) => bookingApi.confirmLost(booking.id, item.asset_id, reason), {
    inline: true, success: 'Đã xác nhận mất món.', invalidate, onSuccess: onClose,
  });
  return (
    <ReasonDialog open title={`Xác nhận mất ${item.asset_code}`} confirmLabel="Xác nhận mất" danger onClose={onClose}
      pending={lost.isPending} error={lost.error} onConfirm={(reason) => lost.mutate(reason)} />
  );
}

function FeeDecisionDialog({ booking, fee, action, onClose }: { booking: BookingDetail; fee: FeeAssessment; action: FeeAction; onClose: () => void }) {
  const decide = useAction((note: string) => bookingApi.decideFee(booking.id, fee.id, feeDecision[action], note), {
    inline: true, success: 'Đã cập nhật khoản phí.', invalidate, onSuccess: onClose,
  });
  return (
    <ReasonDialog open title={`${feeActionLabel[action]}: ${feeKind[fee.kind]} ${formatMoney(fee.amount)}`} confirmLabel={feeActionLabel[action]} onClose={onClose}
      danger={action !== 'approve'} pending={decide.isPending} error={decide.error} onConfirm={(note) => decide.mutate(note)} />
  );
}

function RefundRejectDialog({ booking, refund, onClose }: { booking: BookingDetail; refund: FinanceRefund; onClose: () => void }) {
  const reject = useAction((note: string) => bookingApi.rejectRefund(booking.id, refund.id, note), {
    inline: true, success: 'Đã từ chối khoản hoàn.', invalidate, onSuccess: onClose,
  });
  return (
    <ReasonDialog open title={`Từ chối hoàn ${formatMoney(refund.amount)}`} confirmLabel="Từ chối" danger onClose={onClose}
      pending={reject.isPending} error={reject.error} onConfirm={(note) => reject.mutate(note)} />
  );
}

function RefundPayoutDialog({ booking, refund, onClose }: { booking: BookingDetail; refund: FinanceRefund; onClose: () => void }) {
  const [method, setMethod] = useState<'CASH' | 'BANK_TRANSFER'>('CASH');
  const [reference, setReference] = useState('');
  const [paidAt, setPaidAt] = useState('');
  const payout = useAction(() => bookingApi.payoutRefund(booking.id, refund.id, { method, reference: reference.trim(), paid_at: new Date(paidAt).toISOString() }), {
    inline: true, success: 'Đã ghi nhận đã chi hoàn tiền.', invalidate, onSuccess: onClose,
  });
  const ready = paidAt !== '' && (method === 'CASH' || reference.trim() !== '');
  return (
    <Modal open title={`Ghi nhận đã chi ${formatMoney(refund.amount)}`} onClose={onClose}>
      <div className="stack">
        <Field label="Cách chi" required>
          <Select value={method} onChange={(e) => setMethod(e.target.value as 'CASH' | 'BANK_TRANSFER')}>
            <option value="CASH">{paymentMethod.CASH}</option>
            <option value="BANK_TRANSFER">{paymentMethod.BANK_TRANSFER}</option>
          </Select>
        </Field>
        <Field label={method === 'BANK_TRANSFER' ? 'Mã giao dịch ngân hàng' : 'Mã chứng từ'} required={method === 'BANK_TRANSFER'}
          hint={method === 'BANK_TRANSFER' ? 'Dùng để đối chiếu sao kê.' : undefined}>
          <Input value={reference} maxLength={120} onChange={(e) => setReference(e.target.value)} />
        </Field>
        <Field label="Thời điểm chi" required><Input type="datetime-local" value={paidAt} onChange={(e) => setPaidAt(e.target.value)} /></Field>
        <FormError error={payout.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={!ready} loading={payout.isPending} onClick={() => payout.mutate(undefined)}>Xác nhận đã chi</Button>
        </div>
      </div>
    </Modal>
  );
}
