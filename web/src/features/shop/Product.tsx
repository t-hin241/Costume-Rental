import { useQuery } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { QueryState, useToast } from '../../components/feedback';
import { Badge, Button, FormError, MoneyText, Notice, Skeleton } from '../../components/ui';
import { ApiError } from '../../lib/api/client';
import { bookingApi, catalogApi } from '../../lib/api/endpoints';
import type { BookingInput, BundleComponent, OfferingDetail, Variant } from '../../lib/api/types';
import { useBag } from '../../lib/bag';
import { AvailabilityNotice, OfferingImage, QuoteBreakdown, SchedulePicker } from './shared';

function variantLabel(v: Variant, showColor: boolean): string {
  return showColor && v.color ? `${v.size} · ${v.color}` : v.size;
}

function VariantChips({ title, variants, value, onChange }: {
  title: string; variants: Variant[]; value?: string; onChange: (id: string) => void;
}) {
  const manyColors = new Set(variants.map((v) => v.color)).size > 1;
  return (
    <div>
      <p className="filter-title">{title}</p>
      <div className="chip-group" role="radiogroup" aria-label={title}>
        {variants.map((v) => (
          <button key={v.id} type="button" role="radio" className="chip" aria-checked={value === v.id} disabled={v.status !== 'ACTIVE'} onClick={() => onChange(v.id)}>
            {variantLabel(v, manyColors)}
          </button>
        ))}
      </div>
    </div>
  );
}

export default function Product() {
  const { kind = '', slug = '' } = useParams();
  const offering = useQuery({ queryKey: ['offering', kind, slug], queryFn: () => catalogApi.offering(kind.toUpperCase(), slug) });
  return (
    <div className="container">
      <QueryState query={offering} skeleton={<div style={{ paddingTop: 24 }}><Skeleton height={520} /></div>}>
        {(o) => <ProductView offering={o} />}
      </QueryState>
    </div>
  );
}

function ProductView({ offering }: { offering: OfferingDetail }) {
  const bag = useBag();
  const toast = useToast();
  const navigate = useNavigate();
  const [imageIndex, setImageIndex] = useState(0);
  const [variantId, setVariantId] = useState<string>();
  const [picks, setPicks] = useState<Record<string, string>>(() => singleOptionPicks(offering.components));

  const isBundle = offering.kind === 'BUNDLE';
  const selectionComplete = isBundle ? offering.components.every((c) => picks[c.key]) : Boolean(variantId);
  const schedule = bag.schedule;
  const scheduleComplete = Boolean(schedule.package_id && schedule.pickup_date && schedule.slot_id);

  const prices = useMemo(() => new Map(offering.prices.map((p) => [p.package.id, p.price])), [offering.prices]);

  const line = {
    kind: offering.kind,
    offering_id: offering.id,
    variant_id: isBundle ? undefined : variantId,
    selections: isBundle ? picks : undefined,
    quantity: 1,
  };

  const input: BookingInput | null = selectionComplete && scheduleComplete ? {
    package_id: schedule.package_id as string,
    pickup_date: schedule.pickup_date as string,
    slot_id: schedule.slot_id as string,
    outbound: 'STORE',
    return: 'STORE',
    lines: [line],
  } : null;

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
  });

  const selectionLabel = isBundle
    ? offering.components.map((c) => `${c.item_name}: ${c.options.find((o) => o.id === picks[c.key])?.size ?? ''}`).join(', ')
    : offering.variants.find((v) => v.id === variantId)?.size ?? '';

  const addToBag = (goToCheckout: boolean) => {
    bag.addLine({ ...line, slug: offering.slug, name: offering.name, image: offering.images[0] ?? null, selection_label: selectionLabel });
    if (goToCheckout) navigate('/checkout');
    else toast.success('Đã thêm vào giỏ thuê.');
  };

  const canAdd = selectionComplete && scheduleComplete && availability.data?.available === true;

  return (
    <div className="pdp">
      <div>
        <div className="gallery-main"><OfferingImage image={offering.images[imageIndex]} /></div>
        {offering.images.length > 1 && (
          <div className="gallery-thumbs">
            {offering.images.map((img, i) => (
              <button key={img.url} type="button" aria-label={`Ảnh ${i + 1}`} aria-current={i === imageIndex} onClick={() => setImageIndex(i)}>
                <img src={img.url} alt="" />
              </button>
            ))}
          </div>
        )}
      </div>

      <div>
        {offering.category && <Link className="product-cat" to={`/catalog?category=${offering.category.slug}`}>{offering.category.name}</Link>}
        <h1 className="pdp-title">{offering.name}</h1>
        <div className="row" style={{ marginTop: 8 }}>
          {isBundle && <Badge tone="brand" label="Thuê nguyên bộ" />}
          <span className="small muted">Tiền bảo đảm <strong><MoneyText value={offering.security_deposit} /></strong></span>
        </div>
        {offering.description && <p className="muted" style={{ marginTop: 12 }}>{offering.description}</p>}

        <section className="pdp-section stack" aria-label="Chọn size">
          <h2>{isBundle ? 'Chọn size từng món trong bộ' : 'Chọn size'}</h2>
          {isBundle
            ? offering.components.map((c) => (
              <VariantChips key={c.key} title={`${c.item_name}${c.quantity > 1 ? ` × ${c.quantity}` : ''}`} variants={c.options} value={picks[c.key]}
                onChange={(id) => setPicks((p) => ({ ...p, [c.key]: id }))} />
            ))
            : <VariantChips title="Size" variants={offering.variants} value={variantId} onChange={setVariantId} />}
        </section>

        <section className="pdp-section" aria-label="Chọn lịch thuê">
          <h2>Chọn lịch thuê</h2>
          <SchedulePicker packages={offering.prices.map((p) => p.package)} prices={prices} value={schedule} onChange={bag.setSchedule} />
        </section>

        <section className="pdp-section stack" aria-label="Chi phí">
          {!input && <Notice tone="brand">Chọn size, gói thuê, ngày và khung giờ để xem tình trạng còn hàng và chi phí.</Notice>}
          {input && availability.isPending && <Skeleton height={64} />}
          {availability.data && <AvailabilityNotice result={availability.data} />}
          {availability.error && !(availability.error instanceof ApiError && availability.error.endpointMissing) && <FormError error={availability.error} />}
          {quote.data && <QuoteBreakdown quote={quote.data} />}
          {quote.error && !availability.error && !(quote.error instanceof ApiError && quote.error.endpointMissing) && <FormError error={quote.error} />}
          <div className="grid-2">
            <Button variant="outline" size="lg" disabled={!canAdd} onClick={() => addToBag(false)}>Thêm vào giỏ thuê</Button>
            <Button size="lg" disabled={!canAdd} onClick={() => addToBag(true)}>Đặt thuê ngay</Button>
          </div>
        </section>
      </div>
    </div>
  );
}

function singleOptionPicks(components: BundleComponent[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const c of components) {
    if (c.options.length === 1 && c.options[0]) out[c.key] = c.options[0].id;
  }
  return out;
}
