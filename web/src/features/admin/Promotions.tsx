import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Card, Checkbox, EmptyState, Field, FormError, Input, PageHeader, Select, Skeleton, StatusBadge } from '../../components/ui';
import { adminApi } from '../../lib/api/endpoints';
import type { Campaign, CampaignStatus, CampaignType } from '../../lib/api/types';
import { businessToday, formatDate, formatMoney } from '../../lib/format';
import { campaignStatus, campaignType } from '../../lib/labels';

function value(c: Campaign): string {
  if (c.value_type === 'PERCENT') return `${c.percent}%`;
  return c.amount ? formatMoney(c.amount) : '';
}

export default function Promotions() {
  const campaigns = useQuery({ queryKey: ['admin', 'campaigns'], queryFn: adminApi.campaigns });
  const [editing, setEditing] = useState<Campaign | 'new' | null>(null);

  const columns: Column<Campaign>[] = [
    { key: 'name', header: 'Chương trình', render: (c) => <span><button type="button" className="cell-title" style={{ background: 'none', border: 0, padding: 0 }} onClick={() => setEditing(c)}>{c.name}</button><span className="cell-sub mono">{c.code}</span></span> },
    { key: 'type', header: 'Loại', render: (c) => campaignType[c.type] },
    { key: 'value', header: 'Mức giảm', align: 'right', render: (c) => <span className="tnum">{value(c)}</span> },
    { key: 'period', header: 'Hiệu lực', render: (c) => <span className="nowrap">{formatDate(c.valid_from)} đến {formatDate(c.valid_until)}</span> },
    { key: 'used', header: 'Đã dùng', align: 'right', render: (c) => <span className="tnum">{c.used}{c.usage_limit ? ` / ${c.usage_limit}` : ''}</span> },
    { key: 'status', header: 'Trạng thái', render: (c) => <StatusBadge status={campaignStatus[c.status]} /> },
  ];

  return (
    <>
      <PageHeader title="Khuyến mãi" subtitle="Giảm giá chỉ áp dụng trên tiền thuê, không giảm tiền bảo đảm hay phí phát sinh." />
      <QueryState query={campaigns} skeleton={<Skeleton height={280} />}>
        {(rows) => (
          <Card title="Chương trình" flush actions={<Button size="sm" icon="plus" onClick={() => setEditing('new')}>Tạo chương trình</Button>}>
            {rows.length === 0
              ? <EmptyState icon="tag" title="Chưa có chương trình nào" />
              : <DataTable rows={rows} columns={columns} rowKey={(c) => c.id} caption="Chương trình khuyến mãi" />}
          </Card>
        )}
      </QueryState>
      {editing && <CampaignDialog campaign={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </>
  );
}

function toDate(iso: string | undefined): string {
  return iso ? businessToday(new Date(iso)) : '';
}

function CampaignDialog({ campaign, onClose }: { campaign: Campaign | null; onClose: () => void }) {
  const [name, setName] = useState(campaign?.name ?? '');
  const [code, setCode] = useState(campaign?.code ?? '');
  const [type, setType] = useState<CampaignType>(campaign?.type ?? 'COUPON');
  const [status, setStatus] = useState<CampaignStatus>(campaign?.status ?? 'DRAFT');
  const [valueType, setValueType] = useState<Campaign['value_type']>(campaign?.value_type ?? 'PERCENT');
  const [amount, setAmount] = useState(String(campaign?.percent ?? campaign?.amount?.amount ?? ''));
  const [from, setFrom] = useState(toDate(campaign?.valid_from));
  const [until, setUntil] = useState(toDate(campaign?.valid_until));
  const [limit, setLimit] = useState(campaign?.usage_limit ? String(campaign.usage_limit) : '');
  const [combinable, setCombinable] = useState(campaign?.combinable ?? false);

  const save = useAction(() => adminApi.saveCampaign(campaign?.id ?? null, {
    name: name.trim(), code: code.trim().toUpperCase(), type, status, value_type: valueType,
    percent: valueType === 'PERCENT' ? Number(amount) : null,
    amount: valueType === 'FIXED_AMOUNT' ? { amount: Number(amount), currency: 'VND' } : null,
    valid_from: from, valid_until: until, combinable, usage_limit: limit ? Number(limit) : null,
  }), { inline: true, success: 'Đã lưu chương trình.', invalidate: [['admin', 'campaigns']], onSuccess: onClose });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };

  return (
    <Modal open wide title={campaign ? `Sửa ${campaign.name}` : 'Tạo chương trình'} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <div className="form-grid">
          <Field label="Tên chương trình" required><Input value={name} required maxLength={120} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label="Loại" required>
            <Select value={type} onChange={(e) => setType(e.target.value as CampaignType)}>
              {Object.entries(campaignType)
                // Member discounts need a customer segment, which has no screen yet.
                .filter(([v]) => v !== 'MEMBER_DISCOUNT' || campaign?.type === v)
                .map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </Select>
          </Field>
          {type === 'COUPON' && <Field label="Mã giảm giá" required><Input value={code} required maxLength={40} onChange={(e) => setCode(e.target.value)} /></Field>}
          <Field label="Trạng thái" required>
            <Select value={status} onChange={(e) => setStatus(e.target.value as CampaignStatus)}>
              {Object.entries(campaignStatus).map(([v, s]) => <option key={v} value={v}>{s.label}</option>)}
            </Select>
          </Field>
          <Field label="Kiểu giảm" required>
            <Select value={valueType} onChange={(e) => setValueType(e.target.value as Campaign['value_type'])}>
              <option value="PERCENT">Phần trăm</option>
              <option value="FIXED_AMOUNT">Số tiền cố định</option>
            </Select>
          </Field>
          <Field label={valueType === 'PERCENT' ? 'Mức giảm (%)' : 'Mức giảm (VND)'} required>
            <Input type="number" min={1} step={1} value={amount} required onChange={(e) => setAmount(e.target.value)} />
          </Field>
          <Field label="Bắt đầu" required><Input type="date" value={from} required onChange={(e) => setFrom(e.target.value)} /></Field>
          <Field label="Kết thúc" required><Input type="date" value={until} required onChange={(e) => setUntil(e.target.value)} /></Field>
          <Field label="Giới hạn lượt dùng" hint="Để trống là không giới hạn."><Input type="number" min={1} step={1} value={limit} onChange={(e) => setLimit(e.target.value)} /></Field>
          <div className="row"><Checkbox label="Được dùng chung với ưu đãi khác" checked={combinable} onChange={(e) => setCombinable(e.target.checked)} /></div>
        </div>
        <FormError error={save.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending}>Lưu</Button>
        </div>
      </form>
    </Modal>
  );
}
