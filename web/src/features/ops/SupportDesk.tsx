import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { QueryState, useAction } from '../../components/feedback';
import { Button, Card, Checkbox, EmptyState, Field, PageHeader, Skeleton, StatusBadge, Textarea } from '../../components/ui';
import { supportApi } from '../../lib/api/endpoints';
import type { SupportRequest } from '../../lib/api/types';
import { supportStatus, supportTopic } from '../../lib/labels';
import { SupportThread } from '../account/Support';

export default function SupportDesk() {
  const requests = useQuery({ queryKey: ['support', 'all'], queryFn: supportApi.list });
  return (
    <>
      <PageHeader title="Hỗ trợ khách" subtitle="Yêu cầu khách gửi từ trang tài khoản." />
      <QueryState query={requests} skeleton={<Skeleton height={260} />}>
        {(items) => items.length === 0
          ? <EmptyState icon="help" title="Không có yêu cầu hỗ trợ nào" />
          : <div className="stack">{items.map((r) => <RequestCard key={r.id} request={r} />)}</div>}
      </QueryState>
    </>
  );
}

function RequestCard({ request }: { request: SupportRequest }) {
  const [message, setMessage] = useState('');
  const [resolve, setResolve] = useState(false);
  const reply = useAction(() => supportApi.reply(request.id, { message: message.trim(), resolve }), {
    success: 'Đã gửi phản hồi.',
    invalidate: [['support']],
    onSuccess: () => { setMessage(''); setResolve(false); },
  });
  return (
    <Card title={request.subject} actions={<StatusBadge status={supportStatus[request.status]} />}>
      <div className="stack">
        <p className="small muted">
          <span className="mono">{request.code}</span> · {request.customer_name} · {supportTopic[request.topic]}{request.booking_code ? ` · Đơn ${request.booking_code}` : ''}
        </p>
        <SupportThread request={request} />
        {request.status !== 'RESOLVED' && (
          <>
            <Field label="Phản hồi"><Textarea value={message} maxLength={2000} onChange={(e) => setMessage(e.target.value)} /></Field>
            <div className="row between">
              <Checkbox label="Đánh dấu đã giải quyết" checked={resolve} onChange={(e) => setResolve(e.target.checked)} />
              <Button size="sm" disabled={!message.trim()} loading={reply.isPending} onClick={() => reply.mutate(undefined)}>Gửi phản hồi</Button>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}
