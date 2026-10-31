import { useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Icon } from '../../components/Icon';
import { LogoMark } from '../../components/Logo';
import { Notice, Skeleton } from '../../components/ui';
import { catalogApi, storeApi } from '../../lib/api/endpoints';
import type { Availability, CalendarDay, Image, Money, OfferingCard, Quote, RentalPackage, ReservationTreatment } from '../../lib/api/types';
import type { BagSchedule } from '../../lib/bag';
import { addBusinessDays, businessToday, formatDateTime, formatMoney } from '../../lib/format';

export function OfferingImage({ image }: { image: Image | null | undefined }) {
  if (!image) return <div className="no-image"><LogoMark size={54} /></div>;
  return <img src={image.url} alt={image.alt} loading="lazy" />;
}

export function ProductCard({ offering }: { offering: OfferingCard }) {
  return (
    <Link to={`/catalog/${offering.kind.toLowerCase()}/${offering.slug}`} className="product-card">
      <div className="product-media">
        <OfferingImage image={offering.images[0]} />
        {offering.kind === 'BUNDLE' && <span className="product-kind">Nguyên bộ</span>}
      </div>
      <div className="product-info">
        {offering.category && <p className="product-cat">{offering.category.name}</p>}
        <h3 className="product-name">{offering.name}</h3>
        <p className="product-price">Từ <strong className="tnum">{formatMoney(offering.from_price)}</strong> <span className="muted">/ {offering.from_package_label}</span></p>
        {offering.sizes.length > 0 && <div className="product-sizes">{offering.sizes.slice(0, 6).map((s) => <span key={s}>{s}</span>)}</div>}
      </div>
    </Link>
  );
}

const monthName = new Intl.DateTimeFormat('vi-VN', { timeZone: 'UTC', month: 'long', year: 'numeric' });
const dow = ['T2', 'T3', 'T4', 'T5', 'T6', 'T7', 'CN'];

function firstOfMonth(date: string): string {
  return `${date.slice(0, 7)}-01`;
}

function shiftMonth(month: string, by: number): string {
  const d = new Date(`${month}T00:00:00Z`);
  d.setUTCMonth(d.getUTCMonth() + by);
  return d.toISOString().slice(0, 10);
}

function lastOfMonth(month: string): string {
  return addBusinessDays(shiftMonth(month, 1), -1);
}

/** Month view of the store calendar: which days are open comes from the API. */
export function StoreCalendarPicker({ value, onChange }: { value?: string; onChange: (date: string) => void }) {
  const [month, setMonth] = useState(() => firstOfMonth(value ?? businessToday()));
  const calendar = useQuery({
    queryKey: ['store-calendar', month],
    queryFn: () => storeApi.calendar(month, lastOfMonth(month)),
    staleTime: 60_000,
  });

  // Open on the first month with bookable days, not on a month of disabled ones.
  const earliest = calendar.data?.min_date;
  useEffect(() => {
    if (!value && earliest && firstOfMonth(earliest) > month) setMonth(firstOfMonth(earliest));
  }, [value, earliest, month]);

  const cells = useMemo(() => {
    const byDate = new Map<string, CalendarDay>((calendar.data?.days ?? []).map((d) => [d.date, d]));
    const first = new Date(`${month}T00:00:00Z`);
    const lead = (first.getUTCDay() + 6) % 7; // Monday first
    const out: (string | null)[] = Array.from({ length: lead }, () => null);
    for (let d = month; d <= lastOfMonth(month); d = addBusinessDays(d, 1)) out.push(d);
    return { byDate, out };
  }, [calendar.data, month]);

  const data = calendar.data;
  const canPrev = !data || firstOfMonth(data.min_date) < month;
  const canNext = !data || lastOfMonth(month) < data.max_date;

  return (
    <div className="cal">
      <div className="cal-head">
        <button type="button" className="icon-button" aria-label="Tháng trước" disabled={!canPrev} onClick={() => setMonth(shiftMonth(month, -1))}><Icon name="chevronLeft" size={18} /></button>
        <span>{monthName.format(new Date(`${month}T00:00:00Z`))}</span>
        <button type="button" className="icon-button" aria-label="Tháng sau" disabled={!canNext} onClick={() => setMonth(shiftMonth(month, 1))}><Icon name="chevronRight" size={18} /></button>
      </div>
      {calendar.isPending ? <Skeleton height={260} /> : (
        <div className="cal-grid">
          {dow.map((d) => <span key={d} className="cal-dow">{d}</span>)}
          {cells.out.map((date, i) => {
            if (!date) return <span key={`blank-${i}`} />;
            const day = cells.byDate.get(date);
            const open = Boolean(day?.open);
            return (
              <button key={date} type="button" className="cal-day" disabled={!open} aria-pressed={value === date}
                title={day?.reason ?? undefined} aria-label={`Ngày ${Number(date.slice(8))}${open ? '' : `, không nhận hàng${day?.reason ? `: ${day.reason}` : ''}`}`}
                onClick={() => onChange(date)}>
                {Number(date.slice(8))}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** Picks the rental package, pickup date and slot; the bag shares this choice. */
export function SchedulePicker({ packages, prices, value, onChange }: {
  packages: RentalPackage[]; prices?: Map<string, Money>; value: Partial<BagSchedule>; onChange: (v: Partial<BagSchedule>) => void;
}) {
  const month = value.pickup_date ? firstOfMonth(value.pickup_date) : null;
  const calendar = useQuery({
    queryKey: ['store-calendar', month],
    queryFn: () => storeApi.calendar(month as string, lastOfMonth(month as string)),
    enabled: month !== null,
    staleTime: 60_000,
  });
  const slots = calendar.data?.days.find((d) => d.date === value.pickup_date)?.slots ?? [];

  return (
    <div className="stack">
      <div>
        <p className="filter-title" id="package-label">Gói thuê</p>
        <div className="package-grid" role="radiogroup" aria-labelledby="package-label">
          {packages.map((p) => {
            const price = prices?.get(p.id);
            return (
              <button key={p.id} type="button" role="radio" className="package-option" aria-checked={value.package_id === p.id} onClick={() => onChange({ package_id: p.id })}>
                <strong>{p.label}</strong>
                {price && <span className="tnum">{formatMoney(price)}</span>}
              </button>
            );
          })}
        </div>
      </div>
      <div>
        <p className="filter-title">Ngày nhận hàng</p>
        <StoreCalendarPicker value={value.pickup_date} onChange={(pickup_date) => onChange({ pickup_date, slot_id: undefined })} />
      </div>
      {value.pickup_date && (
        <div>
          <p className="filter-title" id="slot-label">Khung giờ nhận</p>
          <div className="chip-group" role="radiogroup" aria-labelledby="slot-label">
            {slots.map((s) => (
              <button key={s.id} type="button" role="radio" className="chip" aria-checked={value.slot_id === s.id} onClick={() => onChange({ slot_id: s.id })}>{s.label}</button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

export function usePackages() {
  return useQuery({ queryKey: ['packages'], queryFn: catalogApi.packages, staleTime: 5 * 60_000 });
}

export function AvailabilityNotice({ result }: { result: Availability }) {
  if (result.available) {
    return (
      <Notice tone="success" title="Còn hàng trong thời gian đã chọn">
        Nhận {formatDateTime(result.pickup_at)}, trả trước {formatDateTime(result.due_at)}. Kết quả chỉ để tham khảo; hàng được giữ khi bạn đặt thuê.
      </Notice>
    );
  }
  return (
    <Notice tone="danger" title="Không đủ hàng trong thời gian đã chọn">
      <ul className="plain">
        {result.shortages.map((s) => <li key={`${s.line_ref}-${s.label}`}>{s.label}: cần {s.requested}, còn {s.available}</li>)}
      </ul>
      Hãy thử ngày khác hoặc size khác.
    </Notice>
  );
}

const treatmentNote: Record<ReservationTreatment, string> = {
  APPLIED_TO_RENT: 'Được trừ vào tiền thuê khi bàn giao, không phải khoản thu thêm.',
  REFUNDED_AFTER_RETURN: 'Khoản riêng, hoàn lại sau khi trả hàng.',
};

function Row({ label, value, note, kind }: { label: string; value: string; note?: string; kind?: 'discount' | 'total' }) {
  return (
    <>
      <div className={`price-row${kind ? ` ${kind}` : ''}`}><span>{label}</span><span className="tnum">{value}</span></div>
      {note && <p className="price-row-note">{note}</p>}
    </>
  );
}

/** Every amount is the server's quote, shown separately and never added up here. */
export function QuoteBreakdown({ quote }: { quote: Quote }) {
  const deliveryOut = quote.delivery_outbound_fee.amount > 0;
  const deliveryBack = quote.delivery_return_fee.amount > 0;
  return (
    <div className="price-table">
      {quote.lines.map((l) => <Row key={l.label} label={`${l.label} × ${l.quantity}`} value={formatMoney(l.list_rent)} />)}
      {quote.discount_total.amount > 0 && <Row kind="discount" label="Giảm giá" value={`−${formatMoney(quote.discount_total)}`} />}
      <Row label="Tiền thuê" value={formatMoney(quote.rent_total)} />
      <Row label="Tiền bảo đảm" value={formatMoney(quote.security_total)} note="Hoàn lại sau khi trả hàng nếu không có khoản khấu trừ hợp lệ." />
      {quote.reservation_deposit.amount > 0 && (
        <Row label="Tiền giữ lịch" value={formatMoney(quote.reservation_deposit)}
          note={quote.reservation_treatment ? treatmentNote[quote.reservation_treatment] : undefined} />
      )}
      <Row label="Phí giao đi" value={deliveryOut ? formatMoney(quote.delivery_outbound_fee) : 'Không có'} />
      <Row label="Phí lấy về" value={deliveryBack ? formatMoney(quote.delivery_return_fee) : 'Không có'} />
      <Row kind="total" label="Tổng trước khi nhận hàng" value={formatMoney(quote.due_before_handover)} />
      {quote.refundable_total.amount > 0 && <p className="price-row-note">Trong đó {formatMoney(quote.refundable_total)} là tiền được hoàn lại.</p>}
      {quote.due_to_confirm.amount > 0 && (
        <div className="price-callout">
          <div className="price-row"><span>Thanh toán để giữ lịch</span><strong className="tnum">{formatMoney(quote.due_to_confirm)}</strong></div>
        </div>
      )}
    </div>
  );
}
