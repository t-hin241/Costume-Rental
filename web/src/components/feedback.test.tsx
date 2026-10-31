import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ApiError } from '../lib/api/client';
import { QueryState, ToastProvider, useAction } from './feedback';

function wrap(ui: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><ToastProvider>{ui}</ToastProvider></QueryClientProvider>);
}

function Loader({ fn }: { fn: () => Promise<string> }) {
  const q = useQuery({ queryKey: ['x'], queryFn: fn });
  return <QueryState query={q}>{(d) => <p>{d}</p>}</QueryState>;
}

function Acting({ fn }: { fn: () => Promise<void> }) {
  const action = useAction(fn, { success: 'Đã lưu.' });
  return <button type="button" onClick={() => action.mutate(undefined)}>Lưu</button>;
}

describe('QueryState', () => {
  it('shows the data once loaded', async () => {
    wrap(<Loader fn={() => Promise.resolve('Dữ liệu')} />);
    expect(await screen.findByText('Dữ liệu')).toBeInTheDocument();
  });

  it('says a feature is not connected yet, instead of showing a failure', async () => {
    wrap(<Loader fn={() => Promise.reject(new ApiError({ status: 404, code: 'request.not_found' }))} />);
    expect(await screen.findByText('Chức năng chưa được kết nối API')).toBeInTheDocument();
  });

  it('explains a missing permission', async () => {
    wrap(<Loader fn={() => Promise.reject(new ApiError({ status: 403, code: 'identity.forbidden' }))} />);
    expect(await screen.findByText('Bạn không có quyền xem nội dung này')).toBeInTheDocument();
  });

  it('offers a retry and shows the request ID for lookups', async () => {
    wrap(<Loader fn={() => Promise.reject(new ApiError({ status: 500, code: 'internal', requestId: 'req-9' }))} />);
    expect(await screen.findByRole('button', { name: 'Thử lại' })).toBeInTheDocument();
    expect(screen.getByText('req-9')).toBeInTheDocument();
  });
});

describe('useAction', () => {
  it('tells the user when an action succeeds', async () => {
    wrap(<Acting fn={() => Promise.resolve()} />);
    await userEvent.click(screen.getByRole('button', { name: 'Lưu' }));
    expect(await screen.findByText('Đã lưu.')).toBeInTheDocument();
  });

  it('tells the user when an action fails', async () => {
    wrap(<Acting fn={() => Promise.reject(new ApiError({ status: 409, code: 'rentals.insufficient_funds' }))} />);
    await userEvent.click(screen.getByRole('button', { name: 'Lưu' }));
    await waitFor(() => expect(screen.getByText('Chưa nhận đủ số tiền cần thiết.')).toBeInTheDocument());
  });
});
