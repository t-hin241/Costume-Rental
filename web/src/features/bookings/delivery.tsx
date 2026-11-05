import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Card, Checkbox, DefinitionList, Field, FormError, Input, Notice, Select, Skeleton, StatusBadge, Textarea } from '../../components/ui';
import { deliveryApi, storeApi, type CloseShipmentInput, type DeliveryEventInput } from '../../lib/api/endpoints';
import type { DeliveryFault, DeliveryStatus, DeliveryTask, FollowUp } from '../../lib/api/types';
import { formatDateTime, formatMoney, formatTime } from '../../lib/format';
import { deliveryDirection, deliveryFault, deliveryStatus, followUp } from '../../lib/labels';
import { StoreCalendarPicker } from '../shop/shared';

// Delivery runs in manual mode (decision D43): staff book each shipment in the
// carrier's app and record here what happens to it. The server says which
// actions apply; every action is checked again there.

function windowText(t: DeliveryTask): string {
  return `${formatDateTime(t.window_from)} – ${formatTime(t.window_until)}`;
}

function addressText(t: DeliveryTask): string {
  const a = t.address;
  return [a.recipient_name, a.phone, a.line, a.ward_name || a.ward_code].filter(Boolean).join(' · ');
}

/** What the customer sees: each leg with its window and progress. */
export function DeliveryTracking({ bookingId }: { bookingId: string }) {
  const tasks = useQuery({ queryKey: ['deliveries', bookingId], queryFn: () => deliveryApi.ofBooking(bookingId) });
  return (
    <Card title="Giao nhận tận nơi">
      <QueryState query={tasks} skeleton={<Skeleton height={80} />}>
        {({ items }) => items.length === 0 ? <p className="small muted">Chưa có vận đơn.</p> : (
          <div className="stack-sm">
            {items.map((t) => (
              <div key={t.id} className="item-row">
                <div>
                  <strong>{deliveryDirection[t.direction]}</strong>
                  <span className="cell-sub multiline">{windowText(t)}{t.shipment_code ? ` · Mã vận đơn ${t.shipment_code}` : ''}</span>
                  <span className="cell-sub multiline">{addressText(t)}</span>
                </div>
                <StatusBadge status={deliveryStatus[t.status]} />
              </div>
            ))}
          </div>
        )}
      </QueryState>
    </Card>
  );
}

type Dialog = 'shipment' | 'event' | 'classify' | 'close' | null;

/** One shipment with the staff actions the server allows now. */
export function DeliveryTaskCard({ task, showBooking }: { task: DeliveryTask; showBooking?: boolean }) {
  const [dialog, setDialog] = useState<Dialog>(null);
  const invalidate = [['deliveries'], ['bookings']];
  const simple = useAction((run: () => Promise<DeliveryTask>) => run(), { success: 'Đã cập nhật vận đơn.', invalidate });
  const unclassified = task.attempts.filter((a) => !a.classified);
  return (
    <div className="stack-sm" style={{ borderTop: '1px solid var(--border, #e5e5e5)', paddingTop: 12 }}>
      <div className="row between">
        <div>
          <strong>{deliveryDirection[task.direction]}</strong>
          {showBooking && <> · <Link className="link" to={`/ops/bookings/${task.booking_id}`}>Đơn {task.booking_code}</Link> · {task.customer_name}</>}
        </div>
        <StatusBadge status={deliveryStatus[task.status]} />
      </div>
      <DefinitionList items={[
        { label: 'Khung giờ', value: windowText(task) },
        { label: 'Địa chỉ', value: addressText(task) },
        { label: 'Mã vận đơn', value: task.shipment_code ? <span className="mono">{task.shipment_code}</span> : 'Chưa có' },
        { label: 'Phí khách trả', value: formatMoney(task.customer_fee) },
        ...(task.delivered_at ? [{ label: 'Hãng báo đã giao', value: formatDateTime(task.delivered_at) }] : []),
        ...(task.received_at ? [{ label: 'Về cửa hàng', value: formatDateTime(task.received_at) }] : []),
        ...(task.follow_up ? [{ label: 'Hướng xử lý', value: `${followUp[task.follow_up]}${task.closed_reason ? ` · ${task.closed_reason}` : ''}` }] : []),
      ]} />
      {task.attempts.length > 0 && (
        <ul className="plain small">
          {task.attempts.map((a) => (
            <li key={a.id}>{formatDateTime(a.at)} · {deliveryStatus[a.status].label} · {deliveryFault[a.fault]}{a.reason ? ` · ${a.reason}` : ''}</li>
          ))}
        </ul>
      )}
      {task.awaiting_follow_up && (
        <Notice tone="warning" title="Hãng đã ngừng giao/lấy">
          {unclassified.length > 0 ? 'Phân loại lỗi của lần thất bại cuối trước khi chọn hướng xử lý.' : 'Chọn hướng xử lý: vận đơn mới, tại cửa hàng hoặc kết thúc đơn.'}
        </Notice>
      )}
      <div className="action-bar">
        {task.actions.includes('RECORD_SHIPMENT') && <Button size="sm" onClick={() => setDialog('shipment')}>Nhập mã vận đơn</Button>}
        {task.actions.includes('MARK_READY') && <Button size="sm" variant="outline" loading={simple.isPending} onClick={() => simple.mutate(() => deliveryApi.markReady(task.id))}>Đã đóng gói</Button>}
        {task.actions.includes('RECORD_EVENT') && <Button size="sm" variant="outline" onClick={() => setDialog('event')}>Cập nhật trạng thái</Button>}
        {task.actions.includes('CLASSIFY_ATTEMPT') && <Button size="sm" variant="outline" onClick={() => setDialog('classify')}>Phân loại lỗi</Button>}
        {task.actions.includes('CLOSE') && <Button size="sm" onClick={() => setDialog('close')}>Chọn hướng xử lý</Button>}
        {task.actions.includes('RECEIVE') && <Button size="sm" loading={simple.isPending} onClick={() => simple.mutate(() => deliveryApi.receive(task.id))}>Đã nhận kiện về</Button>}
        {task.actions.includes('CONFIRM_CANCELLED') && <Button size="sm" variant="outline" loading={simple.isPending} onClick={() => simple.mutate(() => deliveryApi.confirmCancelled(task.id))}>Hãng đã hủy</Button>}
        {task.can_collect_at_store && <Button size="sm" loading={simple.isPending} onClick={() => simple.mutate(() => deliveryApi.collect(task.id))}>Khách đã nhận tại cửa hàng</Button>}
      </div>
      {dialog === 'shipment' && <ShipmentDialog task={task} invalidate={invalidate} onClose={() => setDialog(null)} />}
      {dialog === 'event' && <EventDialog task={task} invalidate={invalidate} onClose={() => setDialog(null)} />}
      {dialog === 'classify' && <ClassifyDialog task={task} invalidate={invalidate} onClose={() => setDialog(null)} />}
      {dialog === 'close' && <CloseDialog task={task} invalidate={invalidate} onClose={() => setDialog(null)} />}
    </div>
  );
}

interface DialogProps { task: DeliveryTask; invalidate: unknown[][]; onClose: () => void }

function ShipmentDialog({ task, invalidate, onClose }: DialogProps) {
  const [code, setCode] = useState('');
  const save = useAction(() => deliveryApi.recordShipment(task.id, code.trim()), { inline: true, success: 'Đã lưu mã vận đơn.', invalidate, onSuccess: onClose });
  const submit = (e: FormEvent) => { e.preventDefault(); save.mutate(undefined); };
  return (
    <Modal open title="Nhập mã vận đơn" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <p className="small muted">Tạo vận đơn trên ứng dụng của hãng ({task.provider}) với địa chỉ ở trên, rồi nhập mã hãng cấp.</p>
        <FormError error={save.error} />
        <Field label="Mã vận đơn" required><Input value={code} maxLength={64} onChange={(e) => setCode(e.target.value)} /></Field>
        <div className="action-bar"><Button type="submit" disabled={!code.trim()} loading={save.isPending}>Lưu</Button></div>
      </form>
    </Modal>
  );
}

const outboundEvents: DeliveryStatus[] = ['IN_TRANSIT', 'DELIVERED', 'DELIVERY_FAILED'];
const returnEvents: DeliveryStatus[] = ['PICKUP_CONFIRMED', 'PICKUP_FAILED', 'IN_TRANSIT'];
const failures: DeliveryStatus[] = ['DELIVERY_FAILED', 'PICKUP_FAILED', 'RETURN_FAILED'];
const faults: DeliveryFault[] = ['UNKNOWN', 'CUSTOMER', 'STORE', 'CARRIER'];

function EventDialog({ task, invalidate, onClose }: DialogProps) {
  const options = task.direction === 'OUTBOUND' ? outboundEvents : returnEvents;
  const [body, setBody] = useState<DeliveryEventInput>({ status: options[0] as DeliveryStatus, fault: 'UNKNOWN', gave_up: false, reason: '' });
  const failed = failures.includes(body.status);
  const save = useAction(() => deliveryApi.recordEvent(task.id, failed ? body : { status: body.status, reason: body.reason }),
    { inline: true, success: 'Đã cập nhật trạng thái.', invalidate, onSuccess: onClose });
  const submit = (e: FormEvent) => { e.preventDefault(); save.mutate(undefined); };
  return (
    <Modal open title="Cập nhật trạng thái vận đơn" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <p className="small muted">Ghi lại trạng thái hãng báo trên ứng dụng của hãng. Giờ ghi nhận là lúc lưu.</p>
        <FormError error={save.error} />
        <Field label="Trạng thái" required>
          <Select value={body.status} onChange={(e) => setBody({ ...body, status: e.target.value as DeliveryStatus })}>
            {options.map((s) => <option key={s} value={s}>{deliveryStatus[s].label}</option>)}
          </Select>
        </Field>
        {failed && (
          <>
            <Field label="Lỗi do ai" hint="Chưa rõ thì để “Chưa rõ” và phân loại sau.">
              <Select value={body.fault} onChange={(e) => setBody({ ...body, fault: e.target.value as DeliveryFault })}>
                {faults.map((f) => <option key={f} value={f}>{deliveryFault[f]}</option>)}
              </Select>
            </Field>
            <Checkbox label="Hãng đã ngừng thử (hết số lần, hoàn kiện về kho hoặc hủy chuyến lấy)" checked={Boolean(body.gave_up)}
              onChange={(e) => setBody({ ...body, gave_up: e.target.checked })} />
          </>
        )}
        <Field label="Ghi chú" required={failed}><Textarea value={body.reason} maxLength={500} onChange={(e) => setBody({ ...body, reason: e.target.value })} /></Field>
        <div className="action-bar"><Button type="submit" disabled={failed && !body.reason.trim()} loading={save.isPending}>Lưu</Button></div>
      </form>
    </Modal>
  );
}

function ClassifyDialog({ task, invalidate, onClose }: DialogProps) {
  const open = task.attempts.filter((a) => !a.classified);
  const [attempt, setAttempt] = useState(open[open.length - 1]?.id ?? '');
  const [fault, setFault] = useState<DeliveryFault>('CUSTOMER');
  const [reason, setReason] = useState('');
  const save = useAction(() => deliveryApi.classify(task.id, attempt, fault, reason.trim()), { inline: true, success: 'Đã phân loại.', invalidate, onSuccess: onClose });
  const submit = (e: FormEvent) => { e.preventDefault(); save.mutate(undefined); };
  return (
    <Modal open title="Phân loại lần thất bại" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <p className="small muted">Chỉ lỗi do khách mới được tính phí giao lại (tối đa một lần mỗi vận đơn) và mới ảnh hưởng thời gian thuê.</p>
        <FormError error={save.error} />
        <Field label="Lần thất bại" required>
          <Select value={attempt} onChange={(e) => setAttempt(e.target.value)}>
            {open.map((a) => <option key={a.id} value={a.id}>{formatDateTime(a.at)} · {a.reason || deliveryStatus[a.status].label}</option>)}
          </Select>
        </Field>
        <Field label="Lỗi do ai" required>
          <Select value={fault} onChange={(e) => setFault(e.target.value as DeliveryFault)}>
            {faults.filter((f) => f !== 'UNKNOWN').map((f) => <option key={f} value={f}>{deliveryFault[f]}</option>)}
          </Select>
        </Field>
        <Field label="Căn cứ" required><Textarea value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} /></Field>
        <div className="action-bar"><Button type="submit" disabled={!attempt || !reason.trim()} loading={save.isPending}>Lưu</Button></div>
      </form>
    </Modal>
  );
}

function CloseDialog({ task, invalidate, onClose }: DialogProps) {
  const choices: FollowUp[] = task.direction === 'OUTBOUND' ? ['NEW_SHIPMENT', 'AT_STORE', 'END_BOOKING'] : ['NEW_SHIPMENT', 'AT_STORE'];
  const [body, setBody] = useState<CloseShipmentInput>({ follow_up: 'NEW_SHIPMENT', reason: '' });
  const calendar = useQuery({
    queryKey: ['store-calendar', 'day', body.date, 'COURIER'],
    queryFn: () => storeApi.calendar(body.date as string, body.date as string, 'COURIER'),
    enabled: body.follow_up === 'NEW_SHIPMENT' && Boolean(body.date),
  });
  // The calendar offers rental starts; a new shipment may go out on any open day.
  const windows = calendar.data?.days[0]?.slots ?? [];
  const save = useAction(() => deliveryApi.close(task.id, body), { inline: true, success: 'Đã đóng vận đơn.', invalidate, onSuccess: onClose });
  const submit = (e: FormEvent) => { e.preventDefault(); save.mutate(undefined); };
  const ready = body.reason.trim() && (body.follow_up !== 'NEW_SHIPMENT' || (body.date && body.window_id));
  return (
    <Modal open title="Chọn hướng xử lý" onClose={onClose} wide>
      <form className="stack" onSubmit={submit}>
        {task.direction === 'OUTBOUND' && <p className="small muted">Đóng vận đơn chiều giao nghĩa là kiện đã về cửa hàng.</p>}
        <FormError error={save.error} />
        <Field label="Hướng xử lý" required>
          <Select value={body.follow_up} onChange={(e) => setBody({ follow_up: e.target.value as FollowUp, reason: body.reason })}>
            {choices.map((f) => <option key={f} value={f}>{followUp[f]}</option>)}
          </Select>
        </Field>
        {body.follow_up === 'END_BOOKING' && (
          <Notice tone="warning" title="Kết thúc đơn">
            Lỗi do khách: giữ tiền thuê theo mức không đến nhận và phí chiều giao, hoàn phần còn lại. Lỗi do cửa hàng hoặc hãng: hoàn toàn bộ. Hàng về kho vẫn phải qua kiểm tra.
          </Notice>
        )}
        {body.follow_up === 'NEW_SHIPMENT' && (
          <>
            <Field label="Ngày giao/lấy mới" required>
              <StoreCalendarPicker value={body.date} outbound="COURIER" onChange={(date) => setBody({ ...body, date, window_id: undefined })} />
            </Field>
            {body.date && (
              <Field label="Khung giờ" required>
                {calendar.isPending ? <Skeleton height={40} /> : (
                  <Select value={body.window_id ?? ''} onChange={(e) => setBody({ ...body, window_id: e.target.value || undefined })}>
                    <option value="">Chọn khung giờ</option>
                    {windows.map((w) => <option key={w.id} value={w.id}>{w.label}</option>)}
                  </Select>
                )}
              </Field>
            )}
          </>
        )}
        <Field label="Lý do" required hint="Lưu vào lịch sử thao tác."><Textarea value={body.reason} maxLength={500} onChange={(e) => setBody({ ...body, reason: e.target.value })} /></Field>
        <div className="action-bar"><Button type="submit" disabled={!ready} loading={save.isPending}>Xác nhận</Button></div>
      </form>
    </Modal>
  );
}

/** The shipments of one booking for staff, with their actions. */
export function DeliveryPanel({ bookingId }: { bookingId: string }) {
  const tasks = useQuery({ queryKey: ['deliveries', bookingId], queryFn: () => deliveryApi.ofBooking(bookingId) });
  return (
    <Card title="Giao nhận tận nơi">
      <QueryState query={tasks} skeleton={<Skeleton height={120} />}>
        {({ items }) => items.length === 0
          ? <p className="small muted">Đơn này không có vận đơn.</p>
          : <div className="stack">{items.map((t) => <DeliveryTaskCard key={t.id} task={t} />)}</div>}
      </QueryState>
    </Card>
  );
}
