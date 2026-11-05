import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { StrictMode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';
import { ToastProvider } from '../../components/feedback';
import ResetPassword from './ResetPassword';
import { clearFragmentToken, fragmentToken } from './token';

function openLink(url: string) {
  window.history.replaceState(null, '', url);
}

function page() {
  return (
    <StrictMode>
      <QueryClientProvider client={new QueryClient()}>
        <ToastProvider>
          <MemoryRouter><ResetPassword /></MemoryRouter>
        </ToastProvider>
      </QueryClientProvider>
    </StrictMode>
  );
}

beforeEach(() => openLink('/'));

describe('link token', () => {
  it('can be read again after the address bar is cleaned', () => {
    openLink('/reset-password#token=abc');
    expect(fragmentToken()).toBe('abc');
    clearFragmentToken();
    expect(window.location.hash).toBe('');
    expect(fragmentToken()).toBe('abc');
  });

  it('belongs to the page it came with', () => {
    openLink('/verify-email#token=one');
    expect(fragmentToken()).toBe('one');
    openLink('/reset-password');
    expect(fragmentToken()).toBeNull();
  });

  it('keeps the search part when cleaned', () => {
    openLink('/reset-password?from=mail#token=abc');
    clearFragmentToken();
    expect(window.location.pathname + window.location.search + window.location.hash).toBe('/reset-password?from=mail');
  });
});

describe('reset password page', () => {
  it('keeps the form when React renders it more than once', () => {
    openLink('/reset-password#token=from-email');
    const first = render(page());
    expect(screen.queryByText('Liên kết không hợp lệ')).toBeNull();
    expect(window.location.hash).toBe('');
    // Mounted again, as when a render is thrown away and retried.
    first.unmount();
    render(page());
    expect(screen.queryByText('Liên kết không hợp lệ')).toBeNull();
  });

  it('says the link is invalid when there is no token', () => {
    openLink('/somewhere-else');
    fragmentToken();
    openLink('/reset-password');
    render(page());
    expect(screen.getByText('Liên kết không hợp lệ')).toBeTruthy();
  });
});
