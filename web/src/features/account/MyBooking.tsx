import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { Modal, QueryState, ReasonDialog, useAction } from '../../components/feedback';
import { Button, Card, DefinitionList, FormError, Notice, PageHeader, Skeleton, StatusBadge, btn } from '../../components/ui';
import { bookingApi, storeApi } from '../../lib/api/endpoints';
import type { BookingDetail } from '../../lib/api/types';
import { formatMoney } from '../../lib/format';
import { bookingStatus, paymentState } from '../../lib/labels';
import { BookingLines, BookingStepper, FinanceSummary, HoldCountdown, PaymentHistory, TransitionList, scheduleFacts } from '../bookings/parts';
import { PayOnline } from '../payment/PayOnline';
import { StoreCalendarPicker } from '../shop/shared';

export default function MyBooking() {
  const { id = '' } = useParams();
  const booking = useQuery({ queryKey: ['bookings', 'detail', id], queryFn: () => bookingApi.get(id) });
  return (
    <QueryState query={booking} skeleton={<Skeleton height={420} />}>
      {(b) => <BookingView booking={b} />}
    </QueryState>
  );
}

function BookingView({ booking }: { booking: BookingDetail }) {
  const [dialog, setDialog] = useState<'cancel' | 'reschedule' | null>(null);
  const store = useQuery({ queryKey: ['store-info'], queryFn: storeApi.info, staleTime: 5 * 60_000, retry: false });
  const pendingHold = booking.status === 'PENDING_CONFIRMATION' && booking.hold_expires_at;
  const owes = booking.finance.outstanding.amount > 0;

  return (
    <>
      <PageHeader
        back={{ to: '/account/bookings', label: 'Đơn thuê của tôi' }}
        title={booking.headline}
        subtitle={<><span className="mono">{booking.code}</span> · {booking.item_count} món</>}
        actions={<div className="row"><StatusBadge status={bookingStatus[booking.status]} /><StatusBadge status={paymentState[booking.payment_state]} /></div>}
      />
      <div className="stack">
        <Card><BookingStepper status={booking.status} /></Card>
        {pendingHold && <HoldCountdown expiresAt={pendingHold} />}
        {booking.is_overdue && <Notice tone="danger" title="Đơn đã quá hạn trả">Vui lòng trả hàng sớm nhất có thể hoặc liên hệ cửa hàng.</Notice>}
        {owes && store.data && !store.data.online_payment_available && booking.status !== 'CANCELLED' && booking.status !== 'EXPIRED' && (
          <Notice tone="brand" title={`Còn ${formatMoney(booking.finance.outstanding)} cần thanh toán`}>{store.data.payment_instructions}</Notice>
        )}
        {store.data?.online_payment_available && booking.payment_options.length > 0 && <PayOnline booking={booking} channels={store.data.payment_channels} />}
        <div className="split">
          <div className="stack">
            <Card title="Trang phục"><BookingLines booking={booking} /></Card>
            <Card title="Lịch sử đơn"><TransitionList transitions={booking.transitions} /></Card>
          </div>
          <div className="stack">
            <Card title="Lịch thuê"><DefinitionList items={scheduleFacts(booking)} /></Card>
            <Card title="Chi phí"><FinanceSummary finance={booking.finance} /></Card>
            <Card title="Giao dịch"><PaymentHistory finance={booking.finance} /></Card>
            {(booking.actions.includes('reschedule') || booking.actions.includes('cancel')) && (
              <div className="action-bar">
                {booking.actions.includes('reschedule') && <Button variant="outline" onClick={() => setDialog('reschedule')}>Đổi lịch</Button>}
                {booking.actions.includes('cancel') && <Button variant="ghost" onClick={() => setDialog('cancel')}>Hủy đơn</Button>}
              </div>
            )}
            <Link className={btn('ghost', 'sm')} to={`/account/support?booking=${booking.code}`}>Cần hỗ trợ với đơn này?</Link>
          </div>
        </div>
      </div>
      {dialog === 'cancel' && <CancelDialog booking={booking} onClose={() => setDialog(null)} />}
      {dialog === 'reschedule' && <RescheduleDialog booking={booking} onClose={() => setDialog(null)} />}
    </>
  );
}

function CancelDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const preview = useQuery({ queryKey: ['cancellation-preview', booking.id], queryFn: () => bookingApi.cancellationPreview(booking.id), staleTime: 0 });
  const cancel = useAction((note: string) => bookingApi.cancel(booking.id, 'CUSTOMER_REQUEST', note), {
    inline: true, success: 'Đã hủy đơn thuê.', invalidate: [['bookings']], onSuccess: onClose,
  });
  return (
    <ReasonDialog open title="Hủy đơn thuê" confirmLabel="Xác nhận hủy" danger onClose={onClose} pending={cancel.isPending} error={cancel.error}
      onConfirm={(note) => cancel.mutate(note)}
      description={(
        <QueryState query={preview} skeleton={<Skeleton height={90} />}>
          {(p) => (
            <DefinitionList items={[
              { label: 'Phí hủy', value: `${formatMoney(p.fee)} (${p.fee_percent}%)` },
              { label: 'Số tiền được hoàn', value: formatMoney(p.refund) },
              ...(p.owed.amount > 0 ? [{ label: 'Còn phải trả', value: formatMoney(p.owed) }] : []),
            ]} />
          )}
        </QueryState>
      )} />
  );
}

function RescheduleDialog({ booking, onClose }: { booking: BookingDetail; onClose: () => void }) {
  const [date, setDate] = useState<string>();
  const [slot, setSlot] = useState<string>();
  const calendar = useQuery({
    queryKey: ['store-calendar', 'day', date],
    queryFn: () => storeApi.calendar(date as string, date as string),
    enabled: Boolean(date),
  });
  const slots = calendar.data?.days[0]?.slots ?? [];
  const change = useAction(() => bookingApi.reschedule(booking.id, date as string, slot as string), {
    inline: true, success: 'Đã đổi lịch thuê.', invalidate: [['bookings']], onSuccess: onClose,
  });
  return (
    <Modal open title="Đổi lịch thuê" onClose={onClose}>
      <div className="stack">
        <p className="small muted">Giữ nguyên gói thuê. Nếu lịch mới không còn hàng, đơn giữ nguyên lịch cũ.</p>
        <StoreCalendarPicker value={date} onChange={(d) => { setDate(d); setSlot(undefined); }} />
        {date && (
          <div className="chip-group" role="radiogroup" aria-label="Khung giờ nhận">
            {slots.map((s) => <button key={s.id} type="button" role="radio" className="chip" aria-checked={slot === s.id} onClick={() => setSlot(s.id)}>{s.label}</button>)}
          </div>
        )}
        <FormError error={change.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button disabled={!date || !slot} loading={change.isPending} onClick={() => change.mutate(undefined)}>Đổi lịch</Button>
        </div>
      </div>
    </Modal>
  );
}
