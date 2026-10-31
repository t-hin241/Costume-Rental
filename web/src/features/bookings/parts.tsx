import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { StatusBadge } from '../../components/ui';
import { DataTable, type Column } from '../../components/DataTable';
import type { BookingDetail, BookingFinance, BookingStatus, BookingSummary, BookingTransition, FinanceRefund, Money } from '../../lib/api/types';
import { formatCountdown, formatDateTime, formatMoney } from '../../lib/format';
import { bookingFlow, bookingStatus, bucket, chargeKind, legMode, paymentMethod, paymentState, paymentStatus, refundStatus, transitionAction } from '../../lib/labels';
import { OfferingImage } from '../shop/shared';

export function BookingStepper({ status }: { status: BookingStatus }) {
  const current = bookingFlow.indexOf(status);
  if (current < 0) return null;
  return (
    <ol className="stepper" aria-label="Tiến trình đơn thuê">
      {bookingFlow.map((s, i) => (
        <li key={s} data-state={i < current ? 'done' : i === current ? 'current' : 'todo'} aria-current={i === current ? 'step' : undefined}>
          {bookingStatus[s].label}
        </li>
      ))}
    </ol>
  );
}

export function HoldCountdown({ expiresAt }: { expiresAt: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const left = new Date(expiresAt).getTime() - now;
  return (
    <div className="countdown" role="timer">
      <span>{left > 0 ? 'Hàng đang được giữ cho đơn này' : 'Đã hết thời gian giữ hàng'}</span>
      <strong>{formatCountdown(left)}</strong>
    </div>
  );
}

export function TransitionList({ transitions }: { transitions: BookingTransition[] }) {
  return (
    <ol className="timeline">
      {transitions.map((t, i) => (
        <li key={`${t.at}-${i}`}>
          <strong>{transitionAction[t.action] ?? bookingStatus[t.to].label}</strong>
          <span className="cell-sub">{formatDateTime(t.at)} · {t.actor}</span>
          {t.note && <span className="cell-sub">{t.note}</span>}
        </li>
      ))}
    </ol>
  );
}

function Line({ label, value, note }: { label: string; value: Money; note?: string }) {
  return (
    <>
      <div className="price-row"><span>{label}</span><span className="tnum">{formatMoney(value)}</span></div>
      {note && <p className="price-row-note">{note}</p>}
    </>
  );
}

/** The ledger's own figures, each shown separately; nothing is derived here. */
export function FinanceSummary({ finance }: { finance: BookingFinance }) {
  return (
    <div className="price-table">
      <Line label="Tiền thuê" value={finance.rent_total} />
      {finance.discount_total.amount > 0 && <Line label="Đã giảm giá" value={finance.discount_total} />}
      {finance.reservation_deposit.amount > 0 && (
        <Line label="Tiền giữ lịch" value={finance.reservation_deposit}
          note={finance.reservation_treatment === 'APPLIED_TO_RENT' ? 'Được trừ vào tiền thuê khi bàn giao.' : 'Hoàn lại sau khi trả hàng.'} />
      )}
      <Line label="Tiền bảo đảm phải có" value={finance.security_required} />
      <Line label="Tiền bảo đảm đang giữ" value={finance.security_held} />
      {finance.delivery_total.amount > 0 && <Line label="Phí giao nhận" value={finance.delivery_total} />}
      {finance.fees_total.amount > 0 && <Line label="Phí phát sinh" value={finance.fees_total} />}
      <Line label="Đã thanh toán" value={finance.paid_total} />
      {finance.refund_pending.amount > 0 && <Line label="Chờ hoàn" value={finance.refund_pending} />}
      {finance.refunded_total.amount > 0 && <Line label="Đã hoàn" value={finance.refunded_total} />}
      <div className="price-row total"><span>Còn phải thanh toán</span><span className="tnum">{formatMoney(finance.outstanding)}</span></div>
    </div>
  );
}

export function PaymentRows({ finance }: { finance: BookingFinance }) {
  return (
    <>
      {finance.payments.map((p) => (
        <div key={p.id} className="row between small">
          <span>{paymentMethod[p.method]}<span className="cell-sub">{formatDateTime(p.at)}</span></span>
          <span className="row"><span className="tnum strong">{formatMoney(p.amount)}</span><StatusBadge status={paymentStatus[p.status]} /></span>
        </div>
      ))}
    </>
  );
}

export function RefundRow({ refund }: { refund: FinanceRefund }) {
  return (
    <div className="row between small">
      <span>Hoàn {bucket[refund.bucket].toLowerCase()}<span className="cell-sub">{formatDateTime(refund.at)}</span></span>
      <span className="row"><span className="tnum strong">{formatMoney(refund.amount)}</span><StatusBadge status={refundStatus[refund.status]} /></span>
    </div>
  );
}

export function PaymentHistory({ finance }: { finance: BookingFinance }) {
  if (finance.payments.length === 0 && finance.refunds.length === 0) return <p className="small muted">Chưa có giao dịch nào.</p>;
  return (
    <div className="stack-sm">
      <PaymentRows finance={finance} />
      {finance.refunds.map((r) => <RefundRow key={r.id} refund={r} />)}
    </div>
  );
}

export function ChargeList({ finance }: { finance: BookingFinance }) {
  return (
    <ul className="stack-sm small">
      {finance.charges.map((c) => (
        <li key={c.id} className="row between">
          <span>{chargeKind[c.kind]}{c.reason && <span className="cell-sub">{c.reason}</span>}</span>
          <span className="tnum" style={c.reversed ? { textDecoration: 'line-through' } : undefined}>{formatMoney(c.amount)}{c.reversed ? ' (đã đảo)' : ''}</span>
        </li>
      ))}
    </ul>
  );
}

export function BookingLines({ booking }: { booking: BookingDetail }) {
  return (
    <>
      {booking.lines.map((l) => (
        <div key={l.id} className="checkout-line" style={{ gridTemplateColumns: '60px minmax(0, 1fr) auto' }}>
          <div className="checkout-thumb" style={{ width: 60 }}><OfferingImage image={l.image} /></div>
          <div><strong>{l.name}</strong><span className="cell-sub">{l.selection} · Số lượng {l.quantity}</span></div>
          <span className="tnum strong">{formatMoney(l.rent)}</span>
        </div>
      ))}
    </>
  );
}

export function scheduleFacts(booking: BookingSummary) {
  return [
    { label: 'Gói thuê', value: booking.package_label },
    { label: 'Nhận hàng', value: formatDateTime(booking.pickup_at) },
    { label: 'Hạn trả', value: formatDateTime(booking.due_at) },
    { label: 'Cách nhận', value: legMode.outbound[booking.outbound] },
    { label: 'Cách trả', value: legMode.return[booking.return] },
  ];
}

export function BookingTable({ rows, linkBase }: { rows: BookingSummary[]; linkBase: string }) {
  const columns: Column<BookingSummary>[] = [
    {
      key: 'code', header: 'Mã đơn',
      render: (b) => <Link className="cell-title mono" to={`${linkBase}/${b.id}`}>{b.code}</Link>,
    },
    {
      key: 'customer', header: 'Khách',
      render: (b) => <span>{b.customer.name}<span className="cell-sub">{b.customer.phone}</span></span>,
    },
    { key: 'headline', header: 'Trang phục', render: (b) => <span>{b.headline}<span className="cell-sub">{b.item_count} món · {b.package_label}</span></span> },
    { key: 'pickup', header: 'Nhận', render: (b) => <span className="nowrap">{formatDateTime(b.pickup_at)}</span> },
    { key: 'due', header: 'Hạn trả', render: (b) => <span className="nowrap">{formatDateTime(b.due_at)}</span> },
    {
      key: 'status', header: 'Trạng thái',
      render: (b) => <span className="row" style={{ gap: 4 }}><StatusBadge status={bookingStatus[b.status]} />{b.is_overdue && <StatusBadge status={{ label: 'Quá hạn', tone: 'danger' }} />}</span>,
    },
    { key: 'payment', header: 'Thanh toán', render: (b) => <StatusBadge status={paymentState[b.payment_state]} /> },
  ];
  return <DataTable rows={rows} columns={columns} rowKey={(b) => b.id} caption="Danh sách đơn thuê" />;
}
