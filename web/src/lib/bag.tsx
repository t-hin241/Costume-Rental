import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import type { BookingLineInput, Image, OfferingKind } from './api/types';

// The rental bag is a per-browser convenience: only what the customer picked.
// It holds no prices, because every total comes from the quote API.

export interface BagLine {
  key: string;
  kind: OfferingKind;
  offering_id: string;
  slug: string;
  name: string;
  image: Image | null;
  selection_label: string;
  variant_id?: string;
  selections?: Record<string, string>;
  quantity: number;
}

export interface BagSchedule {
  package_id: string;
  pickup_date: string;
  slot_id: string;
}

interface BagState {
  lines: BagLine[];
  schedule: Partial<BagSchedule>;
}

interface BagValue extends BagState {
  count: number;
  addLine: (line: Omit<BagLine, 'key'>) => void;
  setQuantity: (key: string, quantity: number) => void;
  removeLine: (key: string) => void;
  setSchedule: (schedule: Partial<BagSchedule>) => void;
  clear: () => void;
}

const storageKey = 'webee.bag.v1';
const empty: BagState = { lines: [], schedule: {} };

function load(): BagState {
  try {
    const raw = localStorage.getItem(storageKey);
    if (!raw) return empty;
    const parsed = JSON.parse(raw) as BagState;
    return Array.isArray(parsed.lines) ? { lines: parsed.lines, schedule: parsed.schedule ?? {} } : empty;
  } catch {
    return empty;
  }
}

function lineKey(l: Omit<BagLine, 'key'>): string {
  const picks = Object.entries(l.selections ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return JSON.stringify([l.kind, l.offering_id, l.variant_id ?? '', picks]);
}

const BagContext = createContext<BagValue | null>(null);

export function BagProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<BagState>(load);

  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(state));
    } catch {
      // Private windows may refuse storage; the bag then lasts for this visit.
    }
  }, [state]);

  const addLine = useCallback((line: Omit<BagLine, 'key'>) => {
    const key = lineKey(line);
    setState((s) => {
      const existing = s.lines.find((l) => l.key === key);
      const lines = existing
        ? s.lines.map((l) => (l.key === key ? { ...l, quantity: l.quantity + line.quantity } : l))
        : [...s.lines, { ...line, key }];
      return { ...s, lines };
    });
  }, []);

  const setQuantity = useCallback((key: string, quantity: number) => {
    setState((s) => ({ ...s, lines: s.lines.map((l) => (l.key === key ? { ...l, quantity } : l)) }));
  }, []);

  const removeLine = useCallback((key: string) => {
    setState((s) => ({ ...s, lines: s.lines.filter((l) => l.key !== key) }));
  }, []);

  const setSchedule = useCallback((schedule: Partial<BagSchedule>) => {
    setState((s) => ({ ...s, schedule: { ...s.schedule, ...schedule } }));
  }, []);

  const clear = useCallback(() => setState(empty), []);

  const value = useMemo<BagValue>(() => ({
    ...state,
    count: state.lines.reduce((n, l) => n + l.quantity, 0),
    addLine, setQuantity, removeLine, setSchedule, clear,
  }), [state, addLine, setQuantity, removeLine, setSchedule, clear]);

  return <BagContext.Provider value={value}>{children}</BagContext.Provider>;
}

export function useBag(): BagValue {
  const value = useContext(BagContext);
  if (!value) throw new Error('useBag must be used inside BagProvider');
  return value;
}

export function toBookingLines(lines: BagLine[]): BookingLineInput[] {
  return lines.map((l) => ({
    kind: l.kind,
    offering_id: l.offering_id,
    variant_id: l.variant_id,
    selections: l.selections,
    quantity: l.quantity,
  }));
}
