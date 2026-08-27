// Chatham DST tests run in their OWN file because they must run under
// Pacific/Chatham — UTC+13:45/+12:45, the only real zone whose DST shift is
// MISALIGNED with the hour-field grid. Its fall-back (Apr 5 2026, 03:45
// +13:45 -> 02:45 +12:45 at T = 2026-04-04T14:00:00Z) repeats the wall-clock
// span [02:45, 03:45), which CROSSES the 02/03 field boundary — unlike New
// York (02:00 -> 01:00) and Lord Howe (02:00 -> 01:30), whose repeated spans
// stay inside one field. The old hour-field-equality machinery modeled every
// repeat as field-aligned and got both Chatham rows wrong: row '03:00' got a
// contiguous 120-minute span (the +1h step crossed the transition and
// absorbed 15 minutes of row '02:00''s second pass) instead of its real 105
// minutes, and row '02:00' (no hour-field match) lost its second-pass slice
// entirely. The TZ override is applied via vi.stubEnv before any test parses
// a date, and vitest gives each test file its own worker, so the override
// never leaks into the other suites (which run in the machine's local zone,
// under America/New_York via activityShared.dst.test.ts, or under
// Australia/Lord_Howe via activityShared.dst.lordhowe.test.ts).
import { describe, it, expect, vi, beforeAll } from 'vitest';
import dayjs from 'dayjs';
import { bucketWindowShare, floorWindowUntil, rowCoverage, series, prorateBoundaryBuckets } from './activityShared';
import type { ActivityResponse } from '../api/client';

beforeAll(() => {
  vi.stubEnv('TZ', 'Pacific/Chatham');
});

// Fall-back night: Apr 5 2026, the clock jumps 03:45 +13:45 -> 02:45 +12:45
// at 2026-04-04T14:00:00Z. The repeated wall span [02:45, 03:45) (60 minutes
// shown twice) crosses the 02:00/03:00 field boundary, so the merged hourly
// rows' recorded extents are NON-CONTIGUOUS:
//   - row '02:00': [02:00, 03:00) +13:45 ([12:15Z, 13:15Z)) plus the
//     second-pass slice [02:45, 03:00) +12:45 ([14:00Z, 14:15Z)) — 75
//     minutes with a 45-minute gap (the gap displays row '03:00''s data).
//   - row '03:00': [03:00, 03:45) +13:45 ([13:15Z, 14:00Z)) plus
//     [03:00, 04:00) +12:45 ([14:15Z, 15:15Z), its second pass followed by
//     the once-only [03:45, 04:00) continuation) — 105 minutes with a
//     15-minute gap ([14:00Z, 14:15Z) is row '02:00''s data).
describe('rowCoverage — Pacific/Chatham 45-minute-offset fall-back', () => {
  // NOTE: every time must be parsed INSIDE the tests (a describe-level
  // dayjs() would parse in the machine's local zone, before the TZ stub).
  it('row 03:00 covers 105 minutes as two runs, not the contiguous 120', () => {
    // '2026-04-05T03:00:00' is ambiguous (03:00 displays twice); the JS Date
    // setter resolves it to the FIRST occurrence 13:15Z, exactly like the
    // New York suite's 01:00 rows.
    const c = rowCoverage(dayjs('2026-04-05T03:00:00'), 'hour', dayjs('2026-04-05T05:00:00'));
    expect(c.coverage).toBe(105);
    expect(c.runs).toEqual([
      { from: dayjs('2026-04-04T13:15:00.000Z').valueOf(), to: dayjs('2026-04-04T14:00:00.000Z').valueOf() },
      { from: dayjs('2026-04-04T14:15:00.000Z').valueOf(), to: dayjs('2026-04-04T15:15:00.000Z').valueOf() },
    ]);
    // The KPI proration over the gap-free window [13:15Z, 14:15Z) reads the
    // real 45 in-window minutes of 105 — the old contiguous model divided
    // 60 minutes of "coverage" by 120 (share 0.5 instead of 3/7).
    const share = bucketWindowShare(
      '2026-04-05T03:00:00',
      dayjs('2026-04-04T13:15:00.000Z'),
      dayjs('2026-04-04T14:15:00.000Z'),
      dayjs('2026-04-04T15:45:00.000Z'),
      'hour',
      false,
    );
    expect(share).toBeCloseTo(45 / 105, 10);
  });

  it('row 02:00 covers 75 minutes with its second-pass slice represented', () => {
    const c = rowCoverage(dayjs('2026-04-05T02:00:00'), 'hour', dayjs('2026-04-05T05:00:00'));
    expect(c.coverage).toBe(75);
    expect(c.runs).toEqual([
      { from: dayjs('2026-04-04T12:15:00.000Z').valueOf(), to: dayjs('2026-04-04T13:15:00.000Z').valueOf() },
      { from: dayjs('2026-04-04T14:00:00.000Z').valueOf(), to: dayjs('2026-04-04T14:15:00.000Z').valueOf() },
    ]);
  });

  it('keeps an unaffected early-hour row at 60 minutes', () => {
    // 01:00's first pass sits entirely before the repeated span [02:45,
    // 03:45): the wall-intersection test must not flag it even though a
    // transition exists within the 180-minute discovery window.
    const c = rowCoverage(dayjs('2026-04-05T01:00:00'), 'hour', dayjs('2026-04-05T05:00:00'));
    expect(c.coverage).toBe(60);
    expect(c.runs).toHaveLength(1);
  });

  it('keeps a constant-offset +12:45 day at 60 minutes (control)', () => {
    const c = rowCoverage(dayjs('2026-04-06T03:00:00'), 'hour', dayjs('2026-04-06T05:00:00'));
    expect(c.coverage).toBe(60);
  });
});

// The hour floor's second-pass branch rebuilds the containing-hour start
// from the transition (see findFallBackBefore in activityShared.ts). For
// the misaligned Chatham repeat that start is NOT first+1h: a second-pass
// until like 02:50 +12:45 (14:05Z) reconstructs wall 02:00:00, which exists
// only in the FIRST pass (12:15Z), and the old +1h correction landed on
// 13:15Z — one full hour before the true hour start 14:00Z (02:45:00
// +12:45, the instant the second pass begins; 02:00:00 +12:45 never
// occurs). The snapped window/live-cell boundary then sat an hour early and
// the live hour amputated the second occurrence's usage.
describe('floorWindowUntil hour branch — Chatham misaligned repeat', () => {
  it('floors a field-02 second-pass until to the transition (14:00Z, not 13:15Z)', () => {
    const until = dayjs('2026-04-04T14:05:00.000Z'); // 02:50 +12:45
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-04-04T14:00:00.000Z');
  });

  it('floors a field-03 second-pass until to its second-pass start (14:15Z)', () => {
    // 03:05 +12:45 = 14:20Z: the second pass of wall 03:00-04:00 begins at
    // 03:00 +12:45 = 14:15Z (which coincides with the +1h correction here —
    // the aligned half of the misaligned shape).
    const until = dayjs('2026-04-04T14:20:00.000Z'); // 03:05 +12:45
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-04-04T14:15:00.000Z');
  });

  it('keeps a first-pass instant on the first-pass grid (bit-identical)', () => {
    // 03:25:30 +13:45 (13:40:30Z): the reconstruction resolves to the first
    // occurrence 13:15Z with the same offset — no correction.
    const until = dayjs('2026-04-04T13:40:30.000Z');
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-04-04T13:15:00.000Z');
  });

  it('keeps a constant-offset +12:45 day on the :45 hour grid (bit-identical)', () => {
    // Apr 6 01:40:30 +12:45 = 2026-04-05T12:55:30Z: hour 01:00 starts at
    // 12:15:00Z, exactly where the reconstruction lands.
    const now = dayjs('2026-04-05T12:55:30.000Z');
    expect(floorWindowUntil(now, 'hour').toISOString()).toBe('2026-04-05T12:15:00.000Z');
  });
});

// series() on a sub-hour axis must split the two merged Chatham rows with
// the SAME run-based coverage as the KPI proration: the chart's fold
// iterates each row's runs (the gap between them — the other row's data —
// counts for neither) and divides by the same 105/75-minute coverage, so
// the chart total stays equal to bucketWindowShare's share for the same
// window and rows. Windows are past-shaped (cutoff well after `until`),
// passing liveExtend=false exactly like the DST suite.
describe('series — a window crossing the Chatham repeat keeps chart total == KPI', () => {
  const ROWS = [
    { hour_bucket: '2026-04-05T02:00:00', v: 750 },  // merged 02:00 row (75-minute coverage)
    { hour_bucket: '2026-04-05T03:00:00', v: 1050 }, // merged 03:00 row (105-minute coverage)
  ];

  it('min15 window crossing the transition from mid-first-pass', () => {
    // Window [13:15Z, 14:30Z) = [03:00 +13:45, 03:15 +12:45): crosses T and
    // cuts the second passes at 14:30Z. The axis holds 03:00, 03:15, 03:30
    // (the 02:45 and repeated-03:00 cells are sort-deduped off). Row 03
    // counts 45 + 15 = 60 of its 105 minutes; row 02 counts 15 of its 75 —
    // the 02:45..02:59 second-pass labels the axis cannot show are spread
    // over the visible cells (the fold's total-safety arm), so the totals
    // agree with the KPI.
    const since = dayjs('2026-04-04T13:15:00.000Z');
    const until = dayjs('2026-04-04T14:30:00.000Z');
    const cutoff = dayjs('2026-04-04T15:45:00.000Z');
    const out = series(ROWS, r => r.v, since, until, cutoff, 'min15', false);
    expect(out.map(p => p.value)).toEqual([350, 200, 200]);
    const kpi = ROWS.reduce((a, r) => a + r.v * bucketWindowShare(r.hour_bucket, since, until, cutoff, 'hour'), 0);
    expect(kpi).toBeCloseTo(750, 10);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(kpi, 10);
  });

  it('live min15 window ending on the second pass (the live-cell shape)', () => {
    // 45-minute window snapped to the FIXED floor: until = 14:00Z (02:45
    // +12:45), cutoff 14:03:30Z inside the live cell [14:00Z, 14:15Z). Row
    // 02's second-pass slice [14:00Z, 14:03Z) is REAL usage and folds onto
    // the live cell (its label 02:45 is a fresh tick here — no collision),
    // so the chart and the KPI both count 63/63 of the row's recorded
    // extent; row 03's whole 45 recorded minutes lie inside the window
    // (share 1).
    const since = dayjs('2026-04-04T13:15:00.000Z');
    const until = dayjs('2026-04-04T14:00:00.000Z');
    const cutoff = dayjs('2026-04-04T14:03:30.000Z');
    const out = series(ROWS, r => r.v, since, until, cutoff, 'min15');
    expect(out[0].value).toBeCloseTo(350, 10);
    expect(out[1].value).toBeCloseTo(350, 10);
    expect(out[2].value).toBeCloseTo(350, 10);
    expect(out[3].value).toBeCloseTo(750 / 21, 10);
    const kpi = ROWS.reduce((a, r) => a + r.v * bucketWindowShare(r.hour_bucket, since, until, cutoff, 'min15'), 0);
    expect(kpi).toBeCloseTo(1050 + 750 / 21, 10);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(kpi, 10);
  });
});

// The hour-granularity presets (1d/2d) snap `until` to the FIXED floor; the
// live hour — the hour starting AT the snapped until — then sits on the
// second pass (14:00Z) and must carry the row's second-pass recorded slice
// exactly like the New York and Lord Howe suites' live hours carry theirs.
// With the old floor (13:15Z) the live hour started one full hour early:
// the boundary lay before the second pass had begun and the newest 5
// recorded minutes read as a different bucket.
describe('bucketWindowShare — hour-granularity live window on the Chatham repeat', () => {
  it('shares the whole merged row when the live hour counts the second pass (share 1)', () => {
    // 1d preset at 02:50:30 +12:45 (14:05:30Z): until floors to 14:00Z, the
    // live hour [14:00Z, 15:00Z) holds the row's recorded slice [14:00Z,
    // 14:05Z) (row '02:00''s second pass, up to the fetch) on top of the
    // in-window [12:15Z, 13:15Z) — the full 65 recorded minutes of the
    // merged 02:00 row (75-minute coverage, 65 recorded).
    const now = dayjs('2026-04-04T14:05:30.000Z');
    const until = floorWindowUntil(now, 'hour');
    expect(until.toISOString()).toBe('2026-04-04T14:00:00.000Z');
    const since = floorWindowUntil(now.subtract(24, 'hour'), 'hour');
    expect(since.toISOString()).toBe('2026-04-03T13:15:00.000Z'); // wall 03:00 +13:45 the day before
    const share = bucketWindowShare('2026-04-05T02:00:00', since, until, now, 'hour');
    expect(share).toBeCloseTo(1, 10);
  });
});

// prorateBoundaryBuckets mirrors the Overview per-row proration on the
// server-bucketed custom-range responses. On the Chatham repeat the LAST
// bucket of a range ending mid-second-pass starts at the FIXED floor
// (14:00Z); boundaryShare must re-anchor that second-pass start to the
// merged row's first occurrence (12:15Z — 105 minutes earlier, which the
// old subtract(1,'hour') step could never reach) so it divides by the true
// 75-minute coverage and prorates the in-window slice the same way
// bucketWindowShare does.
describe('prorateBoundaryBuckets — Chatham boundary buckets', () => {
  const resp = (buckets: string[], values: number[]): ActivityResponse => ({
    metric: 'spend', group_by: 'model', rollup: 'hour',
    series: buckets.map((b, i) => ({ bucket: b, group: 'a', value: values[i], is_zero: false })),
    summary: [{ group: 'a', min: 60, max: 75, avg: 67, sum: 135, value: 75, percent: 100 }],
    buckets,
    totals: { spend: 135, tokens: 0, requests: 0, cache: 0 },
  });

  it('prorates the last bucket starting on the second pass by the merged row\'s coverage', () => {
    // Custom window [01:30 +13:45, 02:50 +12:45) = [11:45Z, 14:05Z). The
    // 01:00 bucket's row lies fully pre-jump: 30 of its 60 minutes are
    // inside. The 02:00 bucket starts at the FIXED floor 14:00Z (the second
    // pass begins at 02:45) and carries the merged 75-minute row; the
    // in-window slice is its whole first pass (60) plus the 5 recorded
    // second-pass minutes [14:00Z, 14:05Z): share 65/75.
    const since = dayjs('2026-04-04T11:45:00.000Z');
    const until = dayjs('2026-04-04T14:05:00.000Z');
    const cutoff = dayjs('2026-04-05T05:00:00'); // 15:45Z — after the whole repeat
    const buckets = ['2026-04-05 01:00', '2026-04-05 02:00'];
    const out = prorateBoundaryBuckets(resp(buckets, [60, 75]), since, until, until, cutoff, 'hour', 'hour');
    expect(out.series[0].value).toBeCloseTo(30, 10);   // 30/60 of the 01:00 row
    expect(out.series[1].value).toBeCloseTo(65, 10);   // 65/75 of the merged 02:00 row
    // The prorated response must agree with the Overview per-row proration.
    expect(bucketWindowShare('2026-04-05T01:00:00', since, until, cutoff, 'hour', false)).toBeCloseTo(30 / 60, 10);
    expect(bucketWindowShare('2026-04-05T02:00:00', since, until, cutoff, 'hour', false)).toBeCloseTo(65 / 75, 10);
  });
});

// Spring-forward night: Sep 27 2026, the clock jumps 02:45 +12:45 -> 03:45
// +13:45 at T = 2026-09-26T14:00:00Z. The skipped wall span [02:45, 03:45)
// (60 minutes of wall time that never display) crosses the 02/03 field
// boundary — the same misaligned shape as the Apr 5 fall-back, but
// symmetric: the post-gap wall hour 03:00 has only the 15-minute
// post-jump slice [03:45+13:45, 04:00+13:45) = [14:00Z, 14:15Z), and the
// pre-gap wall hour 02:00 has its full 60-minute pre-jump slice
// [02:00+12:45, 03:00+12:45) = [13:15Z, 14:15Z) split by the 15-min
// post-jump continuation [02:45+13:45, 03:00+13:45) = [14:00Z, 14:15Z) at
// the higher offset. These tests pin the hour floor — the symmetric twin
// of the fall-back floor — on the post-gap hour containing `until`.
// The fall-back branch's `first.utcOffset() > until.utcOffset()` check is
// INERT here: V8 rolls the non-existent `03:00+13:45` forward to the
// first valid post-jump time (04:00+13:45 = 14:15Z), giving first a
// POST-jump offset equal to until's, so neither offset comparison flags
// the spring-forward. The spring-forward branch in floorWindowUntil
// walks back from first to find T and rebuilds the floor from the
// transition — `T + max(0, field - τ+)` with `field = until.hour() * 60`
// (until is a real post-jump instant, its wall hour is unambiguous).
describe('floorWindowUntil hour branch — Chatham 45-minute-offset spring-forward', () => {
  it('floors a post-gap until to the transition (14:00Z, not 14:15Z)', () => {
    // 03:50:30 +13:45 = 14:05:30Z. The wall hour 03:00+13:45 begins at the
    // transition T = 14:00Z (the pre-jump 03:45+12:45 = T is the last
    // instant of the 02:00 hour's first pass; the post-jump 03:45+13:45 =
    // T starts the 03:00 hour's 15-min post-jump slice [14:00Z, 14:15Z)).
    // The old minute subtraction floored 14:05:30Z to 14:15Z — the V8
    // resolution of non-existent `03:00+13:45` rolled forward by the gap's
    // 60 min to `04:00+13:45`, then zeroed sec/ms. That cell was an hour
    // late and the live hour read as 5 min of 0.
    const until = dayjs('2026-09-26T14:05:30.000Z');
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-09-26T14:00:00.000Z');
  });

  it('floors every instant in [14:00Z, 14:15Z) to the transition (14:00Z)', () => {
    // The post-gap wall hour's 15-min post-jump slice. Every instant in it
    // shares the SAME containing-hour start: the transition T, not T+15min.
    // A 1d preset at any of these times lands the live hour on
    // [14:00Z, 15:00Z) and the live cell carries the full post-jump slice.
    for (const iso of [
      '2026-09-26T14:00:00.000Z',
      '2026-09-26T14:00:01.000Z',
      '2026-09-26T14:05:30.000Z',
      '2026-09-26T14:10:00.000Z',
      '2026-09-26T14:14:59.000Z',
    ]) {
      expect(floorWindowUntil(dayjs(iso), 'hour').toISOString()).toBe('2026-09-26T14:00:00.000Z');
    }
    // 14:15:00Z is the start of the 04:00 wall hour (+13:45) — its
    // post-jump slice begins at the transition + 15min.
    expect(floorWindowUntil(dayjs('2026-09-26T14:15:00.000Z'), 'hour').toISOString()).toBe('2026-09-26T14:15:00.000Z');
  });

  it('keeps a pre-jump instant on the pre-jump hour grid (bit-identical)', () => {
    // 02:40 +12:45 = 13:25:30Z. The standard minute subtraction lands on
    // 02:00+12:45 = 13:15Z — no spring-forward correction needed.
    const until = dayjs('2026-09-26T13:25:30.000Z');
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-09-26T13:15:00.000Z');
  });

  it('keeps a constant-offset +13:45 day on the :45 hour grid (bit-identical)', () => {
    // Sep 28 01:40:30 +13:45 = 11:55:30Z. Post-spring-forward + constant
    // offset day: hour 01:00 starts at 11:15:00Z, exactly where the
    // reconstruction lands. Control day pins the unambiguous behavior.
    const now = dayjs('2026-09-27T11:55:30.000Z');
    expect(floorWindowUntil(now, 'hour').toISOString()).toBe('2026-09-27T11:15:00.000Z');
  });
});

// 1d/2d presets (hour granularity) snap `until` to the FIXED floor; the
// live cell at the floor holds the post-jump recorded slice. The old
// floor (14:15Z) sat inside the post-gap hour, the live cell sat on the
// 14:15Z-15:00Z tail, and the row's 5 recorded post-jump minutes vanished
// from the KPI. The fixed floor lands the live cell on [14:00Z, 15:00Z),
// the row's whole 60-min pre-jump pass lies inside the window, and the
// 5 min of post-jump data count on top — share 1, exactly like the
// fall-back case in the suite above.
describe('bucketWindowShare — hour-granularity live window on the Chatham spring', () => {
  it('shares the whole 02:00 row when the live hour starts at the transition (share 1)', () => {
    // 1d preset at 03:50:30 +13:45 (14:05:30Z): until floors to 14:00Z, the
    // live hour [14:00Z, 15:00Z) carries the row's pre-jump in-window
    // slice [13:15Z, 14:00Z) = 45 min PLUS the post-jump continuation
    // [14:00Z, 14:05:30Z) = 5 min — share 1 of the 60-min row.
    const now = dayjs('2026-09-26T14:05:30.000Z');
    const until = floorWindowUntil(now, 'hour');
    expect(until.toISOString()).toBe('2026-09-26T14:00:00.000Z');
    const since = floorWindowUntil(now.subtract(24, 'hour'), 'hour');
    const share = bucketWindowShare('2026-09-27T02:00:00', since, until, now, 'hour');
    expect(share).toBeCloseTo(1, 10);
  });

  it('keeps a constant-offset +13:45 day live window at share 1 (control)', () => {
    // Sep 28 01:50:30 +13:45 = 12:05:30Z. The row's whole 50 recorded
    // minutes (cutoff 12:05:30) lie inside the live hour [11:15Z, 12:15Z)
    // — share 1, bit-identical with the old floor.
    const now = dayjs('2026-09-27T12:05:30.000Z');
    const until = floorWindowUntil(now, 'hour');
    const since = floorWindowUntil(now.subtract(24, 'hour'), 'hour');
    const share = bucketWindowShare('2026-09-28T01:00:00', since, until, now, 'hour');
    expect(share).toBeCloseTo(1, 10);
  });
});

// series() on a min15 axis must keep chart total == KPI for a window that
// crosses the Chatham spring transition. Uses rows with KNOWN coverage
// (the 02:00 row's full 60-min pre-jump + 15-min post-jump pass; the
// 04:00 row's normal 60-min post-jump) and a past-shaped window
// (cutoff well after `until`, liveExtend=false like the fall-back
// series tests) so the chart and the KPI read the same window and rows.
// The 03:00 row is excluded — its real 15-min coverage sits inside the
// spring gap and the rowCoverage fall-back machinery doesn't model it;
// a future spring-forward coverage fix is out of scope here. Including
// only the unambiguously-shaped rows keeps the parity assertion focused
// on the hour floor: with the fixed floor the live cell (when used)
// carries the post-jump slice the way the fall-back live cell does, and
// the chart and KPI count the same coverage for the same window.
describe('series — a window crossing the Chatham spring keeps chart total == KPI', () => {
  const ROWS = [
    { hour_bucket: '2026-09-27T02:00:00', v: 600 },  // 60-min coverage (pre-jump 45min + post-jump 15min, contiguous)
    { hour_bucket: '2026-09-27T04:00:00', v: 60 },   // 60-min coverage, post-jump
  ];

  it('min15 past window crossing the transition (chart total matches KPI)', () => {
    // Window [13:15Z, 14:30Z): 02:00 row's whole 60-min coverage lies
    // inside the window (share 1); 04:00 row contributes its first 15
    // min [14:15Z, 14:30Z) of the post-jump hour (share 0.25). KPI =
    // 600 + 15 = 615. The min15 axis is {13:15, 13:30, 13:45, 14:00,
    // 14:15} (5 cells — 14:30 starts AT until and is excluded by the
    // half-open window). The chart distributes the 02:00 row's 60 min
    // evenly over the first 4 cells (15 min each, 0.25 each) and the
    // 04:00 row's 15 min over 14:15 (0.25). Total = 4*150 + 15 = 615.
    const since = dayjs('2026-09-26T13:15:00.000Z');
    const until = dayjs('2026-09-26T14:30:00.000Z');
    const cutoff = dayjs('2026-09-26T15:45:00.000Z');
    const out = series(ROWS, r => r.v, since, until, cutoff, 'min15', false);
    const kpi = ROWS.reduce((a, r) => a + r.v * bucketWindowShare(r.hour_bucket, since, until, cutoff, 'hour', false), 0);
    expect(kpi).toBeCloseTo(615, 10);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(kpi, 10);
    // The exact axis values for clarity (mirrors the fall-back series
    // test's `expect(out.map(p => p.value)).toEqual([...])` shape).
    expect(out.map(p => p.value)).toEqual([150, 150, 150, 150, 15]);
  });
});