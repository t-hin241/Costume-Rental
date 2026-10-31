import { useCallback, useEffect, useRef, useState } from 'react';

/** Delays a fast-changing value, so typing in a search box sends one request. */
export function useDebounced<T>(value: T, ms = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

/**
 * Keeps one idempotency key per input: a retry after a lost response repeats
 * the same request, and a changed input counts as a new one.
 */
export function useRequestKey(): (input: unknown) => string {
  const current = useRef({ input: '', key: '' });
  return useCallback((input: unknown) => {
    const json = JSON.stringify(input);
    if (current.current.input !== json) current.current = { input: json, key: crypto.randomUUID() };
    return current.current.key;
  }, []);
}
