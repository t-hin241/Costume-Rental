import { messageForCode } from './errors';

const baseURL: string = import.meta.env.VITE_API_BASE_URL ?? '';
const requestTimeoutMs = 20_000;

export interface FieldError {
  field: string;
  reason: string;
}

interface ErrorBody {
  error?: { code?: string; message?: string; fields?: FieldError[] };
  request_id?: string;
}

/** A failed API call, carrying the backend's stable error code. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: FieldError[];
  readonly requestId: string | null;
  readonly retryAfterSeconds: number | null;

  constructor(init: {
    status: number;
    code: string;
    fields?: FieldError[];
    requestId?: string | null;
    retryAfterSeconds?: number | null;
  }) {
    super(messageForCode(init.code));
    this.name = 'ApiError';
    this.status = init.status;
    this.code = init.code;
    this.fields = init.fields ?? [];
    this.requestId = init.requestId ?? null;
    this.retryAfterSeconds = init.retryAfterSeconds ?? null;
  }

  /** The endpoint is not served yet: see web/API_CONTRACT.md. */
  get endpointMissing(): boolean {
    return this.code === 'request.not_found';
  }

  get unauthenticated(): boolean {
    return this.status === 401;
  }
}

type Query = Record<string, string | number | boolean | null | undefined>;

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE';
  body?: unknown;
  query?: Query;
  signal?: AbortSignal;
}

function buildURL(path: string, query?: Query): string {
  if (!query) return baseURL + path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== '') params.set(key, String(value));
  }
  const qs = params.toString();
  return baseURL + path + (qs ? `?${qs}` : '');
}

async function parseError(res: Response): Promise<ApiError> {
  let body: ErrorBody = {};
  try {
    body = (await res.json()) as ErrorBody;
  } catch {
    // A proxy or gateway answered with something other than the JSON error format.
  }
  const retryAfter = Number(res.headers.get('Retry-After'));
  return new ApiError({
    status: res.status,
    code: body.error?.code ?? (res.status >= 500 ? 'internal' : 'request.failed'),
    fields: body.error?.fields,
    requestId: body.request_id ?? res.headers.get('X-Request-ID'),
    retryAfterSeconds: Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : null,
  });
}

/** Calls the API under /api/v1 with the session cookie. */
export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, query, signal } = options;
  const timeout = AbortSignal.timeout(requestTimeoutMs);
  let res: Response;
  try {
    res = await fetch(buildURL(`/api/v1${path}`, query), {
      method,
      credentials: 'include',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
    });
  } catch (err) {
    if (signal?.aborted) throw err;
    throw new ApiError({ status: 0, code: 'request.network' });
  }
  if (!res.ok) throw await parseError(res);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}
