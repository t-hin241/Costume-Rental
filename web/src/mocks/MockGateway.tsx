import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { QueryState, useAction } from '../components/feedback';
import { Button, Notice, Skeleton } from '../components/ui';
import { api } from '../lib/api/client';
import { paymentApi } from '../lib/api/endpoints';
import type { PaymentSession } from '../lib/api/types';
import { formatCountdown, formatMoney } from '../lib/format';
import { paymentChannel, paymentPurpose } from '../lib/labels';

// Stands in for the gateway's hosted page during development. It deliberately
// has no card fields: on a real gateway those live on the provider's site.

function ChannelBody({ session }: { session: PaymentSession }) {
  const [qr, setQr] = useState<string | null>(null);
  const scannable = session.channel === 'BANK_QR' || session.channel === 'E_WALLET';

  useEffect(() => {
    if (!scannable) return;
    let live = true;
    void import('qrcode').then(({ toDataURL }) => toDataURL(`GIA-LAP:${session.id}`, { width: 196, margin: 1 })).then((url) => {
      if (live) setQr(url);
    });
    return () => { live = false; };
  }, [scannable, session.id]);

  if (scannable) {
    return (
      <>
        {qr && <img className="qr" src={qr} alt="Mã QR giả lập, không dùng để thanh toán" />}
        <p className="small muted">Trên cổng thật, khách quét mã bằng ứng dụng rồi xác nhận. Mã này không có giá trị.</p>
      </>
    );
  }
  return <p className="small muted">Trên cổng thật, khách nhập thông tin thẻ tại đây, trên trang của nhà cung cấp. Bản giả lập không có ô nhập thẻ và không nhận dữ liệu thẻ.</p>;
}

function Gateway({ session }: { session: PaymentSession }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);

  const finish = useAction(
    (outcome: 'SUCCEEDED' | 'FAILED') => api<void>(`/mock-gateway/${session.id}/complete`, { method: 'POST', body: { outcome } }),
    { inline: true, onSuccess: () => window.location.assign(`/payment/return?session=${encodeURIComponent(session.id)}`) },
  );
  const left = new Date(session.expires_at).getTime() - now;

  return (
    <>
      <div>
        <p className="small muted">Thanh toán cho Webee Rental, đơn {session.booking_code}</p>
        <p className="gateway-amount tnum">{formatMoney(session.amount)}</p>
        <p className="small muted">{paymentPurpose[session.purpose].label} · {paymentChannel[session.channel]}</p>
      </div>
      <ChannelBody session={session} />
      {session.status !== 'PENDING'
        ? <Notice tone="warning" title="Giao dịch này đã kết thúc" />
        : (
          <>
            <p className="small">Hết hạn sau <strong className="tnum">{formatCountdown(left)}</strong></p>
            <Button size="lg" block loading={finish.isPending} onClick={() => finish.mutate('SUCCEEDED')}>Giả lập thanh toán thành công</Button>
            <Button variant="outline" block disabled={finish.isPending} onClick={() => finish.mutate('FAILED')}>Giả lập thanh toán thất bại</Button>
          </>
        )}
    </>
  );
}

export default function MockGateway() {
  const { id = '' } = useParams();
  const session = useQuery({ queryKey: ['payment-session', id], queryFn: () => paymentApi.session(id) });
  return (
    <main className="gateway-page">
      <div className="gateway-card">
        <p className="gateway-note">Cổng thanh toán giả lập, không có giao dịch thật</p>
        <QueryState query={session} skeleton={<Skeleton height={220} />}>{(s) => <Gateway session={s} />}</QueryState>
      </div>
    </main>
  );
}
