import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Card, EmptyState, Field, FormError, Input, PageHeader, Select, Skeleton, StatusBadge, Textarea } from '../../components/ui';
import { accountApi } from '../../lib/api/endpoints';
import type { SupportRequest, SupportTopic } from '../../lib/api/types';
import { formatDateTime } from '../../lib/format';
import { supportStatus, supportTopic } from '../../lib/labels';

export function SupportThread({ request }: { request: SupportRequest }) {
  return (
    <ol className="timeline">
      {request.messages.map((m, i) => (
        <li key={`${m.at}-${i}`}>
          <strong>{m.author}</strong>
          <span className="cell-sub">{formatDateTime(m.at)}</span>
          <p className="small" style={{ marginTop: 2, whiteSpace: 'pre-wrap' }}>{m.body}</p>
        </li>
      ))}
    </ol>
  );
}

export default function Support() {
  const [params] = useSearchParams();
  const bookingCode = params.get('booking') ?? '';
  const [creating, setCreating] = useState(Boolean(bookingCode));
  const [openId, setOpenId] = useState<string | null>(null);
  const requests = useQuery({ queryKey: ['support', 'mine'], queryFn: accountApi.supportRequests });

  return (
    <>
      <PageHeader title="Yêu cầu hỗ trợ" subtitle="Gửi câu hỏi về đơn thuê, thanh toán hoặc sản phẩm."
        actions={<Button icon="plus" onClick={() => setCreating(true)}>Tạo yêu cầu</Button>} />
      <QueryState query={requests} skeleton={<Skeleton height={200} />}>
        {(items) => items.length === 0
          ? <EmptyState icon="help" title="Bạn chưa có yêu cầu hỗ trợ nào" />
          : (
            <div className="stack">
              {items.map((r) => (
                <Card key={r.id} title={r.subject} actions={<StatusBadge status={supportStatus[r.status]} />}>
                  <div className="stack-sm">
                    <p className="small muted"><span className="mono">{r.code}</span> · {supportTopic[r.topic]}{r.booking_code ? ` · Đơn ${r.booking_code}` : ''}</p>
                    <div><Button variant="ghost" size="sm" aria-expanded={openId === r.id} onClick={() => setOpenId(openId === r.id ? null : r.id)}>
                      {openId === r.id ? 'Thu gọn' : `Xem trao đổi (${r.messages.length})`}
                    </Button></div>
                    {openId === r.id && <SupportThread request={r} />}
                  </div>
                </Card>
              ))}
            </div>
          )}
      </QueryState>
      {creating && <NewRequest bookingCode={bookingCode} onClose={() => setCreating(false)} />}
    </>
  );
}

function NewRequest({ bookingCode, onClose }: { bookingCode: string; onClose: () => void }) {
  const [topic, setTopic] = useState<SupportTopic>(bookingCode ? 'BOOKING' : 'OTHER');
  const [subject, setSubject] = useState('');
  const [code, setCode] = useState(bookingCode);
  const [message, setMessage] = useState('');
  const create = useAction(() => accountApi.createSupportRequest({ topic, subject: subject.trim(), booking_code: code.trim(), message: message.trim() }), {
    inline: true, success: 'Đã gửi yêu cầu hỗ trợ.', invalidate: [['support']], onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate(undefined);
  };
  return (
    <Modal open title="Tạo yêu cầu hỗ trợ" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <FormError error={create.error} />
        <div className="form-grid">
          <Field label="Chủ đề" required>
            <Select value={topic} onChange={(e) => setTopic(e.target.value as SupportTopic)}>
              {Object.entries(supportTopic).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
            </Select>
          </Field>
          <Field label="Mã đơn (nếu có)"><Input value={code} maxLength={32} onChange={(e) => setCode(e.target.value)} /></Field>
        </div>
        <Field label="Tiêu đề" required><Input value={subject} required maxLength={160} onChange={(e) => setSubject(e.target.value)} /></Field>
        <Field label="Nội dung" required><Textarea rows={5} value={message} required maxLength={2000} onChange={(e) => setMessage(e.target.value)} /></Field>
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={create.isPending}>Gửi yêu cầu</Button>
        </div>
      </form>
    </Modal>
  );
}
