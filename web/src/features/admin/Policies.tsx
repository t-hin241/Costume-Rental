import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent, type ReactNode } from 'react';
import { Icon } from '../../components/Icon';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Card, Field, FormError, Input, Notice, PageHeader, Select, Skeleton, Textarea } from '../../components/ui';
import { adminApi } from '../../lib/api/endpoints';
import type { FundsRequirement, PolicyDocument, ReservationTreatment } from '../../lib/api/types';
import { formatDateTime } from '../../lib/format';
import { fundsRequirement } from '../../lib/labels';

export default function Policies() {
  const policy = useQuery({ queryKey: ['admin', 'policy'], queryFn: adminApi.policy });
  return (
    <>
      <PageHeader title="Chính sách kinh doanh" subtitle="Mỗi lần xuất bản tạo một phiên bản mới. Đơn đã đặt giữ nguyên phiên bản lúc đặt." />
      <QueryState query={policy} skeleton={<Skeleton height={480} />}>
        {(p) => <Editor key={p.version} active={p} />}
      </QueryState>
    </>
  );
}

function Num({ label, value, onChange, hint, min = 0 }: { label: string; value: number; onChange: (v: number) => void; hint?: string; min?: number }) {
  return (
    <Field label={label} hint={hint} required>
      <Input type="number" inputMode="numeric" min={min} step={1} value={value} required onChange={(e) => onChange(Number(e.target.value))} />
    </Field>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <Card title={title}><div className="form-grid">{children}</div></Card>;
}

function TierRows<T>({ title, rows, render, onAdd, onRemove }: {
  title: string; rows: T[]; render: (row: T, i: number) => ReactNode; onAdd: () => void; onRemove: (i: number) => void;
}) {
  return (
    <fieldset className="full stack-sm" style={{ border: 0, padding: 0, margin: 0 }}>
      <legend className="field-label">{title}</legend>
      {rows.map((r, i) => (
        <div key={i} className="row" style={{ flexWrap: 'nowrap', alignItems: 'flex-end' }}>
          {render(r, i)}
          <button type="button" className="icon-button" aria-label="Xóa mức" onClick={() => onRemove(i)}><Icon name="trash" size={18} /></button>
        </div>
      ))}
      <div><Button variant="ghost" size="sm" icon="plus" onClick={onAdd}>Thêm mức</Button></div>
    </fieldset>
  );
}

function Editor({ active }: { active: PolicyDocument }) {
  const [draft, setDraft] = useState(active);
  const [publishing, setPublishing] = useState(false);
  const patch = (fn: (d: PolicyDocument) => void) => {
    const next = structuredClone(draft);
    fn(next);
    setDraft(next);
  };
  const dirty = JSON.stringify(draft) !== JSON.stringify(active);

  return (
    <div className="stack">
      <Notice tone="brand" title={`Phiên bản ${active.version}`}>
        Xuất bản {formatDateTime(active.published_at)} bởi {active.published_by}. Giá thuê theo gói nằm ở mục Sản phẩm, giảm giá ở mục Khuyến mãi.
      </Notice>
      <Section title="Quy định đặt thuê">
        <Num label="Thời gian giữ hàng (phút)" min={1} value={draft.booking.hold_minutes} onChange={(v) => patch((d) => { d.booking.hold_minutes = v; })} />
        <Num label="Đặt trước tối thiểu (giờ)" value={draft.booking.min_lead_hours} onChange={(v) => patch((d) => { d.booking.min_lead_hours = v; })} />
        <Num label="Đặt trước tối đa (giờ)" min={1} value={draft.booking.max_advance_hours} onChange={(v) => patch((d) => { d.booking.max_advance_hours = v; })} />
        <Num label="Khách tự đổi lịch trước giờ nhận (giờ)" value={draft.booking.reschedule_cutoff_hours} onChange={(v) => patch((d) => { d.booking.reschedule_cutoff_hours = v; })} />
        <Num label="Số dòng tối đa mỗi đơn" min={1} value={draft.booking.max_lines_per_booking} onChange={(v) => patch((d) => { d.booking.max_lines_per_booking = v; })} />
        <Num label="Số lượng tối đa mỗi dòng" min={1} value={draft.booking.max_quantity_per_line} onChange={(v) => patch((d) => { d.booking.max_quantity_per_line = v; })} />
      </Section>
      <Section title="Xác nhận và bàn giao">
        <Field label="Cách xác nhận đơn" required>
          <Select value={draft.confirmation.mode} onChange={(e) => patch((d) => { d.confirmation.mode = e.target.value as 'STAFF' | 'AUTO'; })}>
            <option value="STAFF">Nhân viên xác nhận</option>
            <option value="AUTO">Tự xác nhận khi đủ tiền</option>
          </Select>
        </Field>
        <Field label="Điều kiện tiền để xác nhận" required>
          <Select value={draft.confirmation.funds} onChange={(e) => patch((d) => { d.confirmation.funds = e.target.value as FundsRequirement; })}>
            {Object.entries(fundsRequirement).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </Select>
        </Field>
        <Field label="Điều kiện tiền để bàn giao" required>
          <Select value={draft.confirmation.handover_funds} onChange={(e) => patch((d) => { d.confirmation.handover_funds = e.target.value as FundsRequirement; })}>
            {Object.entries(fundsRequirement).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </Select>
        </Field>
      </Section>
      <Section title="Tiền giữ lịch">
        <Field label="Cách tính" required>
          <Select value={draft.reservation.kind} onChange={(e) => patch((d) => { d.reservation.kind = e.target.value as PolicyDocument['reservation']['kind']; })}>
            <option value="NONE">Không thu</option>
            <option value="FIXED">Số tiền cố định</option>
            <option value="RENT_PERCENT">Phần trăm tiền thuê</option>
          </Select>
        </Field>
        {draft.reservation.kind === 'RENT_PERCENT' && (
          <Num label="Phần trăm tiền thuê" min={1} value={draft.reservation.rent_percent ?? 0} onChange={(v) => patch((d) => { d.reservation.rent_percent = v; })} />
        )}
        {draft.reservation.kind === 'FIXED' && (
          <Num label="Số tiền (VND)" min={1} value={draft.reservation.amount?.amount ?? 0} onChange={(v) => patch((d) => { d.reservation.amount = { amount: v, currency: 'VND' }; })} />
        )}
        <Field label="Khi thuê thành công" required>
          <Select value={draft.reservation.treatment} onChange={(e) => patch((d) => { d.reservation.treatment = e.target.value as ReservationTreatment; })}>
            <option value="APPLIED_TO_RENT">Trừ vào tiền thuê</option>
            <option value="REFUNDED_AFTER_RETURN">Hoàn lại sau khi trả</option>
          </Select>
        </Field>
      </Section>
      <Section title="Buffer chuẩn bị và xử lý">
        <Num label="Chuẩn bị trước giờ nhận (giờ)" value={draft.buffers.default_preparation_hours} onChange={(v) => patch((d) => { d.buffers.default_preparation_hours = v; })} />
        <Num label="Kiểm tra, vệ sinh sau hạn trả (giờ)" value={draft.buffers.default_turnaround_hours} onChange={(v) => patch((d) => { d.buffers.default_turnaround_hours = v; })} />
        <TierRows title="Ngoại lệ theo nhóm món" rows={draft.buffers.by_class}
          onAdd={() => patch((d) => { d.buffers.by_class.push({ class: '', preparation_hours: 0, turnaround_hours: 0 }); })}
          onRemove={(i) => patch((d) => { d.buffers.by_class.splice(i, 1); })}
          render={(r, i) => (
            <>
              <Field label="Nhóm"><Input value={r.class} onChange={(e) => patch((d) => { (d.buffers.by_class[i] as typeof r).class = e.target.value; })} /></Field>
              <Num label="Chuẩn bị (giờ)" value={r.preparation_hours} onChange={(v) => patch((d) => { (d.buffers.by_class[i] as typeof r).preparation_hours = v; })} />
              <Num label="Xử lý (giờ)" value={r.turnaround_hours} onChange={(v) => patch((d) => { (d.buffers.by_class[i] as typeof r).turnaround_hours = v; })} />
            </>
          )} />
      </Section>
      <Section title="Phí hủy">
        <TierRows title="Theo thời gian còn lại đến giờ nhận" rows={draft.cancellation.tiers}
          onAdd={() => patch((d) => { d.cancellation.tiers.push({ min_notice_hours: 0, fee_percent: 0 }); })}
          onRemove={(i) => patch((d) => { d.cancellation.tiers.splice(i, 1); })}
          render={(r, i) => (
            <>
              <Num label="Còn từ (giờ)" value={r.min_notice_hours} onChange={(v) => patch((d) => { (d.cancellation.tiers[i] as typeof r).min_notice_hours = v; })} />
              <Num label="Phí (% tiền thuê)" value={r.fee_percent} onChange={(v) => patch((d) => { (d.cancellation.tiers[i] as typeof r).fee_percent = v; })} />
            </>
          )} />
        <Num label="Quá giờ nhận thì coi là không đến (giờ)" value={draft.cancellation.no_show_after_hours} onChange={(v) => patch((d) => { d.cancellation.no_show_after_hours = v; })} />
        <Num label="Phí khi không đến (%)" value={draft.cancellation.no_show_fee_percent} onChange={(v) => patch((d) => { d.cancellation.no_show_fee_percent = v; })} />
      </Section>
      <Section title="Phí phát sinh">
        <Num label="Ân hạn trả trễ (phút)" value={draft.late_fee.grace_minutes} onChange={(v) => patch((d) => { d.late_fee.grace_minutes = v; })} />
        <Num label="Duyệt cấp cao khi phí vượt (VND)" value={draft.fee_approval_above.amount} onChange={(v) => patch((d) => { d.fee_approval_above = { amount: v, currency: 'VND' }; })} />
        <TierRows title="Phí trả trễ theo giá thuê/ngày của món" rows={draft.late_fee.tiers}
          onAdd={() => patch((d) => { d.late_fee.tiers.push({ up_to_hours: 0, percent: 0 }); })}
          onRemove={(i) => patch((d) => { d.late_fee.tiers.splice(i, 1); })}
          render={(r, i) => (
            <>
              <Num label="Trễ đến (giờ)" value={r.up_to_hours} onChange={(v) => patch((d) => { (d.late_fee.tiers[i] as typeof r).up_to_hours = v; })} />
              <Num label="Phí (%)" value={r.percent} onChange={(v) => patch((d) => { (d.late_fee.tiers[i] as typeof r).percent = v; })} />
            </>
          )} />
        <Num label="Sau mức cuối, mỗi (giờ)" min={1} value={draft.late_fee.extra_period_hours} onChange={(v) => patch((d) => { d.late_fee.extra_period_hours = v; })} />
        <Num label="Cộng thêm (%)" value={draft.late_fee.extra_percent} onChange={(v) => patch((d) => { d.late_fee.extra_percent = v; })} />
        <Num label="Thời hạn tìm lại món thiếu (giờ)" value={draft.loss_verification_hours} onChange={(v) => patch((d) => { d.loss_verification_hours = v; })} />
      </Section>
      <Section title="Sửa đơn">
        <Num label="Khách tự sửa trước giờ nhận (giờ)" value={draft.modification.self_service_cutoff_hours} onChange={(v) => patch((d) => { d.modification.self_service_cutoff_hours = v; })} />
        <Num label="Số lần tự đổi size" value={draft.modification.self_service_size_changes} onChange={(v) => patch((d) => { d.modification.self_service_size_changes = v; })} />
        <Num label="Giữ hàng mới khi chờ thanh toán (phút)" min={1} value={draft.modification.hold_minutes} onChange={(v) => patch((d) => { d.modification.hold_minutes = v; })} />
      </Section>
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        <Button variant="ghost" disabled={!dirty} onClick={() => setDraft(active)}>Bỏ thay đổi</Button>
        <Button disabled={!dirty} onClick={() => setPublishing(true)}>Xuất bản phiên bản mới</Button>
      </div>
      {publishing && <PublishDialog draft={draft} onClose={() => setPublishing(false)} />}
    </div>
  );
}

function PublishDialog({ draft, onClose }: { draft: PolicyDocument; onClose: () => void }) {
  const [version, setVersion] = useState('');
  const [reason, setReason] = useState('');
  const publish = useAction(() => adminApi.publishPolicy({ policy: { ...draft, version: version.trim() }, reason: reason.trim() }), {
    inline: true, success: 'Đã xuất bản chính sách mới.', invalidate: [['admin', 'policy'], ['public-policy']], onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    publish.mutate(undefined);
  };
  return (
    <Modal open title="Xuất bản chính sách" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Notice tone="info">Chỉ áp dụng cho đơn đặt sau khi xuất bản. Phiên bản cũ được giữ nguyên.</Notice>
        <Field label="Mã phiên bản" required><Input value={version} required maxLength={64} onChange={(e) => setVersion(e.target.value)} /></Field>
        <Field label="Lý do" required><Textarea value={reason} required maxLength={500} onChange={(e) => setReason(e.target.value)} /></Field>
        <FormError error={publish.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={publish.isPending}>Xuất bản</Button>
        </div>
      </form>
    </Modal>
  );
}
