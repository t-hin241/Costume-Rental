import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { EmptyState, Field, Input, PageHeader, Pagination, Select, Skeleton } from '../../components/ui';
import { bookingApi } from '../../lib/api/endpoints';
import type { BookingStatus } from '../../lib/api/types';
import { useDebounced } from '../../lib/hooks';
import { bookingStatus } from '../../lib/labels';
import { BookingTable } from '../bookings/parts';

const pageSize = 20;

export default function Bookings() {
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const [draft, setDraft] = useState(q);
  const term = useDebounced(draft);
  const status = (params.get('status') ?? '') as BookingStatus | '';
  const page = Number(params.get('page')) || 1;

  const update = (key: string, value: string) => {
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      if (value) next.set(key, value); else next.delete(key);
      if (key !== 'page') next.delete('page');
      return next;
    }, { replace: true });
  };

  useEffect(() => {
    if (term !== q) update('q', term);
    // Only the debounced text writes to the URL.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [term]);

  const result = useQuery({
    queryKey: ['bookings', 'all', { q, status, page }],
    queryFn: () => bookingApi.list({ q, status, page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });

  return (
    <>
      <PageHeader title="Đơn thuê" subtitle={result.data ? `${result.data.total} đơn` : undefined} />
      <div className="toolbar">
        <Field label="Tìm kiếm">
          <Input type="search" placeholder="Mã đơn, tên khách, SĐT" value={draft} onChange={(e) => setDraft(e.target.value)} />
        </Field>
        <Field label="Trạng thái">
          <Select value={status} onChange={(e) => update('status', e.target.value)}>
            <option value="">Tất cả</option>
            {Object.entries(bookingStatus).map(([value, s]) => <option key={value} value={value}>{s.label}</option>)}
          </Select>
        </Field>
      </div>
      <div className="card">
        <QueryState query={result} skeleton={<Skeleton height={320} />}>
          {(data) => data.items.length === 0
            ? <EmptyState icon="list" title="Không có đơn phù hợp" />
            : <BookingTable rows={data.items} linkBase="/ops/bookings" />}
        </QueryState>
      </div>
      {result.data && <Pagination page={result.data} onChange={(p) => update('page', String(p))} />}
    </>
  );
}
