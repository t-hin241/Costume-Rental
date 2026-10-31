import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { QueryState } from '../../components/feedback';
import { EmptyState, Field, Input, PageHeader, Pagination, Select, Skeleton, StatusBadge } from '../../components/ui';
import { adminApi } from '../../lib/api/endpoints';
import type { AuditEvent } from '../../lib/api/types';
import { formatDateTime } from '../../lib/format';
import { useDebounced } from '../../lib/hooks';
import { auditResult } from '../../lib/labels';

const pageSize = 25;

const columns: Column<AuditEvent>[] = [
  { key: 'at', header: 'Thời điểm', render: (e) => <span className="nowrap">{formatDateTime(e.occurred_at)}</span> },
  { key: 'actor', header: 'Người thực hiện', render: (e) => e.actor },
  { key: 'action', header: 'Hành động', render: (e) => <span className="mono">{e.action}</span> },
  { key: 'resource', header: 'Đối tượng', render: (e) => <span>{e.resource_type}<span className="cell-sub mono">{e.resource_id}</span></span> },
  { key: 'reason', header: 'Lý do', render: (e) => e.reason },
  { key: 'result', header: 'Kết quả', render: (e) => <StatusBadge status={auditResult[e.result]} /> },
];

export default function Audit() {
  const [q, setQ] = useState('');
  const [result, setResult] = useState('');
  const [page, setPage] = useState(1);
  const term = useDebounced(q);
  const events = useQuery({
    queryKey: ['admin', 'audit', { term, result, page }],
    queryFn: () => adminApi.audit({ q: term, result, page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });

  return (
    <>
      <PageHeader title="Lịch sử thao tác" subtitle="Bản ghi chỉ thêm, không sửa và không xóa." />
      <div className="toolbar">
        <Field label="Tìm kiếm">
          <Input type="search" placeholder="Người thực hiện, hành động, đối tượng" value={q} onChange={(e) => { setQ(e.target.value); setPage(1); }} />
        </Field>
        <Field label="Kết quả">
          <Select value={result} onChange={(e) => { setResult(e.target.value); setPage(1); }}>
            <option value="">Tất cả</option>
            {Object.entries(auditResult).map(([value, s]) => <option key={value} value={value}>{s.label}</option>)}
          </Select>
        </Field>
      </div>
      <div className="card">
        <QueryState query={events} skeleton={<Skeleton height={320} />}>
          {(data) => data.items.length === 0
            ? <EmptyState icon="list" title="Không có bản ghi phù hợp" />
            : <DataTable rows={data.items} columns={columns} rowKey={(e) => e.id} caption="Lịch sử thao tác" />}
        </QueryState>
      </div>
      {events.data && <Pagination page={events.data} onChange={setPage} />}
    </>
  );
}
