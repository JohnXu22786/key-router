import { describe, it, expect } from 'vitest';
import { usdToMicroUsd, microUsdToUsd } from './keyLimits';

describe('key limit unit conversions (USD <-> micro-USD)', () => {
  it('stores a $30 budget as 30,000,000 micro-USD — not 30', () => {
    // Regression: the key form sent the raw USD value, so a "$30" budget was
    // persisted as 30 micro-USD ($0.00003). The next relayed request pushed
    // total_spent past it and applySpendLimit disabled the key with
    // spend_limit_exhausted seconds after the user saved.
    expect(usdToMicroUsd(30)).toBe(30_000_000);
  });

  it('keeps 0 (no budget / unlimited) as 0', () => {
    expect(usdToMicroUsd(0)).toBe(0);
    expect(microUsdToUsd(0)).toBe(0);
  });

  it('converts fractional dollars', () => {
    expect(usdToMicroUsd(0.25)).toBe(250_000);
    expect(usdToMicroUsd(1.5)).toBe(1_500_000);
  });

  it('round-trips stored micro-USD back to USD for the form', () => {
    expect(microUsdToUsd(30_000_000)).toBe(30);
    expect(microUsdToUsd(123_456_789)).toBe(123.456789);
  });
});
