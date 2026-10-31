import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { storeApi } from '../../lib/api/endpoints';
import type { StoreCalendar } from '../../lib/api/types';
import { StoreCalendarPicker } from './shared';

function calendar(from: string, to: string): StoreCalendar {
  const days = [];
  for (let d = new Date(`${from}T00:00:00Z`); d <= new Date(`${to}T00:00:00Z`); d.setUTCDate(d.getUTCDate() + 1)) {
    const date = d.toISOString().slice(0, 10);
    const open = date >= '2026-11-02' && !date.endsWith('-15');
    days.push({ date, open, reason: date.endsWith('-15') ? 'Nghỉ lễ' : null, slots: [] });
  }
  return { timezone: 'Asia/Ho_Chi_Minh', min_date: '2026-11-02', max_date: '2027-01-29', days };
}

function setup(onChange = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter><StoreCalendarPicker onChange={onChange} /></MemoryRouter>
    </QueryClientProvider>,
  );
  return onChange;
}

beforeEach(() => {
  vi.useFakeTimers({ now: new Date('2026-10-31T05:00:00Z'), toFake: ['Date'] });
  vi.spyOn(storeApi, 'calendar').mockImplementation((from, to) => Promise.resolve(calendar(from, to)));
});

describe('StoreCalendarPicker', () => {
  it('skips a month with no bookable days', async () => {
    setup();
    expect(await screen.findByText(/tháng 11/i)).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Ngày 3' })).toBeEnabled();
    vi.useRealTimers();
  });

  it('takes closed days from the API and explains them', async () => {
    const onChange = setup();
    await screen.findByText(/tháng 11/i);
    const closed = await screen.findByRole('button', { name: 'Ngày 15, không nhận hàng: Nghỉ lễ' });
    expect(closed).toBeDisabled();
    vi.useRealTimers();
    await userEvent.click(screen.getByRole('button', { name: 'Ngày 3' }));
    expect(onChange).toHaveBeenCalledWith('2026-11-03');
  });

  it('stops at the booking horizon the API allows', async () => {
    setup();
    await screen.findByText(/tháng 11/i);
    await screen.findByRole('button', { name: 'Ngày 3' });
    expect(screen.getByRole('button', { name: 'Tháng trước' })).toBeDisabled();
    vi.useRealTimers();
  });
});
