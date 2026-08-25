// DST coverage tests live in their OWN file because they must run under
// America/New_York: the fall-back repeat only exists in zones with DST.
// The TZ override is applied via vi.stubEnv before any test parses a date,
// and vitest gives each test file its own worker, so the override never
// leaks into the other suites (which run in the machine's local zone).
import { describe, it, expect, vi, beforeAll } from 'vitest';
import dayjs from 'dayjs';
import { bucketWindowShare, bucketAxis } from './activityShared';

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

// bucketStarts hosts the DST repeat-dedup guard (sort > prevSort) inside the
// loop this fix changed to the half-open window condition — these tests pin
// the guard down under the new condition.
describe('bucketStarts — DST axes under the half-open window', () => {
  it('fall-back: keeps the FIRST occurrence of the repeated hour, drops the repeat and the boundary bucket', () => {
    // [00:00 EDT, 03:00 EST) spans 3 elapsed hours; 01:00 wall-clock occurs
    // twice. The axis must hold exactly 00:00, 01:00 (first), 02:00 — no
    // repeated 01:00 tick, and no 03:00 bucket (starts AT until).
    const since = dayjs('2026-11-01T00:00:00');
    const until = dayjs('2026-11-01T03:00:00');
    const out = bucketAxis(since, until, 'hour');
    expect(out.map(p => p.sort)).toEqual([
      '2026-11-01 00:00', '2026-11-01 01:00', '2026-11-01 02:00',
    ]);
  });

  it('spring-forward: skips the non-existent hour and excludes the boundary bucket', () => {
    // 02:00 does not exist on 2026-03-08 (EST -> EDT). [00:00, 03:00 EDT)
    // covers 00:00 and 01:00; the 03:00 bucket starts AT until (zero
    // overlap) and is excluded.
    const since = dayjs('2026-03-08T00:00:00');
    const until = dayjs('2026-03-08T03:00:00');
    const out = bucketAxis(since, until, 'hour');
    expect(out.map(p => p.sort)).toEqual(['2026-03-08 00:00', '2026-03-08 01:00']);
  });

  it('minute axis over the fall-back repeat keeps strictly increasing sort keys', () => {
    // The repeated 01:00..01:59 wall-clock walk is deduped off the axis; the
    // surviving keys must still strictly increase.
    const since = dayjs('2026-11-01T01:55:00');
    const until = dayjs('2026-11-01T02:05:00');
    const sorts = bucketAxis(since, until, 'minute').map(p => p.sort);
    expect(sorts).toEqual([...new Set(sorts)]);
    expect(sorts.every((s, i) => i === 0 || s > sorts[i - 1])).toBe(true);
  });
});
