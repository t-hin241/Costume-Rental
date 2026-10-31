import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { Card, EmptyState, PageHeader, Skeleton } from '../../components/ui';
import { opsApi } from '../../lib/api/endpoints';
import type { OpsCounters } from '../../lib/api/types';
import { formatBusinessDate } from '../../lib/format';
import { BookingTable } from '../bookings/parts';

const tiles: { key: keyof OpsCounters; label: string; to: string; style?: 'alert' | 'warn' }[] = [
  { key: 'pending_confirmation', label: 'Chờ xác nhận', to: '/ops/bookings?status=PENDING_CONFIRMATION' },
  { key: 'to_prepare', label: 'Cần chuẩn bị', to: '/ops/bookings?status=CONFIRMED' },
  { key: 'handovers_today', label: 'Giao hôm nay', to: '/ops/schedule' },
  { key: 'returns_today', label: 'Nhận trả hôm nay', to: '/ops/schedule' },
  { key: 'inspections_open', label: 'Cần kiểm tra', to: '/ops/bookings?status=RETURN_INSPECTION' },
  { key: 'overdue', label: 'Trả trễ', to: '/ops/alerts', style: 'alert' },
  { key: 'conflicts', label: 'Đơn bị ảnh hưởng', to: '/ops/alerts', style: 'warn' },
  { key: 'assets_blocked', label: 'Tài sản chưa sẵn sàng', to: '/ops/assets' },
];

export default function Today() {
  const overview = useQuery({ queryKey: ['ops', 'overview'], queryFn: opsApi.overview, refetchInterval: 60_000 });
  return (
    <QueryState query={overview} skeleton={<Skeleton height={420} />}>
      {(o) => (
        <>
          <PageHeader title="Hôm nay" subtitle={formatBusinessDate(o.date)} />
          <div className="stack-lg">
            <div className="stat-grid">
              {tiles.map((t) => {
                const value = o.counters[t.key];
                const urgent = t.style && value > 0 ? ` stat-${t.style}` : '';
                return (
                  <Link key={t.key} to={t.to} className={`stat${urgent}`}>
                    <span className="stat-label">{t.label}</span>
                    <span className="stat-value">{value}</span>
                  </Link>
                );
              })}
            </div>
            <Card title="Việc cần xử lý" flush>
              {o.queue.length === 0
                ? <EmptyState icon="check" title="Không còn việc tồn đọng" />
                : <BookingTable rows={o.queue} linkBase="/ops/bookings" />}
            </Card>
          </div>
        </>
      )}
    </QueryState>
  );
}
