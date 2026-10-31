import { ApiError } from '../../lib/api/client';

/** Allows only web addresses, so a bad value can never run as `javascript:`. */
export function checkoutTarget(url: string): string {
  let parsed: URL;
  try {
    parsed = new URL(url, window.location.origin);
  } catch {
    throw new ApiError({ status: 0, code: 'payments.invalid_checkout_url' });
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
    throw new ApiError({ status: 0, code: 'payments.invalid_checkout_url' });
  }
  return parsed.href;
}
