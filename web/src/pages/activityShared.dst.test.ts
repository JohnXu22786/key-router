// DST coverage tests live in their OWN file because they must run under
// America/New_York: the fall-back repeat only exists in zones with DST.
// The TZ override is applied via vi.stubEnv before any test parses a date,
// and vitest gives each test file its own worker, so the override never
// leaks into the other suites (which run in the machine's local zone).
import { describe, it, expect, vi, beforeAll } from 'vitest';
import dayjs from 'dayjs';
import { bucketWindowShare } from './activityShared';

beforeAll(() => {
  vi.stubEnv('TZ', 'America/New_York');
});

// Fall-back night: Nov 1 2026, 02:00 EDT -> 01:00 EST. The wall-clock hour
// 01:00 spans two elapsed hours and BOTH occurrences truncate to the same
// hourly row (the backend truncates to the local hour), so the row's
// recorded value covers 120 minutes. A window inside it must divide by 120
// — dividing by 60 would inflate the share up to 2x. Spring-forward skips
// a wall-clock hour entirely (no row exists for it), which stays 60.
describe('bucketWindowShare — DST fall-back', () => {
  it('divides by the repeated hour’s 120-minute coverage', () => {
    // 01:00 EDT (05:00Z) .. 01:30 EDT = 30 elapsed minutes of a 120-minute row.
    const share = bucketWindowShare(
      '2026-11-01T01:00:00',
      dayjs('2026-11-01T01:00:00'),
      dayjs('2026-11-01T01:30:00'),
      dayjs('2026-11-01T03:00:00'),
      'hour',
    );
    expect(share).toBeCloseTo(30 / 120, 10);
  });

  it('keeps a normal hour at 60-minute coverage', () => {
    const share = bucketWindowShare(
      '2026-10-31T01:00:00',
      dayjs('2026-10-31T01:00:00'),
      dayjs('2026-10-31T01:30:00'),
      dayjs('2026-10-31T03:00:00'),
      'hour',
    );
    expect(share).toBeCloseTo(0.5, 10);
  });

  it('clamps the coverage at the fetch time inside the repeat', () => {
    // cutoff = 01:45 EDT (05:45Z): the row's recorded value covers 45
    // elapsed minutes; the whole window lies inside them, so share = 1.
    const share = bucketWindowShare(
      '2026-11-01T01:00:00',
      dayjs('2026-11-01T01:00:00'),
      dayjs('2026-11-01T01:45:00'),
      dayjs('2026-11-01T01:45:00'),
      'hour',
    );
    expect(share).toBeCloseTo(1, 10);
  });

  it('keeps a 15m rolling window spanning the repeat at its true share', () => {
    // Window [01:50 EDT .. 02:05 EST] crosses the repeat: 70 elapsed
    // minutes of the 120-minute row.
    const share = bucketWindowShare(
      '2026-11-01T01:00:00',
      dayjs('2026-11-01T01:50:00'),
      dayjs('2026-11-01T02:05:00'),
      dayjs('2026-11-01T03:00:00'),
      'hour',
    );
    expect(share).toBeCloseTo(70 / 120, 10);
  });
});
