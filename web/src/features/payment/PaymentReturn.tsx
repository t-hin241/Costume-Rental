import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { EmptyState, Notice, Skeleton, btn } from '../../components/ui';
import { paymentApi } from '../../lib/api/endpoints';
import type { PaymentSession } from '../../lib/api/types';
import { formatMoney } from '../../lib/format';

const pollMs = 2000;

function Outcome({ session }: { session: PaymentSession }) {
  const amount = formatMoney(session.amount);
  switch (session.status) {
    case 'SUCCEEDED':
      return <Notice tone="success" title="Thanh toán thành công">Đã nhận {amount} cho đơn {session.booking_code}.</Notice>;
    case 'FAILED':
      return <Notice tone="danger" title="Thanh toán không thành công">Bạn chưa bị trừ tiền cho giao dịch này. Bạn có thể thử lại trên trang đơn thuê.</Notice>;
    case 'EXPIRED':
      return <Notice tone="warning" title="Giao dịch đã hết hạn">Bạn có thể tạo lại giao dịch trên trang đơn thuê.</Notice>;
    default:
      return (
        <Notice tone="brand" title="Đang chờ xác nhận từ cổng thanh toán">
          Quá trình này thường chỉ mất vài giây. Nếu đã trả tiền, bạn có thể rời trang này; đơn thuê sẽ tự cập nhật khi có kết quả.
        </Notice>
      );
  }
}

/**
 * Where the gateway sends the customer back. Query parameters are never
 * trusted for the result: only the session read from the server counts.
 */
export default function PaymentReturn() {
  const [params] = useSearchParams();
  const client = useQueryClient();
  const id = params.get('session');

  const session = useQuery({
    queryKey: ['payment-session', id],
    queryFn: () => paymentApi.session(id as string),
    enabled: Boolean(id),
    staleTime: 0,
    refetchInterval: (query) => (query.state.data?.status === 'PENDING' ? pollMs : false),
  });

  const status = session.data?.status;
  useEffect(() => {
    if (status && status !== 'PENDING') void client.invalidateQueries({ queryKey: ['bookings'] });
  }, [status, client]);

  if (!id) return <EmptyState icon="alert" title="Không tìm thấy giao dịch" />;

  return (
    <div className="pay-page">
      <div className="pay-card">
        <h1 className="page-title">Kết quả thanh toán</h1>
        <QueryState query={session} skeleton={<Skeleton height={90} />}>
          {(s) => (
            <>
              <Outcome session={s} />
              <Link className={btn('primary', 'lg', 'btn-block')} to={`/account/bookings/${s.booking_id}`}>Về đơn thuê</Link>
            </>
          )}
        </QueryState>
      </div>
    </div>
  );
}
