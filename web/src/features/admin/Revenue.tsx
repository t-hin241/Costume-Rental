import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { Modal, QueryState, ReasonDialog, useAction } from '../../components/feedback';
import {
  Badge, Button, Card, EmptyState, Field, FormError, Input, Notice, PageHeader, Pagination, Select, Skeleton, Tabs, btn, fieldError,
} from '../../components/ui';
import { revenueApi } from '../../lib/api/endpoints';
import type {
  ManualRevenueInput, ReportGroup, RevenueCategory, RevenueEntry, RevenueQuery, RevenueReportRow, RevenueSource, TaxProfile, TaxProfileInput,
} from '../../lib/api/types';
import { useAuth } from '../../lib/auth';
import { businessToday, formatBusinessDate, formatDateTime, formatMoney, formatNumber } from '../../lib/format';
import { reportGroup, revenueCategory, revenueSource } from '../../lib/labels';
import { monthStart, PeriodFields } from './period';
import { ThresholdCard } from './ThresholdCard';

type Tab = 'year' | 'ledger' | 'profiles';

const categories = Object.keys(revenueCategory) as RevenueCategory[];
const vnd = (amount: number) => ({ amount, currency: 'VND' as const });

/** Decision D41: the revenue ledger and the threshold watch. The system computes no tax. */
export default function Revenue() {
  const [tab, setTab] = useState<Tab>('year');
  return (
    <>
      <PageHeader title="Doanh thu & thuế" subtitle="Sổ doanh thu, theo dõi ngưỡng doanh thu năm và hồ sơ thuế." />
      <Notice tone="info">
        Hệ thống không tính thuế, không cộng thuế vào tiền khách trả và không phát hành hóa đơn. Doanh thu được ghi khi đơn hoàn tất
        hoặc quyết toán hủy; tiền bảo đảm và tiền giữ lịch không phải doanh thu. Báo cáo này là số liệu nội bộ, không phải tờ khai thuế.
      </Notice>
      <Tabs label="Phần" value={tab} onChange={setTab} options={[
        { value: 'year', label: 'Ngưỡng năm' },
        { value: 'ledger', label: 'Sổ doanh thu' },
        { value: 'profiles', label: 'Hồ sơ thuế' },
      ]} />
      {tab === 'year' && <YearTab />}
      {tab === 'ledger' && <LedgerTab />}
      {tab === 'profiles' && <ProfilesTab />}
    </>
  );
}

function YearTab() {
  const current = Number(businessToday().slice(0, 4));
  const [year, setYear] = useState(current);
  const result = useQuery({ queryKey: ['revenue', 'year', year], queryFn: () => revenueApi.year(year) });
  return (
    <div className="stack-lg">
      <div className="toolbar">
        <Field label="Năm">
          <Select value={year} onChange={(e) => setYear(Number(e.target.value))}>
            {[current, current - 1, current - 2].map((y) => <option key={y} value={y}>{y}</option>)}
          </Select>
        </Field>
      </div>
      <ThresholdCard year={year} />
      <QueryState query={result} skeleton={<Skeleton height={200} />}>
        {(y) => y.revenue && (
          <Card title="Theo nhóm khoản thu" flush>
            <DataTable rows={y.by_category} rowKey={(r) => r.category} caption={`Doanh thu năm ${y.year} theo nhóm`} columns={[
              { key: 'category', header: 'Nhóm', render: (r) => revenueCategory[r.category] },
              { key: 'amount', header: 'Doanh thu', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.amount)}</span> },
            ]} />
            <div className="card-body small muted">
              Trước giảm giá {formatMoney(y.list!)}, giảm giá {formatMoney(y.discount!)}, sau giảm giá {formatMoney(y.revenue)}. Hồ sơ {y.profile_version}.
            </div>
          </Card>
        )}
      </QueryState>
    </div>
  );
}

function LedgerTab() {
  const { can } = useAuth();
  const [query, setQuery] = useState<RevenueQuery>({ from: monthStart(), to: businessToday(), category: '', source: '', booking: '', page: 1, page_size: 20 });
  const [group, setGroup] = useState<ReportGroup>('day');
  const [adding, setAdding] = useState(false);
  const [reversing, setReversing] = useState<RevenueEntry | null>(null);
  const set = (patch: Partial<RevenueQuery>) => setQuery((q) => ({ ...q, page: 1, ...patch }));
  const report = useQuery({
    queryKey: ['revenue', 'report', query.from, query.to, group],
    queryFn: () => revenueApi.report({ from: query.from, to: query.to, group }),
    enabled: Boolean(query.from && query.to),
  });
  const entries = useQuery({
    queryKey: ['revenue', 'entries', query],
    queryFn: () => revenueApi.entries(query),
    enabled: Boolean(query.from && query.to),
    placeholderData: keepPreviousData,
  });
  const reverse = useAction((reason: string) => revenueApi.reverseEntry(reversing!.id, reason), {
    inline: true, success: 'Đã đảo bút toán.', invalidate: [['revenue']], onSuccess: () => setReversing(null),
  });

  const columns: Column<RevenueEntry>[] = [
    { key: 'at', header: 'Ngày ghi nhận', render: (r) => formatDateTime(r.recognized_at) },
    {
      key: 'source', header: 'Nguồn',
      render: (r) => (
        <span>
          <span className="cell-title">{r.booking_code ?? r.channel}</span>
          <span className="cell-sub">{revenueSource[r.source]}{r.document_ref ? ` · ${r.document_ref}` : ''}</span>
        </span>
      ),
    },
    {
      key: 'category', header: 'Nhóm',
      render: (r) => <span>{revenueCategory[r.category]}{r.reverses && <> <Badge label="Bút toán đảo" tone="warning" /></>}</span>,
    },
    { key: 'list', header: 'Trước giảm', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.list)}</span> },
    { key: 'discount', header: 'Giảm giá', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.discount)}</span> },
    { key: 'net', header: 'Doanh thu', align: 'right', render: (r) => <strong className="tnum">{formatMoney(r.net)}</strong> },
    {
      key: 'note', header: 'Ghi chú',
      render: (r) => <span className="small">{r.reason ?? r.note ?? ''}</span>,
    },
    {
      key: 'action', header: '',
      render: (r) => can('revenue.record') && r.source === 'MANUAL' && !r.reverses &&
        <Button variant="ghost" size="sm" onClick={() => setReversing(r)}>Đảo</Button>,
    },
  ];

  return (
    <div className="stack-lg">
      <div className="toolbar">
        <PeriodFields from={query.from} to={query.to} onChange={(from, to) => set({ from, to })} />
        <Field label="Gộp theo">
          <Select value={group} onChange={(e) => setGroup(e.target.value as ReportGroup)}>
            {(Object.keys(reportGroup) as ReportGroup[]).map((g) => <option key={g} value={g}>{reportGroup[g]}</option>)}
          </Select>
        </Field>
      </div>
      <Card title="Tổng hợp doanh thu" flush>
        <QueryState query={report} skeleton={<Skeleton height={200} />}>
          {(r) => r.rows.length === 0
            ? <EmptyState icon="chart" title="Chưa có doanh thu trong khoảng này" />
            : <ReportTable rows={r.rows} group={r.group} />}
        </QueryState>
      </Card>
      <Card title="Bút toán doanh thu" flush actions={(
        <div className="action-bar">
          <a className={btn('outline', 'sm')} href={revenueApi.exportURL({ ...query, page: undefined, page_size: undefined })} download>Xuất CSV</a>
          {can('revenue.record') && <Button size="sm" icon="plus" onClick={() => setAdding(true)}>Nhập doanh thu ngoài website</Button>}
        </div>
      )}>
        <div className="card-body">
          <div className="toolbar">
            <Field label="Nguồn">
              <Select value={query.source} onChange={(e) => set({ source: e.target.value as RevenueSource | '' })}>
                <option value="">Tất cả</option>
                {(Object.keys(revenueSource) as RevenueSource[]).map((s) => <option key={s} value={s}>{revenueSource[s]}</option>)}
              </Select>
            </Field>
            <Field label="Nhóm">
              <Select value={query.category} onChange={(e) => set({ category: e.target.value as RevenueCategory | '' })}>
                <option value="">Tất cả</option>
                {categories.map((c) => <option key={c} value={c}>{revenueCategory[c]}</option>)}
              </Select>
            </Field>
            <Field label="Mã đơn">
              <Input value={query.booking} maxLength={40} placeholder="BK-…" onChange={(e) => set({ booking: e.target.value.trim().toUpperCase() })} />
            </Field>
          </div>
        </div>
        <QueryState query={entries} skeleton={<Skeleton height={260} />}>
          {(page) => page.items.length === 0
            ? <EmptyState icon="list" title="Không có bút toán nào" />
            : <DataTable rows={page.items} columns={columns} rowKey={(r) => r.id} caption="Bút toán doanh thu" />}
        </QueryState>
      </Card>
      {entries.data && <Pagination page={entries.data} onChange={(p) => setQuery((q) => ({ ...q, page: p }))} />}
      {adding && <ManualEntryDialog onClose={() => setAdding(false)} />}
      {reversing && (
        <ReasonDialog open title="Đảo bút toán doanh thu" confirmLabel="Đảo bút toán" danger onClose={() => setReversing(null)}
          description={<p>Bút toán {formatMoney(reversing.net)} ({reversing.channel}) được giữ lại; một bút toán đảo được ghi vào hôm nay.</p>}
          pending={reverse.isPending} error={reverse.error} onConfirm={(reason) => reverse.mutate(reason)} />
      )}
    </div>
  );
}

function ReportTable({ rows, group }: { rows: RevenueReportRow[]; group: ReportGroup }) {
  const total = rows.reduce((s, r) => ({ list: s.list + r.list.amount, discount: s.discount + r.discount.amount, net: s.net + r.net.amount }),
    { list: 0, discount: 0, net: 0 });
  const period = (d: string) => {
    if (group === 'day') return formatBusinessDate(d);
    if (group === 'month') return `Tháng ${d.slice(5, 7)}/${d.slice(0, 4)}`;
    if (group === 'quarter') return `Quý ${Math.floor((Number(d.slice(5, 7)) - 1) / 3) + 1}/${d.slice(0, 4)}`;
    return `Năm ${d.slice(0, 4)}`;
  };
  return (
    <>
      <DataTable rows={rows} rowKey={(r) => `${r.period}-${r.category}`} caption="Doanh thu theo kỳ và nhóm" columns={[
        { key: 'period', header: 'Kỳ', render: (r) => period(r.period) },
        { key: 'category', header: 'Nhóm', render: (r) => revenueCategory[r.category] },
        { key: 'entries', header: 'Bút toán', align: 'right', render: (r) => <span className="tnum">{r.entries}</span> },
        { key: 'list', header: 'Trước giảm', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.list)}</span> },
        { key: 'discount', header: 'Giảm giá', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.discount)}</span> },
        { key: 'net', header: 'Doanh thu', align: 'right', render: (r) => <strong className="tnum">{formatMoney(r.net)}</strong> },
      ]} />
      <div className="card-body between small">
        <span className="muted">Cả khoảng</span>
        <span className="tnum">
          Trước giảm {formatMoney(vnd(total.list))} · Giảm giá {formatMoney(vnd(total.discount))} · <strong>Doanh thu {formatMoney(vnd(total.net))}</strong>
        </span>
      </div>
    </>
  );
}

function ManualEntryDialog({ onClose }: { onClose: () => void }) {
  const [draft, setDraft] = useState<ManualRevenueInput>({
    category: 'RENTAL_REVENUE', amount: vnd(0), date: businessToday(), channel: '', note: '', document_ref: '',
  });
  const save = useAction((body: ManualRevenueInput) => revenueApi.addEntry(body), {
    inline: true, success: 'Đã ghi doanh thu.', invalidate: [['revenue']], onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(draft);
  };
  return (
    <Modal open title="Nhập doanh thu ngoài website" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <p className="small muted">
          Dùng cho doanh thu bán ở kênh khác (tại quầy không qua hệ thống, hội chợ, mạng xã hội) để ngưỡng năm tính đủ toàn bộ hoạt động kinh doanh.
          Bút toán không sửa được; nếu nhập sai, hãy đảo rồi nhập lại.
        </p>
        <FormError error={save.error} inlineFields />
        <div className="form-grid">
          <Field label="Ngày phát sinh" required error={fieldError(save.error, 'date')}>
            <Input type="date" value={draft.date} max={businessToday()} required onChange={(e) => setDraft({ ...draft, date: e.target.value })} />
          </Field>
          <Field label="Nhóm" required>
            <Select value={draft.category} onChange={(e) => setDraft({ ...draft, category: e.target.value as RevenueCategory })}>
              {categories.map((c) => <option key={c} value={c}>{revenueCategory[c]}</option>)}
            </Select>
          </Field>
          <Field label="Số tiền (đồng)" required error={fieldError(save.error, 'amount')}>
            <Input type="number" min={1} step={1} inputMode="numeric" required value={draft.amount.amount || ''}
              onChange={(e) => setDraft({ ...draft, amount: vnd(Math.trunc(Number(e.target.value))) })} />
          </Field>
          <Field label="Kênh bán" required hint="Ví dụ: Hội chợ cosplay, Facebook.">
            <Input value={draft.channel} maxLength={80} required onChange={(e) => setDraft({ ...draft, channel: e.target.value })} />
          </Field>
          <Field label="Số chứng từ" hint="Phiếu thu, mã giao dịch ngân hàng…">
            <Input value={draft.document_ref} maxLength={80} onChange={(e) => setDraft({ ...draft, document_ref: e.target.value })} />
          </Field>
          <Field label="Mô tả">
            <Input value={draft.note} maxLength={500} onChange={(e) => setDraft({ ...draft, note: e.target.value })} />
          </Field>
        </div>
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending} disabled={!draft.channel.trim() || draft.amount.amount <= 0}>Ghi doanh thu</Button>
        </div>
      </form>
    </Modal>
  );
}

function ProfilesTab() {
  const { can } = useAuth();
  const [adding, setAdding] = useState(false);
  const result = useQuery({ queryKey: ['revenue', 'profiles'], queryFn: revenueApi.profiles });
  const columns: Column<TaxProfile>[] = [
    { key: 'version', header: 'Phiên bản', render: (r) => <strong>{r.version}</strong> },
    { key: 'threshold', header: 'Ngưỡng năm', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.annual_revenue_threshold)}</span> },
    { key: 'marks', header: 'Mốc cảnh báo', render: (r) => r.warning_percents.map((m) => `${m}%`).join(', ') },
    { key: 'from', header: 'Hiệu lực từ', render: (r) => formatDateTime(r.effective_from) },
    { key: 'until', header: 'Đến', render: (r) => r.effective_until ? formatDateTime(r.effective_until) : <Badge label="Đang áp dụng" tone="success" /> },
  ];
  return (
    <div className="stack-lg">
      <QueryState query={result} skeleton={<Skeleton height={200} />}>
        {(data) => (
          <>
            <Card title="Lịch sử hồ sơ thuế" flush actions={can('tax.manage') && (
              <Button size="sm" icon="plus" onClick={() => setAdding(true)}>Tạo phiên bản mới</Button>
            )}>
              {data.items.length === 0
                ? <EmptyState icon="settings" title="Chưa có hồ sơ thuế">Chưa có hồ sơ nên chưa ghi nhận doanh thu và chưa theo dõi ngưỡng.</EmptyState>
                : <DataTable rows={[...data.items].reverse()} columns={columns} rowKey={(r) => r.version} caption="Phiên bản hồ sơ thuế" />}
            </Card>
            {adding && <ProfileDialog first={data.items.length === 0} current={data.items.at(-1)} onClose={() => setAdding(false)} />}
          </>
        )}
      </QueryState>
      <p className="small muted">
        Hồ sơ chỉ gồm ngưỡng doanh thu năm và các mốc cảnh báo; không có thuế suất. Phiên bản không sửa được: tạo phiên bản mới
        thì phiên bản cũ kết thúc vào ngày bắt đầu của phiên bản mới, và doanh thu đã ghi giữ phiên bản của nó.
      </p>
    </div>
  );
}

function ProfileDialog({ first, current, onClose }: { first: boolean; current?: TaxProfile; onClose: () => void }) {
  const [draft, setDraft] = useState({
    version: '',
    threshold: current?.annual_revenue_threshold.amount ?? 1_000_000_000,
    thresholdFrom: current?.threshold_effective_from ?? '2026-01-01',
    marks: (current?.warning_percents ?? [70, 85, 95]).join(', '),
    from: first ? `${businessToday().slice(0, 4)}-01-01` : businessToday(),
  });
  const save = useAction((body: TaxProfileInput) => revenueApi.addProfile(body), {
    inline: true, success: 'Đã lưu phiên bản hồ sơ thuế.', invalidate: [['revenue']], onSuccess: onClose,
  });
  const marks = draft.marks.split(',').map((m) => Number(m.trim())).filter((m) => Number.isInteger(m) && m > 0);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({
      version: draft.version.trim(), annual_revenue_threshold: vnd(Math.trunc(draft.threshold)),
      threshold_effective_from: draft.thresholdFrom, warning_percents: marks, effective_from: draft.from,
    });
  };
  return (
    <Modal open title="Tạo phiên bản hồ sơ thuế" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <FormError error={save.error} inlineFields />
        <div className="form-grid">
          <Field label="Mã phiên bản" required hint="Ví dụ: tax-2026.">
            <Input value={draft.version} maxLength={64} required onChange={(e) => setDraft({ ...draft, version: e.target.value })} />
          </Field>
          <Field label="Hiệu lực từ ngày" required
            hint={first ? 'Phiên bản đầu tiên có thể bắt đầu trong quá khứ để ghi nhận các đơn đã kết thúc.' : 'Từ hôm nay trở đi.'}>
            <Input type="date" value={draft.from} min={first ? undefined : businessToday()} required onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
          </Field>
          <Field label="Ngưỡng doanh thu năm (đồng)" required>
            <Input type="number" min={1} step={1} required value={draft.threshold} onChange={(e) => setDraft({ ...draft, threshold: Number(e.target.value) })} />
          </Field>
          <Field label="Ngưỡng áp dụng từ ngày" required hint="Ngày văn bản quy định ngưỡng có hiệu lực.">
            <Input type="date" value={draft.thresholdFrom} required onChange={(e) => setDraft({ ...draft, thresholdFrom: e.target.value })} />
          </Field>
          <Field label="Mốc cảnh báo (%)" required hint={`Tăng dần, cách nhau bằng dấu phẩy. Đang hiểu là: ${marks.map((m) => `${formatNumber(m)}%`).join(', ') || 'chưa có'}.`}>
            <Input value={draft.marks} required onChange={(e) => setDraft({ ...draft, marks: e.target.value })} />
          </Field>
        </div>
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending} disabled={!draft.version.trim() || marks.length === 0}>Lưu phiên bản</Button>
        </div>
      </form>
    </Modal>
  );
}
