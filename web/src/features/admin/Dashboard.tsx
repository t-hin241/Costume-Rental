import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { BarList, ColumnChart } from '../../components/Charts';
import { DataTable, type Column } from '../../components/DataTable';
import { QueryState } from '../../components/feedback';
import { Card, PageHeader, Select, Skeleton } from '../../components/ui';
import { adminApi } from '../../lib/api/endpoints';
import type { Dashboard as Data, DashboardKpi } from '../../lib/api/types';
import { formatBusinessDate, formatCompactVND, formatMoney, formatNumber } from '../../lib/format';
import { assetStatus, bookingStatus } from '../../lib/labels';

const kpiMeta: Record<DashboardKpi['key'], { label: string; format: (v: number) => string; brand?: boolean }> = {
  revenue: { label: 'Doanh thu ghi nhận', format: (v) => formatMoney({ amount: v, currency: 'VND' }), brand: true },
  bookings: { label: 'Đơn mới', format: formatNumber },
  customers: { label: 'Khách có đơn', format: formatNumber },
  active_rentals: { label: 'Đơn đang thuê', format: formatNumber },
  utilization: { label: 'Tỷ lệ tài sản đang thuê', format: (v) => `${formatNumber(v)}%` },
  overdue: { label: 'Đơn trả trễ', format: formatNumber },
};

function change(k: DashboardKpi): string | null {
  if (k.change_percent === null) return null;
  const sign = k.change_percent > 0 ? '+' : '';
  return `${sign}${formatNumber(k.change_percent)}% so với kỳ trước`;
}

const ranges = [7, 30, 90];

export default function Dashboard() {
  const [days, setDays] = useState(30);
  const dashboard = useQuery({ queryKey: ['admin', 'dashboard', days], queryFn: () => adminApi.dashboard(days) });
  return (
    <>
      <PageHeader title="Tổng quan" subtitle="Booking, doanh thu, khách hàng, tồn kho và vận hành." actions={(
        <Select aria-label="Khoảng thời gian" value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {ranges.map((r) => <option key={r} value={r}>{r} ngày gần nhất</option>)}
        </Select>
      )} />
      <QueryState query={dashboard} skeleton={<Skeleton height={460} />}>
        {(d) => <DashboardView data={d} />}
      </QueryState>
    </>
  );
}

function DashboardView({ data }: { data: Data }) {
  const topColumns: Column<Data['top_offerings'][number]>[] = [
    { key: 'name', header: 'Trang phục', render: (r) => <strong>{r.name}</strong> },
    { key: 'bookings', header: 'Số đơn', align: 'right', render: (r) => <span className="tnum">{r.bookings}</span> },
    { key: 'revenue', header: 'Doanh thu', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.revenue)}</span> },
  ];

  return (
    <div className="stack-lg">
      <div className="stat-grid stat-grid-3">
        {data.kpis.map((k) => {
          const meta = kpiMeta[k.key];
          const foot = change(k);
          return (
            <div key={k.key} className={`stat${meta.brand ? ' stat-brand' : ''}`}>
              <span className="stat-label">{meta.label}</span>
              <span className="stat-value">{meta.format(k.value)}</span>
              {foot && <span className="stat-foot">{foot}</span>}
            </div>
          );
        })}
      </div>
      <Card title={`Doanh thu theo ngày, ${data.range_days} ngày gần nhất`}>
        <ColumnChart
          ariaLabel="Biểu đồ cột doanh thu theo ngày"
          formatTick={formatCompactVND}
          data={data.revenue_series.map((p) => ({
            key: p.date,
            label: `${p.date.slice(8)}/${p.date.slice(5, 7)}`,
            value: p.revenue.amount,
            tooltip: <>{formatBusinessDate(p.date)}: <strong>{formatMoney(p.revenue)}</strong></>,
          }))}
        />
        <details className="details-table">
          <summary>Xem bảng số liệu</summary>
          <DataTable rows={data.revenue_series} rowKey={(r) => r.date} caption="Doanh thu theo ngày" columns={[
            { key: 'date', header: 'Ngày', render: (r) => formatBusinessDate(r.date) },
            { key: 'revenue', header: 'Doanh thu', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.revenue)}</span> },
          ]} />
        </details>
        <p className="small muted" style={{ marginTop: 8 }}>{data.revenue_recognized_note}</p>
      </Card>
      <div className="grid-2">
        <Card title="Đơn thuê theo trạng thái">
          <BarList rows={data.bookings_by_status.map((r) => ({ key: r.status, label: bookingStatus[r.status].label, value: r.count }))} />
        </Card>
        <Card title="Tài sản theo tình trạng">
          <BarList rows={data.inventory_by_status.map((r) => ({ key: r.status, label: assetStatus[r.status].label, value: r.count }))} />
        </Card>
      </div>
      <Card title="Trang phục được thuê nhiều" flush>
        <DataTable rows={data.top_offerings} columns={topColumns} rowKey={(r) => r.name} caption="Trang phục được thuê nhiều" />
      </Card>
    </div>
  );
}
