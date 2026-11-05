import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { QueryState, ReasonDialog, useAction } from '../../components/feedback';
import { Badge, Button, EmptyState, Notice, PageHeader, Pagination, Skeleton } from '../../components/ui';
import { adminApi } from '../../lib/api/endpoints';
import type { ProfileReview } from '../../lib/api/types';
import { formatDateTime } from '../../lib/format';
import { retentionBlocker } from '../../lib/labels';

const pageSize = 20;

/** Decision D39: profiles of customer accounts unused for 24 months, for an admin to review. */
export default function DataRetention() {
  const [page, setPage] = useState(1);
  const [erasing, setErasing] = useState<ProfileReview | null>(null);
  const result = useQuery({
    queryKey: ['admin', 'profile-retention', { page }],
    queryFn: () => adminApi.profileRetention({ page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });
  const erase = useAction((reason: string) => adminApi.eraseProfile(erasing!.user_id, reason), {
    inline: true,
    success: 'Đã xóa họ tên và số điện thoại. Tài khoản, email và đơn thuê vẫn giữ nguyên.',
    invalidate: [['admin', 'profile-retention'], ['admin', 'users']],
    onSuccess: () => setErasing(null),
  });

  const columns: Column<ProfileReview>[] = [
    {
      key: 'customer', header: 'Khách',
      render: (r) => <span><span className="cell-title">{r.full_name}</span><span className="cell-sub">{r.email} · {r.phone}</span></span>,
    },
    { key: 'seen', header: 'Hoạt động cuối', render: (r) => formatDateTime(r.last_activity_at) },
    {
      key: 'blockers', header: 'Còn giữ vì',
      render: (r) => r.blockers.length === 0
        ? <span className="muted">Không còn gì</span>
        : <span className="row" style={{ gap: 4 }}>{r.blockers.map((b) => <Badge key={b} label={retentionBlocker[b]} />)}</span>,
    },
    {
      key: 'action', header: '',
      render: (r) => r.blockers.length === 0 && <Button variant="danger" size="sm" onClick={() => setErasing(r)}>Xóa hồ sơ</Button>,
    },
  ];

  return (
    <>
      <PageHeader title="Dữ liệu cá nhân" subtitle="Hồ sơ của khách không dùng tài khoản trong 24 tháng." />
      <Notice tone="info">
        Hệ thống không tự xóa hồ sơ. Xóa chỉ bỏ họ tên và số điện thoại; tài khoản, email, đơn thuê và sổ tiền vẫn giữ.
        Hội thoại hỗ trợ tự xóa sau 24 tháng kể từ khi đóng.
      </Notice>
      <div className="card">
        <QueryState query={result} skeleton={<Skeleton height={320} />}>
          {(data) => data.items.length === 0
            ? <EmptyState icon="users" title="Không có hồ sơ nào cần xem xét" />
            : <DataTable rows={data.items} columns={columns} rowKey={(r) => r.user_id} caption="Hồ sơ cần xem xét" />}
        </QueryState>
      </div>
      {result.data && <Pagination page={result.data} onChange={setPage} />}
      {erasing && (
        <ReasonDialog open title={`Xóa hồ sơ của ${erasing.full_name}`} confirmLabel="Xóa hồ sơ" danger onClose={() => setErasing(null)}
          pending={erase.isPending} error={erase.error} onConfirm={(reason) => erase.mutate(reason)} />
      )}
    </>
  );
}
