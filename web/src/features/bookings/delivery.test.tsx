import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import { ToastProvider } from '../../components/feedback';
import { deliveryApi } from '../../lib/api/endpoints';
import type { DeliveryTask } from '../../lib/api/types';
import { DeliveryTaskCard } from './delivery';

const base: DeliveryTask = {
  id: 't1', booking_id: 'b1', booking_code: 'BK-1', customer_name: 'Khách Test', direction: 'OUTBOUND', status: 'PENDING_CREATION',
  provider: 'ghn', shipment_code: null, window_from: '2026-11-10T00:00:00Z', window_until: '2026-11-10T02:00:00Z',
  expected_at: null, picked_up_at: null, delivered_at: null, received_at: null, customer_fee: { amount: 30000, currency: 'VND' },
  address: { recipient_name: 'Khách Test', phone: '0911111111', line: '2 Đường B', ward_code: 'W1', ward_name: 'Phường Một' },
  attempts: [], gave_up_at: null, follow_up: null, closed_reason: '', replaces: null, replaced_by: null, failure_reason: '',
  actions: ['RECORD_SHIPMENT'], can_collect_at_store: false, can_end_booking: false, awaiting_follow_up: false,
};

// jsdom has no modal dialogs.
beforeAll(() => {
  HTMLDialogElement.prototype.showModal ??= function showModal(this: HTMLDialogElement) { this.setAttribute('open', ''); };
  HTMLDialogElement.prototype.close ??= function close(this: HTMLDialogElement) { this.removeAttribute('open'); };
});

function show(task: DeliveryTask) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <ToastProvider><MemoryRouter><DeliveryTaskCard task={task} showBooking /></MemoryRouter></ToastProvider>
    </QueryClientProvider>,
  );
}

describe('DeliveryTaskCard', () => {
  it('offers only the actions the server allows', () => {
    show(base);
    expect(screen.getByRole('button', { name: 'Nhập mã vận đơn' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Chọn hướng xử lý' })).not.toBeInTheDocument();
    expect(screen.getByText(/Phường Một/)).toBeInTheDocument();
  });

  it('asks to classify before choosing a follow-up once the carrier gave up', () => {
    show({
      ...base, status: 'MANUAL_REVIEW', awaiting_follow_up: true, actions: ['CLASSIFY_ATTEMPT'],
      attempts: [{ id: 'a1', status: 'DELIVERY_FAILED', at: '2026-11-10T03:00:00Z', fault: 'UNKNOWN', reason: 'hết số lần', classified: false }],
    });
    expect(screen.getByText(/Phân loại lỗi của lần thất bại cuối/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Phân loại lỗi' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Chọn hướng xử lý' })).not.toBeInTheDocument();
  });

  it('sends the shipment code staff typed', async () => {
    const record = vi.spyOn(deliveryApi, 'recordShipment').mockResolvedValue({ ...base, status: 'CREATED', shipment_code: 'GHN1', actions: [] });
    show(base);
    await userEvent.click(screen.getByRole('button', { name: 'Nhập mã vận đơn' }));
    await userEvent.type(screen.getByRole('textbox', { name: /Mã vận đơn/ }), ' GHN1 ');
    await userEvent.click(screen.getByRole('button', { name: 'Lưu' }));
    expect(record).toHaveBeenCalledWith('t1', 'GHN1');
  });
});
