import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { DataTable, type Column } from '../../components/DataTable';
import { Modal, QueryState, ReasonDialog, useAction } from '../../components/feedback';
import {
  Badge, Button, Card, EmptyState, Field, FormError, Input, Notice, PageHeader, Pagination, Select, Skeleton, StatusBadge, Tabs,
} from '../../components/ui';
import { revenueApi } from '../../lib/api/endpoints';
import type { Movement, ReconciliationReport, Transaction, TxnKind } from '../../lib/api/types';
import { useAuth } from '../../lib/auth';
import { businessToday, formatDateTime, formatMoney, formatNumber } from '../../lib/format';
import { bookingStatus, bucket, paymentMethod, revenueCategory, revenueMismatch, txnKind } from '../../lib/labels';
import { monthStart, PeriodFields } from './period';

type Tab = 'report' | 'transactions' | 'cash';

const vnd = (amount: number) => ({ amount, currency: 'VND' as const });
const bookingLink = (id: string, code: string) => <Link className="link" to={`/ops/bookings/${id}`}>{code}</Link>;

/** Decision D41 (tax-policy §10.2, §10.3): money against revenue, and transactions against the statement. */
export default function Reconciliation() {
  const [tab, setTab] = useState<Tab>('report');
  const [from, setFrom] = useState(monthStart);
  const [to, setTo] = useState(businessToday());
  return (
    <>
      <PageHeader title="Đối soát" subtitle="Doanh thu so với tiền thực nhận, giao dịch so với sao kê, và dòng tiền." />
      <div className="toolbar">
        <PeriodFields from={from} to={to} onChange={(f, t) => { setFrom(f); setTo(t); }} />
      </div>
      <Tabs label="Phần" value={tab} onChange={setTab} options={[
        { value: 'report', label: 'Báo cáo đối soát' },
        { value: 'transactions', label: 'Giao dịch' },
        { value: 'cash', label: 'Dòng tiền' },
      ]} />
      {tab === 'report' && <ReportTab from={from} to={to} />}
      {tab === 'transactions' && <TransactionsTab from={from} to={to} />}
      {tab === 'cash' && <CashTab from={from} to={to} />}
    </>
  );
}

function Stat({ label, value, foot, tone }: { label: string; value: string; foot?: ReactNode; tone?: 'warn' | 'alert' }) {
  return (
    <div className={`stat${tone ? ` stat-${tone}` : ''}`}>
      <span className="stat-label">{label}</span>
      <span className="stat-value tnum">{value}</span>
      {foot && <span className="stat-foot">{foot}</span>}
    </div>
  );
}

function more(total: number, shown: number) {
  return total > shown && <p className="card-body small muted">Đang hiện {shown} trên {formatNumber(total)} dòng.</p>;
}

function ReportTab({ from, to }: { from: string; to: string }) {
  const result = useQuery({ queryKey: ['revenue', 'reconciliation', from, to], queryFn: () => revenueApi.reconciliation({ from, to }), enabled: Boolean(from && to) });
  return (
    <QueryState query={result} skeleton={<Skeleton height={420} />}>
      {(r) => <ReportView report={r} />}
    </QueryState>
  );
}

function ReportView({ report: r }: { report: ReconciliationReport }) {
  const net = r.received.amount - r.refunded.amount;
  return (
    <div className="stack-lg">
      <div className="stat-grid stat-grid-3">
        <Stat label="Doanh thu ghi nhận" value={formatMoney(r.recognised)} foot={`${formatNumber(r.ended_bookings)} đơn kết thúc trong kỳ`} />
        <Stat label="Tiền đã nhận" value={formatMoney(r.received)} foot={`Đã hoàn ${formatMoney(r.refunded)}`} />
        <Stat label="Tiền thực nhận" value={formatMoney(vnd(net))} foot="Gồm cả tiền bảo đảm và giữ lịch đang giữ" />
      </div>
      <p className="small muted">
        Doanh thu và tiền thực nhận lệch nhau là bình thường: tiền bảo đảm và tiền giữ lịch không phải doanh thu, và tiền có thể thu trước
        khi đơn kết thúc. Các mục dưới đây là những chỗ cần xem.
      </p>

      <Card title={`Đơn kết thúc có chênh lệch (${r.mismatched.total})`} flush>
        {r.mismatched.items.length === 0
          ? <EmptyState icon="check" title="Không có đơn nào chênh lệch" />
          : <DataTable rows={r.mismatched.items} rowKey={(x) => x.booking_id} caption="Đơn kết thúc có chênh lệch" columns={[
            { key: 'code', header: 'Đơn', render: (x) => <span><span className="cell-title">{bookingLink(x.booking_id, x.booking_code)}</span><span className="cell-sub">{bookingStatus[x.status].label} · {formatDateTime(x.ended_at)}</span></span> },
            { key: 'charged', header: 'Đã tính', align: 'right', render: (x) => <span className="tnum">{formatMoney(x.charged)}</span> },
            { key: 'recognised', header: 'Đã ghi doanh thu', align: 'right', render: (x) => <span className="tnum">{formatMoney(x.recognised)}</span> },
            { key: 'covered', header: 'Đã thu', align: 'right', render: (x) => <span className="tnum">{formatMoney(x.covered)}</span> },
            { key: 'why', header: 'Vấn đề', render: (x) => <span className="row" style={{ gap: 4 }}>{x.mismatches.map((m) => <StatusBadge key={m} status={revenueMismatch[m]} />)}</span> },
          ]} />}
        {more(r.mismatched.total, r.mismatched.items.length)}
      </Card>

      <Card title={`Giao dịch thành công chưa đối soát (${r.unreconciled.total})`} flush>
        {r.unreconciled.items.length === 0
          ? <EmptyState icon="check" title="Mọi giao dịch trong kỳ đã đối soát" />
          : <TxnTable rows={r.unreconciled.items} />}
        {more(r.unreconciled.total, r.unreconciled.items.length)}
      </Card>

      <Card title={`Giao dịch cần kiểm tra (${r.attention.total})`} flush>
        {r.attention.items.length === 0
          ? <EmptyState icon="check" title="Không có giao dịch cần kiểm tra" />
          : <DataTable rows={r.attention.items} rowKey={(x) => `${x.kind}-${x.id}`} caption="Giao dịch cần kiểm tra" columns={[
            { key: 'at', header: 'Thời điểm', render: (x) => formatDateTime(x.at) },
            { key: 'code', header: 'Đơn', render: (x) => bookingLink(x.booking_id, x.booking_code) },
            { key: 'kind', header: 'Loại', render: (x) => txnKind[x.kind] },
            { key: 'why', header: 'Vì sao', render: (x) => x.kind === 'REFUND' ? `Hoàn tiền thất bại${x.reason ? `: ${x.reason}` : ''}` : 'Thu thành công sau khi đơn đã hết hạn hoặc bị hủy; có thể phải hoàn' },
            { key: 'amount', header: 'Số tiền', align: 'right', render: (x) => <span className="tnum">{formatMoney(x.amount)}</span> },
          ]} />}
        {more(r.attention.total, r.attention.items.length)}
      </Card>

      <div className="grid-2">
        <Card title={`Đơn chưa ghi nhận doanh thu (${r.unrecognised.total})`} flush>
          {r.unrecognised.items.length === 0
            ? <EmptyState icon="check" title="Mọi đơn đã kết thúc đều đã ghi nhận" />
            : <DataTable rows={r.unrecognised.items} rowKey={(x) => x.booking_id} caption="Đơn chưa ghi nhận doanh thu" columns={[
              { key: 'code', header: 'Đơn', render: (x) => bookingLink(x.booking_id, x.booking_code) },
              { key: 'ended', header: 'Kết thúc', render: (x) => formatDateTime(x.ended_at) },
            ]} />}
          <p className="card-body small muted">Hệ thống tự ghi bù mỗi giờ khi đã có hồ sơ thuế cho thời điểm đơn kết thúc.</p>
        </Card>
        <Card title={`Khoản thu khác cần xem phân loại (${r.other_charges.total})`} flush>
          {r.other_charges.items.length === 0
            ? <EmptyState icon="check" title="Không có khoản thu khác" />
            : <DataTable rows={r.other_charges.items} rowKey={(x) => x.id} caption="Khoản thu khác" columns={[
              { key: 'code', header: 'Đơn', render: (x) => x.booking_id ? bookingLink(x.booking_id, x.booking_code ?? '') : x.channel },
              { key: 'cat', header: 'Nhóm', render: (x) => revenueCategory[x.category] },
              { key: 'net', header: 'Số tiền', align: 'right', render: (x) => <span className="tnum">{formatMoney(x.net)}</span> },
            ]} />}
        </Card>
      </div>
    </div>
  );
}

function TxnTable({ rows, onReconcile, onRevoke }: { rows: Transaction[]; onReconcile?: (t: Transaction) => void; onRevoke?: (t: Transaction) => void }) {
  const columns: Column<Transaction>[] = [
    { key: 'at', header: 'Thời điểm', render: (t) => formatDateTime(t.at) },
    { key: 'code', header: 'Đơn', render: (t) => bookingLink(t.booking_id, t.booking_code) },
    {
      key: 'kind', header: 'Loại',
      render: (t) => <span><span className="cell-title">{txnKind[t.kind]} · {paymentMethod[t.method]}</span><span className="cell-sub">{t.buckets.map((b) => bucket[b]).join(', ')}{t.reference ? ` · ${t.reference}` : ''}</span></span>,
    },
    { key: 'amount', header: 'Số tiền', align: 'right', render: (t) => <strong className="tnum">{t.kind === 'REFUND' ? '−' : ''}{formatMoney(t.amount)}</strong> },
    {
      key: 'recon', header: 'Đối soát',
      render: (t) => t.reconciliation
        ? <span><Badge label="Đã đối soát" tone="success" /><span className="cell-sub">{t.reconciliation.statement_ref} · {formatDateTime(t.reconciliation.at)}</span></span>
        : <Badge label="Chưa đối soát" tone="warning" />,
    },
  ];
  if (onReconcile || onRevoke) {
    columns.push({
      key: 'action', header: '',
      render: (t) => t.reconciliation
        ? onRevoke && <Button variant="ghost" size="sm" onClick={() => onRevoke(t)}>Hủy đối soát</Button>
        : onReconcile && <Button size="sm" onClick={() => onReconcile(t)}>Đối soát</Button>,
    });
  }
  return <DataTable rows={rows} columns={columns} rowKey={(t) => `${t.kind}-${t.id}`} caption="Giao dịch" />;
}

function TransactionsTab({ from, to }: { from: string; to: string }) {
  const { can } = useAuth();
  const [kind, setKind] = useState<TxnKind | ''>('');
  const [reconciled, setReconciled] = useState<'' | 'true' | 'false'>('false');
  const [page, setPage] = useState(1);
  const [marking, setMarking] = useState<Transaction | null>(null);
  const [revoking, setRevoking] = useState<Transaction | null>(null);
  const result = useQuery({
    queryKey: ['revenue', 'transactions', { from, to, kind, reconciled, page }],
    queryFn: () => revenueApi.transactions({ from, to, kind, reconciled, page, page_size: 20 }),
    enabled: Boolean(from && to),
    placeholderData: keepPreviousData,
  });
  const revoke = useAction((reason: string) => revenueApi.revokeReconciliation(revoking!.reconciliation!.id, reason), {
    inline: true, success: 'Đã hủy đối soát.', invalidate: [['revenue']], onSuccess: () => setRevoking(null),
  });
  const mayMark = can('payment.reconcile');
  return (
    <div className="stack-lg">
      <div className="toolbar">
        <Field label="Loại">
          <Select value={kind} onChange={(e) => { setKind(e.target.value as TxnKind | ''); setPage(1); }}>
            <option value="">Thu và hoàn</option>
            <option value="PAYMENT">Khoản thu</option>
            <option value="REFUND">Khoản hoàn</option>
          </Select>
        </Field>
        <Field label="Trạng thái">
          <Select value={reconciled} onChange={(e) => { setReconciled(e.target.value as '' | 'true' | 'false'); setPage(1); }}>
            <option value="false">Chưa đối soát</option>
            <option value="true">Đã đối soát</option>
            <option value="">Tất cả</option>
          </Select>
        </Field>
      </div>
      <Card flush>
        <QueryState query={result} skeleton={<Skeleton height={300} />}>
          {(data) => data.items.length === 0
            ? <EmptyState icon="list" title="Không có giao dịch nào" />
            : <TxnTable rows={data.items} onReconcile={mayMark ? setMarking : undefined} onRevoke={mayMark ? setRevoking : undefined} />}
        </QueryState>
      </Card>
      {result.data && <Pagination page={result.data} onChange={setPage} />}
      {marking && <ReconcileDialog txn={marking} onClose={() => setMarking(null)} />}
      {revoking && (
        <ReasonDialog open title="Hủy đối soát" confirmLabel="Hủy đối soát" danger onClose={() => setRevoking(null)}
          description={<p>Lần đối soát vẫn được giữ trong lịch sử; giao dịch trở lại danh sách chưa đối soát.</p>}
          pending={revoke.isPending} error={revoke.error} onConfirm={(reason) => revoke.mutate(reason)} />
      )}
    </div>
  );
}

function ReconcileDialog({ txn, onClose }: { txn: Transaction; onClose: () => void }) {
  const [statementRef, setStatementRef] = useState('');
  const [note, setNote] = useState('');
  const save = useAction(() => revenueApi.reconcile(txn.kind, txn.id, { statement_ref: statementRef.trim(), note: note.trim() }), {
    inline: true, success: 'Đã đối soát giao dịch.', invalidate: [['revenue']], onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };
  return (
    <Modal open title={`Đối soát ${txnKind[txn.kind].toLowerCase()} ${formatMoney(txn.amount)}`} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <p className="small muted">Đơn {txn.booking_code} · {paymentMethod[txn.method]} · {formatDateTime(txn.at)}. Đối chiếu với sao kê ngân hàng hoặc sổ quỹ rồi ghi dòng tương ứng.</p>
        <FormError error={save.error} />
        <Field label="Dòng sao kê / sổ quỹ" required hint="Ví dụ: mã giao dịch ngân hàng, hoặc 'Sổ quỹ 05/11 dòng 3'.">
          <Input value={statementRef} maxLength={80} required onChange={(e) => setStatementRef(e.target.value)} />
        </Field>
        <Field label="Ghi chú">
          <Input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
        </Field>
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending} disabled={!statementRef.trim()}>Xác nhận đối soát</Button>
        </div>
      </form>
    </Modal>
  );
}

function movementRows(ms: Movement[]) {
  return ms.length === 0
    ? <EmptyState icon="list" title="Không có" />
    : <DataTable rows={ms} rowKey={(m) => `${m.method}-${m.bucket}`} caption="Dòng tiền" columns={[
      { key: 'method', header: 'Phương thức', render: (m) => paymentMethod[m.method] },
      { key: 'bucket', header: 'Khoản', render: (m) => bucket[m.bucket] },
      { key: 'count', header: 'Số giao dịch', align: 'right', render: (m) => <span className="tnum">{m.count}</span> },
      { key: 'amount', header: 'Số tiền', align: 'right', render: (m) => <strong className="tnum">{formatMoney(m.amount)}</strong> },
    ]} />;
}

function CashTab({ from, to }: { from: string; to: string }) {
  const result = useQuery({ queryKey: ['revenue', 'cash-flow', from, to], queryFn: () => revenueApi.cashFlow({ from, to }), enabled: Boolean(from && to) });
  return (
    <QueryState query={result} skeleton={<Skeleton height={360} />}>
      {(c) => {
        const p = c.position;
        return (
          <div className="stack-lg">
            <Notice tone="info">Số dư bên dưới là thời điểm hiện tại trên mọi đơn, không phụ thuộc khoảng ngày đã chọn.</Notice>
            <div className="stat-grid stat-grid-3">
              <Stat label="Tiền bảo đảm đang giữ" value={formatMoney(p.security_held)} />
              <Stat label="Tiền giữ lịch đang giữ" value={formatMoney(p.reservation_held)} />
              <Stat label="Khách còn nợ (đơn đã kết thúc)" value={formatMoney(p.owed_ended)} tone={p.owed_ended.amount > 0 ? 'alert' : undefined}
                foot={`${formatNumber(p.owed_ended_bookings)} đơn`} />
              <Stat label="Còn phải thu (đơn đang mở)" value={formatMoney(p.due_open)} foot="Gồm cả đơn đang giữ chỗ chưa thanh toán" />
              <Stat label="Hoàn tiền đang chờ" value={formatMoney(p.pending_refunds)} tone={p.pending_refunds.amount > 0 ? 'warn' : undefined} />
              <Stat label="Thu thừa chờ hoàn" value={formatMoney(p.overpaid)} foot={`Thanh toán đang chờ: ${formatMoney(p.pending_payments)}`} />
            </div>
            <div className="grid-2">
              <Card title="Tiền đã nhận trong kỳ" flush>{movementRows(c.received)}</Card>
              <Card title="Tiền đã hoàn trong kỳ" flush>{movementRows(c.refunded)}</Card>
            </div>
          </div>
        );
      }}
    </QueryState>
  );
}
