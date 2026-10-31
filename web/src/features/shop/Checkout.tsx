import { useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Icon } from '../../components/Icon';
import { QueryError, useAction } from '../../components/feedback';
import { Button, Card, Checkbox, EmptyState, Field, FormError, Input, Notice, PageHeader, Select, Skeleton, btn } from '../../components/ui';
import { ApiError } from '../../lib/api/client';
import { bookingApi, storeApi } from '../../lib/api/endpoints';
import type { BookingInput, DeliveryAddress, LegMode } from '../../lib/api/types';
import { useAuth } from '../../lib/auth';
import { toBookingLines, useBag } from '../../lib/bag';
import { useRequestKey } from '../../lib/hooks';
import { legMode } from '../../lib/labels';
import { AvailabilityNotice, OfferingImage, QuoteBreakdown, SchedulePicker, usePackages } from './shared';

const emptyAddress: DeliveryAddress = { recipient_name: '', phone: '', line: '', ward_code: '' };

function LegChoice({ title, direction, options, value, onChange }: {
  title: string; direction: 'outbound' | 'return'; options: LegMode[]; value: LegMode; onChange: (m: LegMode) => void;
}) {
  return (
    <div>
      <p className="filter-title">{title}</p>
      <div className="option-cards" role="radiogroup" aria-label={title}>
        {options.map((m) => (
          <button key={m} type="button" role="radio" className="option-card" aria-checked={value === m} onClick={() => onChange(m)}>
            <Icon name={m === 'STORE' ? 'store' : 'truck'} size={18} />{legMode[direction][m]}
          </button>
        ))}
      </div>
    </div>
  );
}

export default function Checkout() {
  const bag = useBag();
  const { session } = useAuth();
  const navigate = useNavigate();
  const packages = usePackages();
  const fulfillment = useQuery({ queryKey: ['fulfillment'], queryFn: storeApi.fulfillment, staleTime: 5 * 60_000 });
  const store = useQuery({ queryKey: ['store-info'], queryFn: storeApi.info, staleTime: 5 * 60_000, retry: false });

  const [outbound, setOutbound] = useState<LegMode>('STORE');
  const [returnLeg, setReturnLeg] = useState<LegMode>('STORE');
  const [address, setAddress] = useState(emptyAddress);
  const [couponDraft, setCouponDraft] = useState('');
  const [coupon, setCoupon] = useState('');
  const [agreed, setAgreed] = useState(false);

  const courier = outbound === 'COURIER' || returnLeg === 'COURIER';
  const addressComplete = !courier || Boolean(address.recipient_name.trim() && address.phone.trim() && address.line.trim() && address.ward_code);
  const { package_id, pickup_date, slot_id } = bag.schedule;

  const input = useMemo<BookingInput | null>(() => {
    if (bag.lines.length === 0 || !package_id || !pickup_date || !slot_id || !addressComplete) return null;
    return {
      package_id, pickup_date, slot_id, outbound, return: returnLeg,
      lines: toBookingLines(bag.lines),
      address: courier ? address : undefined,
      coupon_code: coupon || undefined,
    };
  }, [bag.lines, package_id, pickup_date, slot_id, outbound, returnLeg, courier, address, coupon, addressComplete]);

  const availability = useQuery({
    queryKey: ['availability', input],
    queryFn: () => bookingApi.availability(input as BookingInput),
    enabled: input !== null,
    staleTime: 0,
  });
  const quote = useQuery({
    queryKey: ['quote', input],
    queryFn: () => bookingApi.quote(input as BookingInput),
    enabled: input !== null,
    retry: false,
  });

  const requestKey = useRequestKey();

  const place = useAction(
    (body: BookingInput) => bookingApi.place({ ...body, request_key: requestKey(body) }),
    {
      inline: true,
      success: 'Đã tạo đơn thuê.',
      invalidate: [['bookings']],
      onSuccess: (booking) => {
        bag.clear();
        navigate(`/account/bookings/${booking.id}`);
      },
    },
  );

  const legOptions = fulfillment.data;
  useEffect(() => {
    if (legOptions && !legOptions.outbound.includes(outbound)) setOutbound(legOptions.outbound[0] ?? 'STORE');
    if (legOptions && !legOptions.return.includes(returnLeg)) setReturnLeg(legOptions.return[0] ?? 'STORE');
  }, [legOptions, outbound, returnLeg]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (input) place.mutate(input);
  };

  if (bag.lines.length === 0) {
    return (
      <div className="container">
        <EmptyState icon="bag" title="Giỏ thuê đang trống">
          <p>Chọn trang phục và lịch thuê để bắt đầu.</p>
          <Link className={btn('primary')} to="/catalog">Xem trang phục</Link>
        </EmptyState>
      </div>
    );
  }

  const couponError = quote.error instanceof ApiError && quote.error.code.startsWith('promotions.') ? quote.error : null;
  const canSubmit = Boolean(input && agreed && availability.data?.available && quote.data);

  return (
    <div className="container" style={{ paddingTop: 24 }}>
      <PageHeader title="Đặt thuê" subtitle="Kiểm tra lại trang phục, lịch nhận và chi phí trước khi đặt." />
      <form className="split" onSubmit={submit}>
        <div className="stack">
          <Card title={`Trang phục (${bag.count})`}>
            {bag.lines.map((l) => (
              <div key={l.key} className="checkout-line">
                <div className="checkout-thumb"><OfferingImage image={l.image} /></div>
                <div>
                  <Link className="cell-title" to={`/catalog/${l.kind.toLowerCase()}/${l.slug}`}>{l.name}</Link>
                  <span className="cell-sub">{l.selection_label}</span>
                </div>
                <div className="row">
                  <div className="qty">
                    <button type="button" aria-label="Giảm số lượng" disabled={l.quantity <= 1} onClick={() => bag.setQuantity(l.key, l.quantity - 1)}>−</button>
                    <span aria-label="Số lượng">{l.quantity}</span>
                    <button type="button" aria-label="Tăng số lượng" onClick={() => bag.setQuantity(l.key, l.quantity + 1)}>+</button>
                  </div>
                  <button type="button" className="icon-button" aria-label={`Bỏ ${l.name}`} onClick={() => bag.removeLine(l.key)}><Icon name="trash" size={18} /></button>
                </div>
              </div>
            ))}
          </Card>

          <Card title="Lịch thuê">
            {packages.isPending ? <Skeleton height={200} /> : packages.isError ? <QueryError error={packages.error} /> : (
              <SchedulePicker packages={packages.data.filter((p) => p.status === 'ACTIVE')} value={bag.schedule} onChange={bag.setSchedule} />
            )}
          </Card>

          <Card title="Nhận và trả hàng">
            {legOptions ? (
              <div className="stack">
                <LegChoice title="Cách nhận" direction="outbound" options={legOptions.outbound} value={outbound} onChange={setOutbound} />
                <LegChoice title="Cách trả" direction="return" options={legOptions.return} value={returnLeg} onChange={setReturnLeg} />
                {courier && (
                  <div className="form-grid">
                    <Field label="Người nhận" required>
                      <Input autoComplete="name" value={address.recipient_name} onChange={(e) => setAddress({ ...address, recipient_name: e.target.value })} />
                    </Field>
                    <Field label="Số điện thoại" required>
                      <Input type="tel" autoComplete="tel" value={address.phone} onChange={(e) => setAddress({ ...address, phone: e.target.value })} />
                    </Field>
                    <div className="full">
                      <Field label="Số nhà, tên đường" required>
                        <Input autoComplete="street-address" value={address.line} onChange={(e) => setAddress({ ...address, line: e.target.value })} />
                      </Field>
                    </div>
                    <div className="full">
                      <Field label="Phường trong khu vực phục vụ" required>
                        <Select value={address.ward_code} onChange={(e) => setAddress({ ...address, ward_code: e.target.value })}>
                          <option value="">Chọn phường</option>
                          {legOptions.zones.map((z) => (
                            <optgroup key={z.id} label={z.name}>
                              {z.wards.map((w) => <option key={w.code} value={w.code}>{w.name}</option>)}
                            </optgroup>
                          ))}
                        </Select>
                      </Field>
                    </div>
                  </div>
                )}
              </div>
            ) : fulfillment.isError ? <QueryError error={fulfillment.error} /> : <Skeleton height={100} />}
          </Card>

          <Card title="Mã giảm giá">
            <div className="row">
              <div style={{ flex: 1, minWidth: 180 }}>
                <Input aria-label="Mã giảm giá" value={couponDraft} maxLength={40} onChange={(e) => setCouponDraft(e.target.value.toUpperCase())} />
              </div>
              {coupon
                ? <Button variant="ghost" onClick={() => { setCoupon(''); setCouponDraft(''); }}>Bỏ mã</Button>
                : <Button variant="outline" disabled={!couponDraft.trim()} onClick={() => setCoupon(couponDraft.trim())}>Áp dụng</Button>}
            </div>
            {couponError && <p className="field-error" style={{ marginTop: 6 }}>{couponError.message}</p>}
          </Card>
        </div>

        <div className="sticky-summary stack">
          <Card title="Chi phí">
            <div className="stack">
              {!input && <Notice tone="brand">Hoàn tất lịch thuê và thông tin nhận hàng để xem chi phí.</Notice>}
              {availability.data && <AvailabilityNotice result={availability.data} />}
              {input && quote.isPending && <Skeleton height={220} />}
              {quote.data && <QuoteBreakdown quote={quote.data} />}
              {quote.error && !couponError && <QueryError error={quote.error} />}
              {quote.data && (
                <p className="small muted">Sau khi đặt, hàng được giữ trong {quote.data.hold_minutes} phút để chờ xác nhận và thanh toán.</p>
              )}
              {store.data && (store.data.online_payment_available
                ? <Notice tone="info" title="Thanh toán trực tuyến">Sau khi đặt, bạn chọn số tiền và hình thức thanh toán trên trang đơn thuê.</Notice>
                : <Notice tone="info" title="Thanh toán tại cửa hàng">{store.data.payment_instructions}</Notice>)}
              <Checkbox label={<>Tôi đồng ý với <Link className="link" to="/policies" target="_blank">chính sách thuê</Link></>}
                checked={agreed} onChange={(e) => setAgreed(e.target.checked)} />
              <FormError error={place.error} />
              {session
                ? <Button type="submit" size="lg" block disabled={!canSubmit} loading={place.isPending}>Xác nhận đặt thuê</Button>
                : <Link className={btn('primary', 'lg', 'btn-block')} to="/login?next=/checkout">Đăng nhập để đặt thuê</Link>}
            </div>
          </Card>
        </div>
      </form>
    </div>
  );
}
