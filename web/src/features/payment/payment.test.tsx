import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ToastProvider } from '../../components/feedback';
import { ApiError } from '../../lib/api/client';
import { paymentApi } from '../../lib/api/endpoints';
import type { BookingDetail, PaymentSession } from '../../lib/api/types';
import { PayOnline } from './PayOnline';
import PaymentReturn from './PaymentReturn';
import { checkoutTarget } from './checkout-target';

const session: PaymentSession = {
  id: 'ps1', booking_id: 'b1', booking_code: 'BK-1', purpose: 'RESERVATION', channel: 'BANK_QR', amount: { amount: 120000, currency: 'VND' },
  status: 'PENDING', checkout_url: 'https://gateway.test/pay/ps1', expires_at: '2026-11-01T00:00:00Z',
};

const booking = {
  id: 'b1',
  payment_options: [
    { purpose: 'RESERVATION', amount: { amount: 120000, currency: 'VND' } },
    { purpose: 'FULL', amount: { amount: 900000, currency: 'VND' } },
  ],
} as BookingDetail;

function wrap(ui: ReactNode, route = '/') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ToastProvider><MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter></ToastProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe('checkoutTarget', () => {
  it('allows web addresses and same-site paths', () => {
    expect(checkoutTarget('https://gateway.test/pay/1')).toBe('https://gateway.test/pay/1');
    expect(checkoutTarget('/mock-gateway/1')).toBe(`${window.location.origin}/mock-gateway/1`);
  });

  it('refuses anything that could run script', () => {
    for (const bad of ['javascript:alert(1)', 'data:text/html,x', 'JaVaScRiPt:alert(1)']) {
      expect(() => checkoutTarget(bad)).toThrow(ApiError);
    }
  });
});

describe('PaymentReturn', () => {
  afterEach(() => vi.useRealTimers());

  it('believes the server, not the query string', async () => {
    vi.spyOn(paymentApi, 'session').mockResolvedValue({ ...session, status: 'FAILED' });
    wrap(<PaymentReturn />, '/payment/return?session=ps1&status=success&vnp_ResponseCode=00');
    expect(await screen.findByText('Thanh toán không thành công')).toBeInTheDocument();
    expect(screen.queryByText('Thanh toán thành công')).not.toBeInTheDocument();
  });

  it('waits for the gateway result, then shows it', async () => {
    const read = vi.spyOn(paymentApi, 'session').mockResolvedValueOnce(session).mockResolvedValue({ ...session, status: 'SUCCEEDED' });
    wrap(<PaymentReturn />, '/payment/return?session=ps1');
    expect(await screen.findByText('Đang chờ xác nhận từ cổng thanh toán')).toBeInTheDocument();
    expect(await screen.findByText('Thanh toán thành công', {}, { timeout: 4000 })).toBeInTheDocument();
    expect(read).toHaveBeenCalledTimes(2);
  });

  it('stops polling once the session is final', async () => {
    const read = vi.spyOn(paymentApi, 'session').mockResolvedValue({ ...session, status: 'EXPIRED' });
    wrap(<PaymentReturn />, '/payment/return?session=ps1');
    expect(await screen.findByText('Giao dịch đã hết hạn')).toBeInTheDocument();
    await new Promise((r) => { setTimeout(r, 2300); });
    expect(read).toHaveBeenCalledTimes(1);
  });

  it('handles a missing session id', () => {
    wrap(<PaymentReturn />, '/payment/return');
    expect(screen.getByText('Không tìm thấy giao dịch')).toBeInTheDocument();
  });
});

describe('PayOnline', () => {
  it('shows only the amounts the backend offers and sends no amount back', async () => {
    const create = vi.spyOn(paymentApi, 'createSession').mockRejectedValue(new ApiError({ status: 409, code: 'payments.exceeds_outstanding' }));
    wrap(<PayOnline booking={booking} channels={['BANK_QR', 'E_WALLET']} />);
    await userEvent.click(screen.getByRole('radio', { name: /Toàn bộ số còn lại/ }));
    await userEvent.click(screen.getByRole('button', { name: 'Thanh toán 900.000đ' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    const body = create.mock.calls[0]?.[1];
    expect(body).toMatchObject({ purpose: 'FULL', channel: 'BANK_QR' });
    expect(body).not.toHaveProperty('amount');
    expect(await screen.findByText('Số tiền vượt quá khoản còn phải thu.')).toBeInTheDocument();
  });

  it('retries with the same key, and a different choice gets a new one', async () => {
    const create = vi.spyOn(paymentApi, 'createSession').mockRejectedValue(new ApiError({ status: 0, code: 'request.network' }));
    wrap(<PayOnline booking={booking} channels={['BANK_QR', 'E_WALLET']} />);
    const pay = () => userEvent.click(screen.getByRole('button', { name: /^Thanh toán 120/ }));
    await pay();
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    await pay();
    await waitFor(() => expect(create).toHaveBeenCalledTimes(2));
    await userEvent.click(screen.getByRole('radio', { name: 'Ví điện tử' }));
    await pay();
    await waitFor(() => expect(create).toHaveBeenCalledTimes(3));
    const keys = create.mock.calls.map((c) => c[1].idempotency_key);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[2]).not.toBe(keys[0]);
  });
});
