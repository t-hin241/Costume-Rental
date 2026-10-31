import { describe, expect, it } from 'vitest';
import { niceScale } from './Charts';

describe('niceScale', () => {
  it('gives round ticks that cover the data', () => {
    expect(niceScale(2_400_000)).toEqual({ top: 3_000_000, step: 1_000_000 });
    expect(niceScale(450_000)).toEqual({ top: 600_000, step: 200_000 });
    expect(niceScale(1_000)).toEqual({ top: 1_000, step: 250 });
  });

  it('never divides by zero on an empty range', () => {
    expect(niceScale(0)).toEqual({ top: 1, step: 1 });
  });
});
