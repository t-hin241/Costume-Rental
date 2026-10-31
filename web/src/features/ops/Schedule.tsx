import { useQuery } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Icon } from '../../components/Icon';
import { QueryState } from '../../components/feedback';
import { Badge, Button, EmptyState, PageHeader, Skeleton, StatusBadge } from '../../components/ui';
import { opsApi } from '../../lib/api/endpoints';
import type { ScheduleEvent } from '../../lib/api/types';
import { addBusinessDays, businessToday, formatBusinessDate, formatTime } from '../../lib/format';
import { bookingStatus, legMode } from '../../lib/labels';

const windowDays = 7;

function businessDateOf(iso: string): string {
  return businessToday(new Date(iso));
}

export default function Schedule() {
  const [from, setFrom] = useState(businessToday);
  const to = addBusinessDays(from, windowDays - 1);
  const schedule = useQuery({ queryKey: ['ops', 'schedule', from, to], queryFn: () => opsApi.schedule(from, to) });

  return (
    <>
      <PageHeader title="Lịch giao nhận" subtitle={`${windowDays} ngày từ ${formatBusinessDate(from)}`} actions={(
        <>
          <Button variant="outline" size="sm" icon="chevronLeft" onClick={() => setFrom(addBusinessDays(from, -windowDays))}>Tuần trước</Button>
          <Button variant="outline" size="sm" onClick={() => setFrom(businessToday())}>Hôm nay</Button>
          <Button variant="outline" size="sm" icon="chevronRight" onClick={() => setFrom(addBusinessDays(from, windowDays))}>Tuần sau</Button>
        </>
      )} />
      <QueryState query={schedule} skeleton={<Skeleton height={380} />}>
        {(events) => <Agenda events={events} />}
      </QueryState>
    </>
  );
}

function Agenda({ events }: { events: ScheduleEvent[] }) {
  const days = useMemo(() => {
    const byDay = new Map<string, ScheduleEvent[]>();
    for (const e of [...events].sort((a, b) => a.at.localeCompare(b.at))) {
      const key = businessDateOf(e.at);
      byDay.set(key, [...(byDay.get(key) ?? []), e]);
    }
    return [...byDay.entries()];
  }, [events]);

  if (days.length === 0) return <EmptyState icon="calendar" title="Không có lịch giao nhận trong tuần này" />;

  return (
    <>
      {days.map(([day, items]) => (
        <section key={day} className="agenda-day">
          <h2>{formatBusinessDate(day)}</h2>
          <div className="card">
            {items.map((e) => (
              <Link key={e.id} to={`/ops/bookings/${e.booking.id}`} className="agenda-item">
                <strong className="tnum">{formatTime(e.at)}</strong>
                <Badge tone={e.kind === 'HANDOVER' ? 'brand' : 'info'} label={e.kind === 'HANDOVER' ? 'Giao' : 'Nhận trả'} />
                <span>
                  <strong>{e.booking.customer.name}</strong> <span className="mono small">{e.booking.code}</span>
                  <span className="cell-sub"><Icon name={e.mode === 'STORE' ? 'store' : 'truck'} size={13} /> {legMode[e.kind === 'HANDOVER' ? 'outbound' : 'return'][e.mode]} · {e.booking.headline}</span>
                </span>
                <span className="row" style={{ gap: 4 }}>
                  <StatusBadge status={bookingStatus[e.booking.status]} />
                  {e.booking.is_overdue && <StatusBadge status={{ label: 'Quá hạn', tone: 'danger' }} />}
                </span>
              </Link>
            ))}
          </div>
        </section>
      ))}
    </>
  );
}
