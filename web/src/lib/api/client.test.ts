import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api } from './client';
import { fieldReason, messageForCode } from './errors';

function stubFetch(res: Response | Error) {
  const fn = vi.fn(() => (res instanceof Error ? Promise.reject(res) : Promise.resolve(res)));
  vi.stubGlobal('fetch', fn);
  return fn;
}

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

async function failure(call: Promise<unknown>): Promise<ApiError> {
  const err = await call.then(() => null, (e: unknown) => e);
  if (!(err instanceof ApiError)) throw new Error('expected an ApiError');
  return err;
}

afterEach(() => vi.unstubAllGlobals());

describe('api', () => {
  it('sends the session cookie and JSON, and never sends a token header', async () => {
    const fn = stubFetch(jsonResponse(200, { ok: true }));
    await api('/auth/login', { method: 'POST', body: { email: 'a@b.c' } });
    const [url, init] = fn.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/v1/auth/login');
    expect(init.credentials).toBe('include');
    expect(init.headers).toEqual({ 'Content-Type': 'application/json' });
    expect(init.body).toBe('{"email":"a@b.c"}');
  });

  it('skips empty query values', async () => {
    const fn = stubFetch(jsonResponse(200, {}));
    await api('/bookings', { query: { q: '', status: 'RENTED', page: 2, x: undefined } });
    expect((fn.mock.calls[0] as unknown as [string])[0]).toBe('/api/v1/bookings?status=RENTED&page=2');
  });

  it('handles 204 without a body', async () => {
    stubFetch(new Response(null, { status: 204 }));
    await expect(api('/auth/logout', { method: 'POST' })).resolves.toBeUndefined();
  });

  it('maps the uniform error format to a Vietnamese message', async () => {
    stubFetch(jsonResponse(409, { error: { code: 'inventory.insufficient', message: 'not enough assets' }, request_id: 'req-1' }));
    const err = await failure(api('/bookings', { method: 'POST', body: {} }));
    expect(err.code).toBe('inventory.insufficient');
    expect(err.message).toBe('Không còn đủ hàng trong thời gian đã chọn.');
    expect(err.requestId).toBe('req-1');
  });

  it('reads Retry-After on rate limits', async () => {
    stubFetch(jsonResponse(429, { error: { code: 'request.rate_limited' } }, { 'Retry-After': '30' }));
    expect((await failure(api('/auth/login', { method: 'POST', body: {} }))).retryAfterSeconds).toBe(30);
  });

  it('recognises an endpoint the backend does not serve yet', async () => {
    stubFetch(jsonResponse(404, { error: { code: 'request.not_found' } }));
    expect((await failure(api('/catalog/offerings'))).endpointMissing).toBe(true);
  });

  it('survives a non-JSON error from a proxy', async () => {
    stubFetch(new Response('<html>Bad gateway</html>', { status: 502 }));
    expect((await failure(api('/x'))).code).toBe('internal');
  });

  it('reports network failures as an ApiError', async () => {
    stubFetch(new TypeError('Failed to fetch'));
    expect((await failure(api('/x'))).code).toBe('request.network');
  });
});

describe('messages', () => {
  it('falls back for codes it does not know', () => {
    expect(messageForCode('something.new')).toBe('Không thể thực hiện thao tác. Vui lòng thử lại.');
  });

  it('keeps limits that come from server configuration', () => {
    expect(fieldReason('must be 12 to 128 characters')).toBe('Cần từ 12 đến 128 ký tự.');
    expect(fieldReason('required, at most 500 characters')).toBe('Nhập tối đa 500 ký tự.');
    expect(fieldReason('some unknown reason')).toBe('Giá trị chưa hợp lệ.');
  });
});
