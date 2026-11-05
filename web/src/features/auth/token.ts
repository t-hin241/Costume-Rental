// The token is read while rendering, and React may repeat a render or throw
// one away (StrictMode, a suspended render retried, siblings prerendered while
// another part loads). Reading therefore never consumes it: the first read
// keeps it for this page, and the address bar is cleaned after commit.
let kept: { path: string; token: string } | null = null;

/** Returns the one-time token of the current page's link, or null. */
export function fragmentToken(): string | null {
  const path = window.location.pathname;
  const token = new URLSearchParams(window.location.hash.slice(1)).get('token');
  if (token) kept = { path, token };
  // Leaving the page forgets the token: coming back later needs a new link.
  else if (kept?.path !== path) kept = null;
  return kept?.token ?? null;
}

/**
 * Removes the token from the address bar, so it does not stay in history or
 * get shared with the link. Call it from an effect, never while rendering.
 */
export function clearFragmentToken(): void {
  if (new URLSearchParams(window.location.hash.slice(1)).has('token')) {
    window.history.replaceState(null, '', window.location.pathname + window.location.search);
  }
}
