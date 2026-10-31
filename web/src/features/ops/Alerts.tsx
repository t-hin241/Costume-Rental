import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { Card, EmptyState, PageHeader, Skeleton, StatusBadge } from '../../components/ui';
import { opsApi } from '../../lib/api/endpoints';
import type { BookingSummary } from '../../lib/api/types';
import { formatDateTime, formatHours } from '../../lib/format';

function BookingRef({ booking }: { booking: BookingSummary }) {
  return <Link className="cell-title mono" to={`/ops/bookings/${booking.id}`}>{booking.code}</Link>;
}

export default function Alerts() {
  const alerts = useQuery({ queryKey: ['ops', 'alerts'], queryFn: opsApi.alerts, refetchInterval: 60_000 });
  return (
    <>
      <PageHeader title="Cảnh báo" subtitle="Đơn trả trễ và các đơn sau bị ảnh hưởng vì hàng chưa về." />
      <QueryState query={alerts} skeleton={<Skeleton height={380} />}>
        {(a) => {
          const none = a.overdue.length + a.conflicts.length === 0;
          if (none) return <EmptyState icon="check" title="Không có cảnh báo nào" />;
          return (
            <div className="stack">
              <Card title={`Trả trễ (${a.overdue.length})`}>
                {a.overdue.length === 0 && <p className="small muted">Không có đơn trả trễ.</p>}
                {a.overdue.map((o) => (
                  <div key={o.booking.id} className="item-row">
                    <div>
                      <BookingRef booking={o.booking} /> · {o.booking.customer.name}
                      <span className="cell-sub">{o.booking.customer.phone} · Hạn trả {formatDateTime(o.booking.due_at)} · Còn {o.items_out} món</span>
                    </div>
                    <StatusBadge status={{ label: `Trễ ${formatHours(o.overdue_hours)}`, tone: 'danger' }} />
                  </div>
                ))}
              </Card>
              <Card title={`Đơn bị ảnh hưởng do trả trễ (${a.conflicts.length})`}>
                {a.conflicts.length === 0 && <p className="small muted">Không có xung đột lịch.</p>}
                {a.conflicts.map((c) => (
                  <div key={`${c.overdue_booking.id}-${c.asset_code}`} className="item-row">
                    <div>
                      <strong>{c.item_name}</strong> <span className="mono small">{c.asset_code}</span>
                      <span className="cell-sub">Đang ở đơn <BookingRef booking={c.overdue_booking} /></span>
                      <span className="cell-sub">
                        Cần cho: {c.endangered.map((b, i) => <span key={b.id}>{i > 0 && ', '}<BookingRef booking={b} /> (nhận {formatDateTime(b.pickup_at)})</span>)}
                      </span>
                    </div>
                    <StatusBadge status={{ label: 'Cần xử lý', tone: 'warning' }} />
                  </div>
                ))}
              </Card>
            </div>
          );
        }}
      </QueryState>
    </>
  );
}
