import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { EmptyState, PageHeader, Pagination, Skeleton, StatusBadge, Tabs, btn } from '../../components/ui';
import { bookingApi } from '../../lib/api/endpoints';
import { formatDateTime } from '../../lib/format';
import { bookingStatus, paymentState } from '../../lib/labels';
import { OfferingImage } from '../shop/shared';

type Filter = 'active' | 'history';

const pageSize = 8;

export default function MyBookings() {
  const [filter, setFilter] = useState<Filter>('active');
  const [page, setPage] = useState(1);
  const result = useQuery({
    queryKey: ['bookings', 'mine', filter, page],
    queryFn: () => bookingApi.mine({ scope: filter, page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });

  const change = (f: Filter) => {
    setFilter(f);
    setPage(1);
  };

  return (
    <>
      <PageHeader title="Đơn thuê của tôi" />
      <div className="stack">
        <Tabs label="Lọc đơn thuê" value={filter} onChange={change} options={[
          { value: 'active', label: 'Đang diễn ra' },
          { value: 'history', label: 'Lịch sử thuê' },
        ]} />
        <QueryState query={result} skeleton={<Skeleton height={260} />}>
          {(data) => data.items.length === 0
            ? (
              <EmptyState icon="list" title={filter === 'active' ? 'Bạn chưa có đơn nào đang diễn ra' : 'Chưa có lịch sử thuê'}>
                <Link className={btn('primary')} to="/catalog">Xem trang phục</Link>
              </EmptyState>
            )
            : (
              <>
                <div className="card">
                  {data.items.map((b) => (
                    <Link key={b.id} to={`/account/bookings/${b.id}`} className="booking-card">
                      <div className="checkout-thumb" style={{ width: 'auto' }}><OfferingImage image={b.image} /></div>
                      <div>
                        <strong>{b.headline}</strong>
                        <span className="cell-sub"><span className="mono">{b.code}</span> · {b.item_count} món · {b.package_label}</span>
                        <span className="cell-sub">Nhận {formatDateTime(b.pickup_at)} · Trả trước {formatDateTime(b.due_at)}</span>
                      </div>
                      <div className="stack-sm" style={{ alignItems: 'flex-end' }}>
                        <StatusBadge status={bookingStatus[b.status]} />
                        <StatusBadge status={paymentState[b.payment_state]} />
                      </div>
                    </Link>
                  ))}
                </div>
                <Pagination page={data} onChange={setPage} />
              </>
            )}
        </QueryState>
      </div>
    </>
  );
}
