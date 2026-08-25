// DST coverage tests live in their OWN file because they must run under
// America/New_York: the fall-back repeat only exists in zones with DST.
// The TZ override is applied via vi.stubEnv before any test parses a date,
// and vitest gives each test file its own worker, so the override never
// leaks into the other suites (which run in the machine's local zone).
import { describe, it, expect, vi, beforeAll } from 'vitest';
import dayjs from 'dayjs';
import { bucketWindowShare, bucketAxis, series } from './activityShared';

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

// series() on a sub-hour axis must split the repeated row with the SAME real
// coverage as the KPI proration: a minute/min15 axis crossing the fall-back
// repeat divides by the row's true 120-minute coverage, never 60 — a
// hard-coded 60 would double every tick's value and inflate the chart total
// away from bucketWindowShare's proration for the same window and rows.
// These windows are past-shaped by design (their cutoff lies well AFTER
// `until`, like a previous period's fetch) and pin the coverage math, not
// the live-cell append — so the series() calls below pass liveExtend=false
// exactly like the pages do for past windows; with the live cell's
// alignment now judged from the range's own until (see hasLiveCell), the
// old default-gated shapes would otherwise append a boundary cell —
// VALUED where the row's coverage extends past `until` (the KPI's
// hour-granular mirror cannot count it, breaking chart-total == KPI
// conservation) or EMPTY where the coverage ends AT `until` (a trailing
// zero tick breaking the exact-shape assertions).
describe('series — DST fall-back on sub-hour axes', () => {
  const ROW = '2026-11-01T01:00:00'; // 01:00 EDT -> 01:00 EST: covers 120 elapsed minutes
  // NOTE: cutoff must be parsed INSIDE each test — a describe-level
  // dayjs() would parse in the machine's local zone, before the TZ stub.

  it('spreads the repeated hour over its real 120-minute coverage on a minute axis', () => {
    // Window [01:00 EDT .. 01:30 EDT): 30 elapsed minutes of the 120-minute
    // row. Each minute bucket gets value/120 (a divide by 60 would show
    // value/60 = 2x per tick), and the total matches the KPI proration. The
    // axis holds 01:00..01:29 — the bucket starting AT until is excluded by
    // the half-open window.
    const cutoff = dayjs('2026-11-01T03:00:00');
    const out = series(
      [{ hour_bucket: ROW }],
      () => 1200,
      dayjs('2026-11-01T01:00:00'), dayjs('2026-11-01T01:30:00'), cutoff, 'minute', false,
    );
    expect(out.map(p => p.value)).toEqual([...Array(30).fill(10)]);
    const kpi = bucketWindowShare(ROW, dayjs('2026-11-01T01:00:00'), dayjs('2026-11-01T01:30:00'), cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
  });

  it('spreads the repeated hour over its real 120-minute coverage on a min15 axis', () => {
    // Window [01:00 EDT .. 01:45 EDT): 45 elapsed minutes; 15-minute cells
    // get value * 15/120 each, and the total matches the KPI proration.
    const cutoff = dayjs('2026-11-01T03:00:00');
    const out = series(
      [{ hour_bucket: ROW }],
      () => 1200,
      dayjs('2026-11-01T01:00:00'), dayjs('2026-11-01T01:45:00'), cutoff, 'min15', false,
    );
    expect(out.map(p => p.value)).toEqual([150, 150, 150]);
    const kpi = bucketWindowShare(ROW, dayjs('2026-11-01T01:00:00'), dayjs('2026-11-01T01:45:00'), cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
  });

  it('keeps a normal single-hour row at 60-minute coverage (unchanged)', () => {
    // The day before fall-back: the walk must not trigger, so the row is
    // still split over exactly 60 minutes.
    const out = series(
      [{ hour_bucket: '2026-10-31T01:00:00' }],
      () => 600,
      dayjs('2026-10-31T01:00:00'), dayjs('2026-10-31T01:30:00'), dayjs('2026-10-31T03:00:00'), 'minute', false,
    );
    expect(out.map(p => p.value)).toEqual([...Array(30).fill(10)]);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(300, 10);
  });

  it('folds the repeated hour onto its first occurrence for a window spanning the whole repeat', () => {
    // Window [01:00 EDT .. 02:00 EST): the whole 120-minute row lies inside,
    // so the KPI share is 1 — the second occurrence's 60 minutes fold onto
    // the same-label first-occurrence buckets (2 real minutes per tick), and
    // the chart total still equals the KPI proration. Note this window does
    // NOT discriminate the fix: the pre-fix ÷60 happened to give the same
    // 20 per tick here (full-coverage windows compensated the missing walk).
    // The discriminating tests are the second-occurrence window below and
    // the crossing one after it; this one pins the folded axis as a spec.
    const cutoff = dayjs('2026-11-01T03:00:00');
    const out = series(
      [{ hour_bucket: ROW }],
      () => 1200,
      dayjs('2026-11-01T01:00:00'), dayjs('2026-11-01T02:00:00'), cutoff, 'minute', false,
    );
    expect(out.map(p => p.value)).toEqual([...Array(60).fill(20)]);
    const kpi = bucketWindowShare(ROW, dayjs('2026-11-01T01:00:00'), dayjs('2026-11-01T02:00:00'), cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
  });

  it('shows a window inside the second occurrence at its real share', () => {
    // Window [01:30 EST .. 01:45 EST): 15 elapsed minutes of the 120-minute
    // row, on the SECOND occurrence's own buckets (01:30 was ambiguous, so
    // both bounds are anchored to 02:00 EST, which is unambiguous). The
    // axis's exact bucket shape is not pinned: bucketStarts' startOf floor
    // re-anchors second-occurrence instants to the first occurrence (a
    // dayjs DST quirk, pre-existing), which pads the axis with trailing
    // zero buckets; the 15 in-window minutes and the total are what matter.
    const cutoff = dayjs('2026-11-01T03:00:00');
    const out = series(
      [{ hour_bucket: ROW }],
      () => 1200,
      dayjs('2026-11-01T02:00:00').subtract(30, 'minute'),
      dayjs('2026-11-01T02:00:00').subtract(15, 'minute'),
      cutoff, 'minute', false, // past-shaped (cutoff well after until), like the pages' past windows
    );
    const vals = out.map(p => p.value);
    expect(vals.filter(v => v === 10)).toHaveLength(15);
    expect(vals.every(v => v === 10 || v === 0)).toBe(true);
    const kpi = bucketWindowShare(ROW, dayjs('2026-11-01T02:00:00').subtract(30, 'minute'), dayjs('2026-11-01T02:00:00').subtract(15, 'minute'), cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
  });

  it('keeps the chart total equal to the KPI proration on a window crossing the transition', () => {
    // Window [01:50 EDT .. 02:05 EST] (the KPI suite's crossing window): 70
    // elapsed minutes of the row are inside. The axis holds 01:50..01:59 and
    // 02:00..02:04 — the 02:05 bucket starts AT until and is excluded. It
    // cannot show the second occurrence's 01:00..01:49 labels (their first
    // occurrence lies before the window), so those minutes are spread evenly
    // over the visible buckets — the total still equals the KPI proration.
    const cutoff = dayjs('2026-11-01T03:00:00');
    const out = series(
      [{ hour_bucket: ROW }],
      () => 1200,
      dayjs('2026-11-01T01:50:00'), dayjs('2026-11-01T02:05:00'), cutoff, 'minute', false,
    );
    expect(out.map(p => p.value)).toEqual([...Array(10).fill(70), ...Array(5).fill(0)]);
    const kpi = bucketWindowShare(ROW, dayjs('2026-11-01T01:50:00'), dayjs('2026-11-01T02:05:00'), cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
  });

  it('never appends a live cell whose label already exists (the repeated hour second occurrence)', () => {
    // A live 3h window ending at 01:30 EST (the FALL-BACK's second
    // occurrence), cutoff 01:33 EST INSIDE the live cell [01:30, 01:45):
    // the append must be skipped — the axis already carries the FIRST
    // occurrence's 01:30 label, and the dayjs startOf('minute') re-anchor
    // (an ambiguous local 01:30 resolves to the first occurrence, 01:30
    // EDT — the documented quirk) makes the range-derived alignment gate
    // fail first for second-occurrence untils anyway. Either way, no
    // duplicate tick is appended: the axis stays monotonic and the chart
    // total still equals the KPI proration (both exclude the second
    // occurrence's tail).
    const since = dayjs('2026-11-01T02:00:00').subtract(3, 'hour'); // 00:00 EDT
    const until = dayjs('2026-11-01T02:00:00').subtract(30, 'minute'); // 01:30 EST
    const cutoff = dayjs('2026-11-01T02:00:00').subtract(27, 'minute'); // 01:33 EST
    const out = series(
      [{ hour_bucket: '2026-11-01T01:00:00' }],
      () => 1200,
      since, until, cutoff, 'min15',
    );
    const labels = out.map(p => p.label);
    expect(labels).toEqual([...new Set(labels)]);   // no duplicated tick
    expect(labels.every((s, i) => i === 0 || s > labels[i - 1])).toBe(true); // monotonic
    const kpi = bucketWindowShare('2026-11-01T01:00:00', since, until, cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
  });

  it('stays exact with second-precision bounds (the live fetch shape)', () => {
    // A live 30m window fetched 30s into 01:30 EDT: since/until/cutoff all
    // carry seconds. The cutoff floors to the minute grid, so the row's
    // recorded extent reads 30 WHOLE minutes, and the covered milliseconds
    // inside the window must be attributed exactly — the 01:00 tick gets
    // only the 30s of its minute inside the window. Whole-minute counting
    // would hand the partial edge minutes to full minutes and break the
    // total; the ms-exact fold keeps the chart total equal to the KPI
    // proration.
    const since = dayjs('2026-11-01T01:00:30'); // 01:00:30 EDT (ambiguous 01:00 parses first)
    const until = dayjs('2026-11-01T01:30:30'); // 01:30:30 EDT
    const out = series(
      [{ hour_bucket: ROW }],
      () => 1200,
      since, until, until, 'minute',
    );
    // Coverage = 30 whole minutes (floored cutoff 01:30:00 EDT); the window
    // holds 1,770,000 of the row's 1,800,000 covered ms.
    const kpi = bucketWindowShare(ROW, since, until, until, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi, 10);
    // Partial edge minute: 30s on the 01:00 tick, full minutes in between,
    // nothing on the 01:30 tick (its minute starts AT the floored extent).
    expect(out[0].value).toBeCloseTo(1200 * (30000 / 1800000), 10);
    expect(out[1].value).toBeCloseTo(1200 * (60000 / 1800000), 10);
    expect(out[30].value).toBe(0);
  });

  // On a window crossing the transition from mid-first-occurrence,
  // bucketStarts dedups every second-occurrence label off the axis (their
  // sort keys sort below the already-emitted first-occurrence cells —
  // "01:59" then "01:00"), so a row anchored on the second occurrence (a
  // "'…-05:00'" hour_bucket — e.g. if a serialization ever keeps the two
  // occurrences separate; the current server's local-hour truncation
  // merges them into the first occurrence's row, Go resolving the
  // ambiguous local hour to the earlier offset) reaches the visible cells
  // only through the repeat branch's fold (current engines: dayjs's
  // startOf('hour') re-anchors the second row onto the first occurrence,
  // so it folds like the first row) or its counts-empty spread arm
  // (engines without that re-anchor — the drop report). Either way the
  // chart total must equal bucketWindowShare's KPI proration for the same
  // window and rows — the second row's newest usage must never vanish from
  // the line while the KPI counts it.
  it('keeps the second-occurrence row when a 30m window crosses the transition mid-first-occurrence (both rows)', () => {
    // A 30m preset viewed at 01:15 EST = [01:45 EDT .. 01:15 EST): 30
    // elapsed minutes crossing the transition, starting mid-first-
    // occurrence. Both bounds anchored to the unambiguous 02:00 EST (the
    // file's established idiom — 01:xx parses to the first occurrence).
    const since = dayjs('2026-11-01T02:00:00').subtract(75, 'minute'); // 01:45 EDT
    const until = dayjs('2026-11-01T02:00:00').subtract(45, 'minute'); // 01:15 EST
    const cutoff = dayjs('2026-11-01T03:00:00');
    // First-occurrence row: 01:00 EDT (05:00Z), 120-minute coverage
    // (rowCoverageEnd's repeat walk). Second-occurrence row: its own 01:00
    // EST instant (06:00Z), 60-minute real extent — re-anchored to the
    // first occurrence's 120-minute view by startOf('hour') where the
    // engine disambiguates the ambiguous wall-clock, covered by the
    // counts-empty arm where it does not; both prorations of its in-window
    // slice coincide (30/120 ≡ 15/60).
    const rows = [
      { hour_bucket: '2026-11-01T01:00:00-04:00', v: 1200 },
      { hour_bucket: '2026-11-01T01:00:00-05:00', v: 600 },
    ];
    const out = series(rows, r => r.v, since, until, cutoff, 'minute', false);
    // The axis holds 01:45..01:59 EDT (15 cells; every second-occurrence
    // label sorts below the emitted "01:59" and was dropped). The first
    // row folds 30 of its 120 minutes onto them (2 min per cell); the
    // second row contributes its in-window slice the same way (its
    // re-anchored fold also lands 2 min per cell of its 120-minute view,
    // or 1 min per cell of its real 60 via the spread arm) — 3 min per
    // cell total.
    expect(out.map(p => p.value)).toEqual([...Array(15).fill(30)]);
    const kpi1 = bucketWindowShare(rows[0].hour_bucket, since, until, cutoff, 'hour');
    const kpi2 = bucketWindowShare(rows[1].hour_bucket, since, until, cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1200 * kpi1 + 600 * kpi2, 10);
    expect(1200 * kpi1).toBeCloseTo(300, 10);  // 30/120 of the first row
    expect(600 * kpi2).toBeCloseTo(150, 10);   // the second row's in-window slice (30/120 folded or 15/60 spread)
  });

  it('shows the second-occurrence row alone (the chart is not flat 0)', () => {
    const since = dayjs('2026-11-01T02:00:00').subtract(75, 'minute'); // 01:45 EDT
    const until = dayjs('2026-11-01T02:00:00').subtract(45, 'minute'); // 01:15 EST
    const cutoff = dayjs('2026-11-01T03:00:00');
    // Only the second occurrence recorded usage (the newest traffic — the
    // #135 "line ends at the real last in-window value" contract): the
    // chart must surface it instead of dropping to a flat 0 — whether the
    // engine re-anchors the row onto the first occurrence (fold, 2 min per
    // cell of a 120-minute view) or keeps its offset (counts-empty spread,
    // 1 min per cell of its real 60) — both land 10 per cell.
    const out = series(
      [{ hour_bucket: '2026-11-01T01:00:00-05:00', v: 600 }],
      r => r.v, since, until, cutoff, 'minute', false,
    );
    expect(out.map(p => p.value)).toEqual([...Array(15).fill(10)]);
    const kpi = bucketWindowShare('2026-11-01T01:00:00-05:00', since, until, cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(600 * kpi, 10);
    expect(600 * kpi).toBeCloseTo(150, 10); // the second row's in-window slice
  });

  it('reads the real floored recorded extent when the fetch falls inside the second occurrence', () => {
    // The live-window shape: a 30m preset window ending at 01:15 EST whose
    // fetch resolves ~30 min into the repeated hour (cutoff 01:45 EST —
    // inside the second occurrence, on the same wall-clock minute grid as
    // the first). The row's recorded extent floors to the second
    // occurrence's minute grid (01:45 EST = 105 whole minutes from 01:00
    // EDT), never to the first occurrence (01:45 EDT, 60 minutes earlier):
    // the window holds 30 of those 105 minutes, so the row's value is
    // prorated by 30/105 — the chart and the KPI must both show it. (The
    // pre-fix floor re-anchored the cutoff's ambiguous wall-clock to the
    // first occurrence, ending the coverage BEFORE the window's start —
    // whole recent usage read 0.)
    const since = dayjs('2026-11-01T02:00:00').subtract(75, 'minute'); // 01:45 EDT
    const until = dayjs('2026-11-01T02:00:00').subtract(45, 'minute'); // 01:15 EST
    const cutoff = dayjs('2026-11-01T02:00:00').subtract(15, 'minute'); // 01:45 EST
    const row = '2026-11-01T01:00:00-04:00';
    const out = series(
      [{ hour_bucket: row, v: 1050 }],
      r => r.v, since, until, cutoff, 'minute', false,
    );
    // Covered minutes 01:45..01:59 EDT land on their cells, the
    // second-occurrence minutes 01:00..01:14 fold onto them (2 min per
    // cell), for 30/105 of the row's value.
    out.forEach(p => expect(p.value).toBeCloseTo(20, 10));
    const kpi = bucketWindowShare(row, since, until, cutoff, 'hour');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(1050 * kpi, 10);
    expect(1050 * kpi).toBeCloseTo(300, 10); // 30/105 of the row
  });
});
