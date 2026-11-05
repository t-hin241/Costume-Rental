import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { QueryState } from '../../components/feedback';
import { Card, EmptyState, PageHeader, Pagination, Skeleton, Tabs } from '../../components/ui';
import { deliveryApi } from '../../lib/api/endpoints';
import { DeliveryTaskCard } from '../bookings/delivery';

/** Every shipment by window: those still needing work first (decision D43). */
export default function Deliveries() {
  const [scope, setScope] = useState<'open' | 'all'>('open');
  const [page, setPage] = useState(1);
  const board = useQuery({
    queryKey: ['deliveries', 'board', scope, page],
    queryFn: () => deliveryApi.board({ open: scope === 'open', page, page_size: 20 }),
  });
  return (
    <>
      <PageHeader title="Giao nhận" subtitle="Tạo vận đơn trên ứng dụng của hãng, rồi ghi lại mã và trạng thái ở đây." />
      <Tabs label="Phạm vi" value={scope} onChange={(v) => { setScope(v); setPage(1); }}
        options={[{ value: 'open', label: 'Cần xử lý' }, { value: 'all', label: 'Tất cả' }]} />
      <QueryState query={board} skeleton={<Skeleton height={260} />}>
        {(data) => data.items.length === 0 ? <EmptyState icon="truck" title="Không có vận đơn nào cần xử lý" /> : (
          <div className="stack">
            {data.items.map((t) => <Card key={t.id}><DeliveryTaskCard task={t} showBooking /></Card>)}
            <Pagination page={data} onChange={setPage} />
          </div>
        )}
      </QueryState>
    </>
  );
}
