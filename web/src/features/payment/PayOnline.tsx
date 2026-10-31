import { useState } from 'react';
import { Icon, type IconName } from '../../components/Icon';
import { useAction } from '../../components/feedback';
import { Button, Card, FormError } from '../../components/ui';
import { paymentApi } from '../../lib/api/endpoints';
import type { BookingDetail, PaymentChannel, PaymentPurpose } from '../../lib/api/types';
import { formatMoney } from '../../lib/format';
import { useRequestKey } from '../../lib/hooks';
import { paymentChannel, paymentPurpose } from '../../lib/labels';
import { checkoutTarget } from './checkout-target';

const channelIcon: Record<PaymentChannel, IconName> = { ATM_CARD: 'card', INTL_CARD: 'card', E_WALLET: 'bag', BANK_QR: 'grid' };

/**
 * Starts a hosted payment. The customer pays on the gateway's own page, so
 * card details never pass through this site.
 */
export function PayOnline({ booking, channels }: { booking: BookingDetail; channels: PaymentChannel[] }) {
  const [purpose, setPurpose] = useState<PaymentPurpose | undefined>(booking.payment_options[0]?.purpose);
  const [channel, setChannel] = useState<PaymentChannel | undefined>(channels[0]);
  const requestKey = useRequestKey();
  const chosen = booking.payment_options.find((o) => o.purpose === purpose);

  const pay = useAction(async () => {
    const input = { purpose: purpose as PaymentPurpose, channel: channel as PaymentChannel };
    const session = await paymentApi.createSession(booking.id, { ...input, idempotency_key: requestKey({ booking: booking.id, ...input }) });
    return checkoutTarget(session.checkout_url);
  }, { inline: true, onSuccess: (url) => window.location.assign(url) });

  return (
    <Card title="Thanh toán trực tuyến">
      <div className="stack">
        <div>
          <p className="filter-title" id="pay-amount">Số tiền</p>
          <div className="option-cards" role="radiogroup" aria-labelledby="pay-amount">
            {booking.payment_options.map((o) => (
              <button key={o.purpose} type="button" role="radio" className="package-option" aria-checked={purpose === o.purpose} onClick={() => setPurpose(o.purpose)}>
                <strong>{paymentPurpose[o.purpose].label}: <span className="tnum">{formatMoney(o.amount)}</span></strong>
                <span>{paymentPurpose[o.purpose].hint}</span>
              </button>
            ))}
          </div>
        </div>
        <div>
          <p className="filter-title" id="pay-channel">Hình thức</p>
          <div className="option-cards" role="radiogroup" aria-labelledby="pay-channel">
            {channels.map((c) => (
              <button key={c} type="button" role="radio" className="option-card" aria-checked={channel === c} onClick={() => setChannel(c)}>
                <Icon name={channelIcon[c]} size={18} />{paymentChannel[c]}
              </button>
            ))}
          </div>
        </div>
        <FormError error={pay.error} />
        <div>
          <Button size="lg" disabled={!chosen || !channel} loading={pay.isPending} onClick={() => pay.mutate(undefined)}>
            {chosen ? `Thanh toán ${formatMoney(chosen.amount)}` : 'Thanh toán'}
          </Button>
        </div>
        <p className="small muted">Bạn sẽ được chuyển đến trang của cổng thanh toán. Webee Rental không nhận và không lưu thông tin thẻ.</p>
      </div>
    </Card>
  );
}
