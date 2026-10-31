import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';
import { BagProvider, toBookingLines, useBag, type BagLine } from './bag';

const wrapper = ({ children }: { children: ReactNode }) => <BagProvider>{children}</BagProvider>;

const line: Omit<BagLine, 'key'> = {
  kind: 'ITEM', offering_id: 'i1', slug: 'i1', name: 'Áo', image: null, selection_label: 'Size M', variant_id: 'v1', quantity: 1,
};

describe('rental bag', () => {
  it('merges the same selection and keeps different sizes apart', () => {
    const { result } = renderHook(() => useBag(), { wrapper });
    act(() => result.current.addLine(line));
    act(() => result.current.addLine(line));
    act(() => result.current.addLine({ ...line, variant_id: 'v2' }));
    expect(result.current.lines).toHaveLength(2);
    expect(result.current.count).toBe(3);
  });

  it('treats bundle size picks as one selection regardless of key order', () => {
    const { result } = renderHook(() => useBag(), { wrapper });
    const bundle = { ...line, kind: 'BUNDLE' as const, variant_id: undefined };
    act(() => result.current.addLine({ ...bundle, selections: { top: 'a', pants: 'b' } }));
    act(() => result.current.addLine({ ...bundle, selections: { pants: 'b', top: 'a' } }));
    expect(result.current.lines).toHaveLength(1);
  });

  it('stores no prices, because totals only ever come from the quote API', () => {
    const { result } = renderHook(() => useBag(), { wrapper });
    act(() => result.current.addLine(line));
    expect(localStorage.getItem('webee.bag.v1')).not.toMatch(/price|amount|rent/i);
  });

  it('survives a corrupted saved bag', () => {
    localStorage.setItem('webee.bag.v1', '{not json');
    const { result } = renderHook(() => useBag(), { wrapper });
    expect(result.current.lines).toEqual([]);
  });

  it('sends only the selection to the booking API', () => {
    const { result } = renderHook(() => useBag(), { wrapper });
    act(() => result.current.addLine({ ...line, quantity: 2 }));
    expect(toBookingLines(result.current.lines)).toEqual([{ kind: 'ITEM', offering_id: 'i1', variant_id: 'v1', selections: undefined, quantity: 2 }]);
  });
});
