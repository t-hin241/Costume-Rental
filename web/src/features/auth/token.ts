/**
 * Reads the one-time token from the URL fragment and removes it from the
 * address bar, so it does not stay in history or get shared with the link.
 */
export function takeFragmentToken(): string | null {
  const token = new URLSearchParams(window.location.hash.slice(1)).get('token');
  if (token) window.history.replaceState(null, '', window.location.pathname + window.location.search);
  return token;
}
