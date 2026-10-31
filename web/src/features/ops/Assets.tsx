import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Checkbox, EmptyState, Field, FormError, Input, PageHeader, Pagination, Select, Skeleton, StatusBadge, Textarea } from '../../components/ui';
import { inventoryApi } from '../../lib/api/endpoints';
import type { Asset, AssetAction, AssetStatus } from '../../lib/api/types';
import { useAuth } from '../../lib/auth';
import { formatDateTime } from '../../lib/format';
import { useDebounced } from '../../lib/hooks';
import { assetAction, assetStatus } from '../../lib/labels';

const pageSize = 20;
const invalidate = [['assets'], ['ops']];

type Dialog = { kind: 'action'; asset: Asset; action: AssetAction } | { kind: 'history'; asset: Asset } | { kind: 'register' } | null;

export default function Assets() {
  const { can } = useAuth();
  const [q, setQ] = useState('');
  const [status, setStatus] = useState<AssetStatus | ''>('');
  const [page, setPage] = useState(1);
  const [dialog, setDialog] = useState<Dialog>(null);
  const close = () => setDialog(null);

  const term = useDebounced(q);
  const result = useQuery({
    queryKey: ['assets', { term, status, page }],
    queryFn: () => inventoryApi.assets({ q: term, status, page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });

  const columns: Column<Asset>[] = [
    { key: 'code', header: 'Mã tài sản', render: (a) => <button type="button" className="cell-title mono" style={{ background: 'none', border: 0, padding: 0 }} onClick={() => setDialog({ kind: 'history', asset: a })}>{a.code}</button> },
    { key: 'item', header: 'Món', render: (a) => <span>{a.item_name}<span className="cell-sub">{a.sku}</span></span> },
    { key: 'variant', header: 'Size · Màu', render: (a) => `${a.size} · ${a.color}` },
    { key: 'status', header: 'Tình trạng', render: (a) => <StatusBadge status={assetStatus[a.status]} /> },
    { key: 'booking', header: 'Đơn', render: (a) => (a.booking_code ? <span className="mono">{a.booking_code}</span> : '') },
    { key: 'updated', header: 'Cập nhật', render: (a) => <span className="nowrap">{formatDateTime(a.updated_at)}</span> },
    {
      key: 'actions', header: 'Thao tác',
      render: (a) => (
        <span className="row" style={{ gap: 4 }}>
          {a.actions.map((action) => (
            <Button key={action} variant="outline" size="sm" onClick={() => setDialog({ kind: 'action', asset: a, action })}>{assetAction[action].label}</Button>
          ))}
        </span>
      ),
    },
  ];

  return (
    <>
      <PageHeader title="Tài sản cho thuê" subtitle="Tình trạng thực tế của từng món. Món chỉ sẵn sàng sau khi đã kiểm tra hoặc vệ sinh xong."
        actions={can('inventory.manage') && <Button icon="plus" onClick={() => setDialog({ kind: 'register' })}>Nhập tài sản</Button>} />
      <div className="toolbar">
        <Field label="Tìm kiếm">
          <Input type="search" placeholder="Mã tài sản, SKU, tên món" value={q} onChange={(e) => { setQ(e.target.value); setPage(1); }} />
        </Field>
        <Field label="Tình trạng">
          <Select value={status} onChange={(e) => { setStatus(e.target.value as AssetStatus | ''); setPage(1); }}>
            <option value="">Tất cả</option>
            {Object.entries(assetStatus).map(([value, s]) => <option key={value} value={value}>{s.label}</option>)}
          </Select>
        </Field>
      </div>
      <div className="card">
        <QueryState query={result} skeleton={<Skeleton height={320} />}>
          {(data) => data.items.length === 0
            ? <EmptyState icon="box" title="Không có tài sản phù hợp" />
            : <DataTable rows={data.items} columns={columns} rowKey={(a) => a.id} caption="Danh sách tài sản" />}
        </QueryState>
      </div>
      {result.data && <Pagination page={result.data} onChange={setPage} />}

      {dialog?.kind === 'action' && <ActionDialog asset={dialog.asset} action={dialog.action} onClose={close} />}
      {dialog?.kind === 'history' && <HistoryDialog asset={dialog.asset} onClose={close} />}
      {dialog?.kind === 'register' && <RegisterDialog onClose={close} />}
    </>
  );
}

function ActionDialog({ asset, action, onClose }: { asset: Asset; action: AssetAction; onClose: () => void }) {
  const meta = assetAction[action];
  const [reason, setReason] = useState('');
  const [checked, setChecked] = useState(false);
  const change = useAction(() => inventoryApi.changeStatus(asset.id, { action, reason: reason.trim(), checklist_completed: checked }), {
    inline: true, success: `Đã cập nhật ${asset.code}.`, invalidate, onSuccess: onClose,
  });
  const ready = (!meta.needsReason || reason.trim()) && (!meta.needsChecklist || checked);
  return (
    <Modal open title={`${meta.label}: ${asset.code}`} onClose={onClose}>
      <div className="stack">
        <p className="small muted">{asset.item_name} · {asset.size} · {asset.color}</p>
        {meta.needsChecklist && <Checkbox label="Đã hoàn tất danh sách kiểm tra cho món này" checked={checked} onChange={(e) => setChecked(e.target.checked)} />}
        <Field label={meta.needsReason ? 'Lý do' : 'Ghi chú'} required={meta.needsReason}>
          <Textarea value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <FormError error={change.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={!ready} loading={change.isPending} onClick={() => change.mutate(undefined)}>{meta.label}</Button>
        </div>
      </div>
    </Modal>
  );
}

function HistoryDialog({ asset, onClose }: { asset: Asset; onClose: () => void }) {
  const history = useQuery({ queryKey: ['assets', 'history', asset.id], queryFn: () => inventoryApi.history(asset.id) });
  return (
    <Modal open title={`Lịch sử ${asset.code}`} onClose={onClose}>
      <QueryState query={history} skeleton={<Skeleton height={160} />}>
        {(entries) => entries.length === 0 ? <p className="small muted">Chưa có lịch sử.</p> : (
          <ol className="timeline">
            {entries.map((e, i) => (
              <li key={`${e.at}-${i}`}>
                <strong>{assetStatus[e.to].label}</strong>
                <span className="cell-sub">{formatDateTime(e.at)} · {e.actor}</span>
                {e.reason && <span className="cell-sub">{e.reason}</span>}
              </li>
            ))}
          </ol>
        )}
      </QueryState>
    </Modal>
  );
}

function RegisterDialog({ onClose }: { onClose: () => void }) {
  const [code, setCode] = useState('');
  const [variant, setVariant] = useState('');
  const register = useAction(() => inventoryApi.register(code.trim(), variant.trim()), {
    inline: true, success: 'Đã nhập tài sản. Tài sản cần qua kiểm tra trước khi cho thuê.', invalidate, onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    register.mutate(undefined);
  };
  return (
    <Modal open title="Nhập tài sản mới" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label="Mã tài sản" required><Input value={code} required maxLength={64} onChange={(e) => setCode(e.target.value)} /></Field>
        <Field label="Mã biến thể (SKU)" required><Input value={variant} required maxLength={64} onChange={(e) => setVariant(e.target.value)} /></Field>
        <FormError error={register.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={register.isPending}>Nhập tài sản</Button>
        </div>
      </form>
    </Modal>
  );
}
