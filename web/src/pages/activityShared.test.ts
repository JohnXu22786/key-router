import { describe, it, expect } from 'vitest';
import dayjs from 'dayjs';
import {
  makeRanges, customRange, granularityFor, series, stackedData, bucketAxis,
  bucketWindowShare, groupTotals, floorWindowUntil, exclusiveUntil, queryWindowUntil,
  prevWindowUntil,
  fmtTick, fmtBucket, fmtDayLabel, CUSTOM_KEY,
  computeTrending, toChartData, cacheHitRate, resampleResponse, liveExtensionEligible,
  prorateBoundaryBuckets,
} from './activityShared';
import type { ActivityResponse } from '../api/client';
import type { Granularity } from './activityShared';

// A fixed "now" matching the OR snapshot context: Thursday, Aug 13 2026,
// 16:05 local. The dynamic badge expectations (16h / 4d / 13d / 7mo) are
// the values OpenRouter showed for those presets on the same weekday.
const NOW = dayjs('2026-08-13T16:05:00');

describe('makeRanges — rolling presets', () => {
  it('keeps OpenRouter preset order and static badges', () => {
    const rs = makeRanges(NOW);
    const rolling = rs.slice(0, 9).map(r => `${r.key}:${r.badge}:${r.label}`);
    expect(rolling).toEqual([
      '15m:15m:Past 15 Minutes',
      '30m:30m:Past 30 Minutes',
      '1h:1h:Past 1 Hour',
      '3h:3h:Past 3 Hours',
      '1d:1d:Past 24 Hours',
      '2d:2d:Past 48 Hours',
      '1w:1w:Past 1 Week',
      '1mo:1mo:Past 1 Month',
      '1y:1y:Past 1 Year',
    ]);
  });

  it('anchors rolling windows to now', () => {
    const rs = makeRanges(NOW);
    const byKey = new Map(rs.map(r => [r.key, r]));
    expect(byKey.get('1d')!.since.format('YYYY-MM-DD HH:mm')).toBe('2026-08-12 16:05');
    expect(byKey.get('1d')!.until.valueOf()).toBe(NOW.valueOf());
    expect(byKey.get('1mo')!.since.format('YYYY-MM-DD')).toBe('2026-07-13');
    expect(byKey.get('1y')!.since.format('YYYY-MM-DD')).toBe('2025-08-13');
  });
});

describe('makeRanges — calendar-anchored presets', () => {
  it('computes dynamic badges from the real range length (OR values)', () => {
    const rs = makeRanges(NOW);
    const byKey = new Map(rs.map(r => [r.key, r]));
    expect(byKey.get('today')!.badge).toBe('16h');       // since midnight
    expect(byKey.get('yesterday')!.badge).toBe('24h');
    expect(byKey.get('week')!.badge).toBe('4d');         // Mon..Thu
    expect(byKey.get('prevweek')!.badge).toBe('7d');
    expect(byKey.get('month')!.badge).toBe('13d');       // Aug 1..13
    expect(byKey.get('prevmonth')!.badge).toBe('31d');   // July has 31 days
    expect(byKey.get('year')!.badge).toBe('7mo');        // Jan..Aug
    expect(byKey.get('prevyear')!.badge).toBe('1y');
  });

  it('anchors the week to the PREVIOUS Monday when now is a Sunday', () => {
    // Sunday Aug 16 2026: the week must still start Monday Aug 10.
    const rs = makeRanges(dayjs('2026-08-16T10:00:00'));
    const byKey = new Map(rs.map(r => [r.key, r]));
    expect(byKey.get('week')!.since.format('YYYY-MM-DD')).toBe('2026-08-10');
    expect(byKey.get('prevweek')!.until.format('YYYY-MM-DD')).toBe('2026-08-10');
    expect(byKey.get('week')!.badge).toBe('6d'); // Mon..Sun 10:00 = 6.4d
    expect(byKey.get('prevweek')!.badge).toBe('7d');
  });

  it('anchors windows to calendar boundaries', () => {
    const rs = makeRanges(NOW);
    const byKey = new Map(rs.map(r => [r.key, r]));
    expect(byKey.get('today')!.since.format('YYYY-MM-DD HH:mm')).toBe('2026-08-13 00:00');
    expect(byKey.get('yesterday')!.since.format('YYYY-MM-DD HH:mm')).toBe('2026-08-12 00:00');
    expect(byKey.get('yesterday')!.until.format('YYYY-MM-DD HH:mm')).toBe('2026-08-13 00:00');
    expect(byKey.get('week')!.since.format('YYYY-MM-DD')).toBe('2026-08-10');   // Monday
    expect(byKey.get('prevweek')!.since.format('YYYY-MM-DD')).toBe('2026-08-03');
    expect(byKey.get('prevweek')!.until.format('YYYY-MM-DD')).toBe('2026-08-10');
    expect(byKey.get('month')!.since.format('YYYY-MM-DD')).toBe('2026-08-01');
    expect(byKey.get('prevmonth')!.since.format('YYYY-MM-DD')).toBe('2026-07-01');
    expect(byKey.get('prevmonth')!.until.format('YYYY-MM-DD')).toBe('2026-08-01');
    expect(byKey.get('year')!.since.format('YYYY-MM-DD')).toBe('2026-01-01');
    expect(byKey.get('prevyear')!.since.format('YYYY-MM-DD')).toBe('2025-01-01');
    expect(byKey.get('prevyear')!.until.format('YYYY-MM-DD')).toBe('2026-01-01');
  });

  it('assigns granularity by scale: sub-hour minutes, hours, days, months', () => {
    const byKey = new Map(makeRanges(NOW).map(r => [r.key, r]));
    expect(byKey.get('15m')!.granularity).toBe('minute');
    expect(byKey.get('30m')!.granularity).toBe('minute');
    expect(byKey.get('1h')!.granularity).toBe('minute');
    expect(byKey.get('3h')!.granularity).toBe('min15');
    expect(byKey.get('1d')!.granularity).toBe('hour');
    expect(byKey.get('2d')!.granularity).toBe('hour');
    expect(byKey.get('today')!.granularity).toBe('hour');
    expect(byKey.get('yesterday')!.granularity).toBe('hour');
    expect(byKey.get('1w')!.granularity).toBe('day');
    expect(byKey.get('1mo')!.granularity).toBe('day');
    expect(byKey.get('prevmonth')!.granularity).toBe('day');
    expect(byKey.get('1y')!.granularity).toBe('month');
    expect(byKey.get('year')!.granularity).toBe('month');
  });
});

describe('custom range', () => {
  it('derives granularity from the window length', () => {
    const since = dayjs('2026-08-13T00:00:00');
    expect(customRange(since, since.add(15, 'minute')).granularity).toBe('minute');
    expect(customRange(since, since.add(1, 'hour')).granularity).toBe('minute');
    expect(customRange(since, since.add(2, 'hour')).granularity).toBe('min15');
    expect(customRange(since, since.add(3, 'hour')).granularity).toBe('min15');
    expect(customRange(since, since.add(4, 'hour')).granularity).toBe('hour');
    expect(customRange(since, since.add(2, 'day')).granularity).toBe('hour');   // < 3d
    expect(customRange(since, since.add(3, 'day')).granularity).toBe('day');
    expect(customRange(since, since.add(30, 'day')).granularity).toBe('day');
    expect(customRange(since, since.add(61, 'day')).granularity).toBe('month');
    expect(customRange(since, since.add(400, 'day')).granularity).toBe('month');
    expect(granularityFor(since, since.add(71, 'hour'))).toBe('hour'); // 2.96d < 3d
  });

  it('marks the key as custom with empty badge', () => {
    const c = customRange(dayjs('2026-08-01'), dayjs('2026-08-13'));
    expect(c.key).toBe(CUSTOM_KEY);
    expect(c.badge).toBe('');
  });
});

describe('series — continuous axis bucketing', () => {
  const row = (hourBucket: string) => ({ hour_bucket: hourBucket });

  it('buckets a 24h window hourly with zero-filled gaps', () => {
    const since = dayjs('2026-08-13T09:00:00');
    const until = dayjs('2026-08-13T13:00:00');
    const out = series(
      [row('2026-08-13T10:00:00'), row('2026-08-13T10:30:00'), row('2026-08-13T12:00:00')],
      () => 1, since, until, until, 'hour',
    );
    // [since, until): the bucket starting AT until (13:00) is excluded — it
    // can never hold data, so it must not render as an empty trailing tick.
    expect(out.map(p => p.label)).toEqual(['08-13 09:00', '08-13 10:00', '08-13 11:00', '08-13 12:00']);
    expect(out.map(p => p.value)).toEqual([0, 2, 0, 1]);
  });

  it('buckets a week daily and a year monthly', () => {
    const since = dayjs('2026-08-10T00:00:00');
    const out = series([row('2026-08-11T03:00:00'), row('2026-08-13T22:00:00')], () => 5, since, since.add(4, 'day'), since.add(4, 'day'), 'day');
    // The 08-14 bucket starts exactly AT until — excluded from the axis.
    expect(out.map(p => p.label)).toEqual(['08-10', '08-11', '08-12', '08-13']);
    expect(out.map(p => p.value)).toEqual([0, 5, 0, 5]);

    const ySince = dayjs('2026-01-01T00:00:00');
    const yUntil = dayjs('2026-03-01T23:59:59');
    const yOut = series([row('2026-01-15T00:00:00'), row('2026-03-02T00:00:00')], () => 7, ySince, yUntil, yUntil, 'month');
    expect(yOut.map(p => p.label)).toEqual(['2026-01', '2026-02', '2026-03']);
    expect(yOut.map(p => p.value)).toEqual([7, 0, 7]);
  });

  it('starts the axis at the range start, not at the first data point', () => {
    const since = dayjs('2026-08-01T00:00:00');
    const until = since.add(6, 'day');
    const out = series([row('2026-08-05T00:00:00')], () => 1, since, until, until, 'day');
    expect(out.length).toBe(6);
    expect(out[0].label).toBe('08-01');
    expect(out[0].value).toBe(0);
  });
});

describe('preset windows snapped to the bucket grid', () => {
  // Mirrors Activity.tsx: presets snap BOTH bounds; custom ranges don't.
  const snapped = (r: { key: string; since: dayjs.Dayjs; until: dayjs.Dayjs; granularity: Granularity }) => ({
    ...r,
    since: floorWindowUntil(r.since, r.granularity),
    until: floorWindowUntil(r.until, r.granularity),
  });
  const NOW = dayjs('2026-08-13T16:05:30'); // Thursday

  it('every rolling preset is exactly its nominal length of complete buckets', () => {
    const byKey = new Map(makeRanges(NOW).map(r => [r.key, snapped(r)]));
    const w = (k: string) => byKey.get(k)!;
    expect(w('15m').until.diff(w('15m').since, 'minute')).toBe(15);
    expect(w('30m').until.diff(w('30m').since, 'minute')).toBe(30);
    expect(w('1h').until.diff(w('1h').since, 'minute')).toBe(60);
    expect(w('3h').until.diff(w('3h').since, 'minute')).toBe(180);
    expect(w('3h').since.minute() % 15).toBe(0); // 3h -> 15-minute grid
    expect(w('3h').until.minute() % 15).toBe(0);
    expect(w('1d').until.diff(w('1d').since, 'hour')).toBe(24);
    expect(w('1d').since.minute()).toBe(0);      // 1d -> hour grid
    expect(w('1d').until.minute()).toBe(0);
    expect(w('2d').until.diff(w('2d').since, 'hour')).toBe(48);
    expect(w('1w').until.diff(w('1w').since, 'day')).toBe(7);
    expect(w('1w').until.hour()).toBe(0);        // 1w -> day grid
    expect(w('1mo').until.diff(w('1mo').since, 'day')).toBe(31); // Jul 13..Aug 13
    expect(w('1mo').until.hour()).toBe(0);
    expect(w('1y').until.diff(w('1y').since, 'month')).toBe(12);
    expect(w('1y').since.date()).toBe(1);        // 1y -> month grid
  });

  it('calendar presets snap to their own grid, live bucket excluded', () => {
    const byKey = new Map(makeRanges(NOW).map(r => [r.key, snapped(r)]));
    const w = (k: string) => byKey.get(k)!;
    // Today: midnight .. current hour (live hour excluded).
    expect(w('today').since.format('YYYY-MM-DD HH:mm')).toBe('2026-08-13 00:00');
    expect(w('today').until.format('YYYY-MM-DD HH:mm')).toBe('2026-08-13 16:00');
    // Yesterday: a whole 24h, unchanged.
    expect(w('yesterday').until.diff(w('yesterday').since, 'hour')).toBe(24);
    // This week: Monday .. today 00:00 (live day excluded).
    expect(w('week').since.day()).toBe(1);
    expect(w('week').until.format('YYYY-MM-DD')).toBe('2026-08-13');
    expect(w('week').until.hour()).toBe(0);
    // This month: 1st .. today 00:00 (live day excluded).
    expect(w('month').since.date()).toBe(1);
    expect(w('month').until.format('YYYY-MM-DD')).toBe('2026-08-13');
    // This year: Jan 1 .. Aug 1 (live month excluded).
    expect(w('year').since.format('YYYY-MM-DD')).toBe('2026-01-01');
    expect(w('year').until.format('YYYY-MM-DD')).toBe('2026-08-01');
    // Past periods were already on boundaries — unchanged.
    expect(w('prevweek').until.diff(w('prevweek').since, 'day')).toBe(7);
    expect(w('prevmonth').until.diff(w('prevmonth').since, 'day')).toBe(31);
    expect(w('prevyear').until.diff(w('prevyear').since, 'year')).toBe(1);
  });
});

describe('floorWindowUntil — rolling windows end at the last complete bucket', () => {
  it('floors to the granularity bucket start', () => {
    const t = dayjs('2026-08-13T16:05:30');
    expect(floorWindowUntil(t, 'minute').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:05:00');
    expect(floorWindowUntil(t, 'min15').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
    expect(floorWindowUntil(t, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
    expect(floorWindowUntil(t, 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 00:00:00');
    expect(floorWindowUntil(t, 'month').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-01 00:00:00');
  });

  it('a 24h window snapped to the bucket grid keeps the live bucket at its recorded share', () => {
    // Both bounds snap to the hour grid: [16:00 yesterday, 16:00 today).
    // The 16:00 bucket (recorded up to the fetch at 16:30) starts exactly AT
    // until — its recorded extent is real in-window usage, so the share is 1
    // (the accumulated live value), never a clamped 0 that would drop the
    // user's newest usage from the chart and make the line fall to 0.
    const since = floorWindowUntil(dayjs('2026-08-12T16:05:00'), 'hour');
    const until = floorWindowUntil(dayjs('2026-08-13T16:30:00'), 'hour');
    expect(since.format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-12 16:00:00');
    expect(until.format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, dayjs('2026-08-13T16:30:00'), 'hour')).toBe(1);
    // Boundary hours inside the snapped window are whole…
    expect(bucketWindowShare('2026-08-12T16:00:00', since, until, dayjs('2026-08-13T16:30:00'), 'hour')).toBe(1);
    // …and interior hours stay whole too.
    expect(bucketWindowShare('2026-08-13T12:00:00', since, until, dayjs('2026-08-13T16:30:00'), 'hour')).toBe(1);
  });

  it('a snapped 24h window is stable between auto-refreshes: only the live hour real usage moves', () => {
    // Two refreshes 30s apart: both bounds snap to the SAME bucket grid, so
    // the completed hours render identical values; the only difference is the
    // live hour's row accumulating real usage (30 -> 31). No phantom decay,
    // no boundary drift — the window slides by one bucket only when the hour
    // grid rolls over.
    const rows = [
      { hour_bucket: '2026-08-12T16:00:00', v: 60 },
      { hour_bucket: '2026-08-12T17:00:00', v: 60 },
      { hour_bucket: '2026-08-13T15:00:00', v: 60 },
      { hour_bucket: '2026-08-13T16:00:00', v: 30 }, // live hour — real accumulated usage
    ];
    const refresh1 = series(rows, r => r.v,
      floorWindowUntil(dayjs('2026-08-12T16:05:00'), 'hour'),
      floorWindowUntil(dayjs('2026-08-13T16:30:00'), 'hour'),
      dayjs('2026-08-13T16:30:00'), 'hour');
    const refresh2 = series([
      ...rows.slice(0, 3),
      { hour_bucket: '2026-08-13T16:00:00', v: 31 }, // the live hour really grew +1
    ], r => r.v,
      floorWindowUntil(dayjs('2026-08-12T16:05:30'), 'hour'),
      floorWindowUntil(dayjs('2026-08-13T16:30:30'), 'hour'),
      dayjs('2026-08-13T16:30:30'), 'hour');
    expect(refresh1.map(p => p.label)).toEqual(refresh2.map(p => p.label));
    // The last point is the LIVE hour — refresh2 ends at 31: the line shows
    // the real last in-window value instead of falling to 0.
    expect(refresh1[refresh1.length - 1].value).toBe(30);
    expect(refresh2[refresh2.length - 1].value).toBe(31);
    // The 24 completed hours are identical between refreshes: only the live
    // bucket carries the real +1.
    const complete1 = refresh1.slice(0, -1).reduce((a, p) => a + p.value, 0);
    const complete2 = refresh2.slice(0, -1).reduce((a, p) => a + p.value, 0);
    expect(complete1).toBe(180);
    expect(complete2).toBe(complete1);
  });

  it('exclusiveUntil lands one second before the floored bucket for server queries', () => {
    // The activity endpoint widens the window to the bucket CONTAINING
    // until; one second before the floored bucket makes it end exactly at
    // the floored bucket, so the server excludes the live bucket itself.
    expect(exclusiveUntil(dayjs('2026-08-13T16:00:00'), 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 15:59:59');
    expect(exclusiveUntil(dayjs('2026-08-13T00:00:00'), 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-12 23:59:59');
  });
});

describe('queryWindowUntil — the query keeps every in-range bucket', () => {
  // Mirrors Activity.tsx: presets snap BOTH bounds before reaching the page.
  const snapped = (r: { key: string; since: dayjs.Dayjs; until: dayjs.Dayjs; granularity: Granularity }) => ({
    ...r,
    since: floorWindowUntil(r.since, r.granularity),
    until: floorWindowUntil(r.until, r.granularity),
  });
  const NOW = dayjs('2026-08-13T16:05:30'); // Thursday
  const byKey = new Map(makeRanges(NOW).map(r => [r.key, snapped(r)]));
  const w = (k: string) => byKey.get(k)!;

  it('an hour-granularity range mid-day keeps the in-progress day (default 1d + day rollup)', () => {
    // Past 24 Hours snapped to the hour grid is [Aug 12 16:00, Aug 13 16:00).
    // The old code floored until to the ROLLUP (day) granularity and sent Aug
    // 12 23:59:59, dropping today's complete hours from the day buckets; the
    // query must send the range's until as-is so the day rollup keeps today.
    expect(w('1d').granularity).toBe('hour');
    expect(queryWindowUntil(w('1d'), 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
  });

  it('a today range mid-day is not empty', () => {
    // Old behavior sent yesterday 23:59:59, so the endpoint's day window
    // [today 00:00, today 00:00) was EMPTY and Explore rendered "No usage in
    // this period" for a day that has traffic.
    expect(queryWindowUntil(w('today'), 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
  });

  it('a day-granularity range keeps the in-progress month for a coarser month rollup', () => {
    // This Week ends at the live day's start (day grid). A month rollup whose
    // until is floored to the month boundary would send July 31 23:59:59 for
    // an August week, amputating the whole in-progress month; the query must
    // send the day boundary as-is so the month bucket stays.
    expect(w('week').granularity).toBe('day');
    expect(queryWindowUntil(w('week'), 'month').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 00:00:00');
  });

  it('week and total rollups align by their server-shaped cuts (week->day, total->hour)', () => {
    // The week bucket is anchored to Monday, so it cuts per day. This Week
    // ends at the LIVE day's start — that bucket holds real recorded usage
    // and the query keeps it (raw until), so the week chart ends at the live
    // value instead of amputating today.
    expect(queryWindowUntil(w('week'), 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 00:00:00');
    // The total bucket aggregates over the hour-shaped window (same as the
    // hour rollup on the server); Today ends at the LIVE hour's start, which
    // must stay in the query — the total is never empty.
    expect(queryWindowUntil(w('today'), 'total').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
  });

  it('a CURRENT-aligned preset boundary keeps its live bucket: the query passes until as-is', () => {
    // A 24h window ending at 09:00 sharp (the current hour's start): the
    // live hour's row is the chart's real last in-window value, so the query
    // sends the boundary as-is and the server's widened window includes the
    // 09:00 bucket — the hour must NOT be amputated (that made the line
    // always fall to 0 while the traffic lives in the current hour).
    const r = { key: '1d', label: '', badge: '', since: dayjs('2026-08-12T09:00:00'), until: dayjs('2026-08-13T09:00:00'), granularity: 'hour' as Granularity };
    expect(queryWindowUntil(r, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 09:00:00');
    // A CUSTOM range ending on the same boundary is NOT eligible: its end is
    // a chosen cutoff, not "now" — the live bucket stays excluded.
    const custom = { ...r, key: 'custom' };
    expect(queryWindowUntil(custom, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 08:59:59');
  });

  it('a fetch resolving just after a bucket rollover keeps the last in-window bucket (no fetch-time clock)', () => {
    // ROLLOVER RACE: the range was RENDERED at 16:59:59.9xx — Activity.tsx
    // snapped its until to 16:00, the start of the hour containing the
    // render-time now — and the fetch RESOLVES at 17:00:00.0xx, one bucket
    // later. The old code judged the live alignment against the fetch-time
    // clock (floorWindowUntil(dayjs(), ...)): floor(17:00:00.0) = 17:00 !=
    // 16:00, so eligibility failed and the query fell to exclusiveUntil ->
    // 15:59:59. The server's widened window then ended at 16:00 and dropped
    // the COMPLETED 16:00 bucket (a full hour of data, the window's last
    // in-window bucket) from the Trends/Explore response — the #135 "line
    // ends one bucket early" symptom for that one refresh. Alignment is a
    // property of the RANGE (its until lies on its own bucket grid = the
    // bucket that contained the render-time now), never of the fetch clock,
    // so this window must still pass 16:00 as-is.
    const hour = { key: '1d', label: '', badge: '', since: dayjs('2026-08-12T16:00:00'), until: dayjs('2026-08-13T16:00:00'), granularity: 'hour' as Granularity };
    expect(queryWindowUntil(hour, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:00:00');
    // One bucket coarser: rendered Aug 12 23:59:59.9xx -> until = Aug 12
    // 00:00 (the live day); fetch Aug 13 00:00:00.0xx. The COMPLETED Aug 12
    // day stays in the query, never amputated to Aug 11 23:59:59.
    const day = { key: '1w', label: '', badge: '', since: dayjs('2026-08-05T00:00:00'), until: dayjs('2026-08-12T00:00:00'), granularity: 'day' as Granularity };
    expect(queryWindowUntil(day, 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-12 00:00:00');
    // And one coarser still: rendered Jul 31 23:59:59.9xx -> until = Jul 1
    // (the live month); fetch Aug 1 00:00:00.0xx. The COMPLETED July month
    // stays in the query, never amputated to Jun 30 23:59:59.
    const month = { key: '1y', label: '', badge: '', since: dayjs('2025-07-01T00:00:00'), until: dayjs('2026-07-01T00:00:00'), granularity: 'month' as Granularity };
    expect(queryWindowUntil(month, 'month').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-07-01 00:00:00');
  });

  it('a past preset whose boundary coincides with the current unit still excludes it (Prev Week on a Monday)', () => {
    // Prev Week [Aug 3, Aug 10) viewed ON Monday Aug 10: its until equals the
    // current day's start — without the current-period gate the week rollup
    // would keep the live day and double-count today's usage on the prev
    // chart. The boundary data belongs to the current week; it must stay out.
    const r = { key: 'prevweek', label: '', badge: '', since: dayjs('2026-08-03T00:00:00'), until: dayjs('2026-08-10T00:00:00'), granularity: 'day' as Granularity };
    expect(queryWindowUntil(r, 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-09 23:59:59');
    expect(queryWindowUntil(r, 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-09 23:59:59');
  });

  it('past day-aligned and month-aligned ranges still exclude exactly the live bucket', () => {
    // Prev month ends at Aug 1 00:00 — a PAST boundary (not the current
    // month's start): the live bucket stays excluded, unchanged.
    expect(queryWindowUntil(w('prevmonth'), 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-07-31 23:59:59');
    // This year ends at Aug 1 00:00 — the CURRENT month's start: the live
    // month (August) holds the user's newest usage and must stay in the query.
    expect(queryWindowUntil(w('year'), 'month').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-01 00:00:00');
  });

  it('week rollup on a past preset excludes the whole boundary week (week-grid alignment)', () => {
    // A week rollup anchors buckets to MONDAY (activityWindow's week
    // branch), so the day-aligned exclusion (until-1s) still lies INSIDE
    // the boundary week [mondayOf(until), +7d): the server widens the query
    // into that whole week, its bucket renders on the axis labeled with the
    // boundary Monday, and the CURRENT period's rows (up to the next
    // Monday) aggregate into it — the #137 violation (Prev Month + Weekly
    // shows next month's first days in its last bucket). Flooring the
    // exclusion to the WEEK grid instead (one second before the boundary
    // week's Monday) makes the widened server window end exactly AT the
    // boundary week and drop it entirely, keeping the Monday-anchored grid.
    expect(queryWindowUntil(w('prevmonth'), 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-07-26 23:59:59');
    expect(queryWindowUntil(w('prevyear'), 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2025-12-28 23:59:59');
    // Prev Week ends ON a Monday: the week floor coincides with the day
    // floor there, so the result is unchanged (see the Monday-alignment
    // test above).
    expect(queryWindowUntil(w('prevweek'), 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-09 23:59:59');
  });

  it('a range living wholly inside the boundary week keeps the day-aligned exclusion (Yesterday + Weekly)', () => {
    // Yesterday [Aug 12, Aug 13) sits INSIDE the boundary week [Aug 10,
    // Aug 17): aligning the exclusion to the week grid would put the
    // widened server window entirely BEFORE the range and return an EMPTY
    // response for a day that has usage. The week-grid exclusion applies
    // only when the boundary week starts inside the range; this window
    // keeps the day-aligned exclusion unchanged.
    expect(queryWindowUntil(w('yesterday'), 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-12 23:59:59');
  });

  it('a custom range on a day boundary keeps the day-aligned exclusion for week rollups', () => {
    // Custom picks are never re-aligned: a midnight-ending custom range
    // keeps the day-floor exclusion even under a week rollup — the boundary
    // week's in-range days (Aug 10-12 here) are the user's own chosen data,
    // and sending the week-grid exclusion would amputate them from the
    // response.
    const r = { key: CUSTOM_KEY, label: '', badge: '', since: dayjs('2026-08-03T00:00:00'), until: dayjs('2026-08-13T00:00:00'), granularity: 'day' as Granularity };
    expect(queryWindowUntil(r, 'week').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-12 23:59:59');
  });

  it('a mid-bucket CUSTOM range keeps its trailing partial day for a finer rollup', () => {
    // Custom ranges are never snapped: the user picked an until of 16:05.
    // Flooring it to the day would amputate the final day they selected; the
    // query must send it as-is (the hour rollup's containing bucket 16:00 —
    // part of the picked range — is then included rather than cut off).
    const r = { key: 'c', label: '', badge: '', since: dayjs('2026-08-08T10:00:00'), until: dayjs('2026-08-13T16:05:00'), granularity: 'day' as Granularity };
    expect(queryWindowUntil(r, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 16:05:00');
  });

  it('sub-hour ranges pass the raw range.until (the re-sampling source)', () => {
    expect(w('15m').granularity).toBe('minute');
    expect(queryWindowUntil(w('15m'), 'hour').toISOString()).toBe(w('15m').until.toISOString());
  });
});

describe('prevWindowUntil — the previous-period query keeps the mid-bucket since boundary slice', () => {
  // Mirrors the activity endpoint (activityWindow + buildActivityAxis in
  // admin.go): a server-bucketed query widens to the rollup buckets
  // CONTAINING its raw bounds (to = floor(untilQ) + 1 unit), sums the FULL
  // boundary rows into the response, and emits the floored axis
  // floor(sinceQ) .. floor(untilQ). Each bucket carries `val`.
  const serverResp = (g: 'hour' | 'day', sinceQ: dayjs.Dayjs, untilQ: dayjs.Dayjs, val: number): ActivityResponse => {
    const buckets: string[] = [];
    const fmt = g === 'hour' ? 'YYYY-MM-DD HH:00' : 'YYYY-MM-DD';
    for (let t = floorWindowUntil(sinceQ, g); !t.isAfter(untilQ); t = t.add(1, g)) {
      buckets.push(t.format(fmt));
    }
    return {
      metric: 'spend', group_by: 'model', rollup: g,
      series: buckets.map(b => ({ bucket: b, group: 'a', value: val, is_zero: false })),
      summary: [{ group: 'a', min: val, max: val, avg: val, sum: val * buckets.length, value: val, percent: 100 }],
      buckets,
      totals: { spend: val * buckets.length, tokens: 0, requests: 0, cache: 0 },
    };
  };

  it('a custom hour range with a mid-bucket since keeps its boundary slice in the previous period (no gap, no double count)', () => {
    // Custom 14:37-18:22 -> hour buckets; the previous period is
    // [10:37, 14:37). The bucket CONTAINING since, [14:00, 15:00), starts
    // INSIDE the prev window: its slice [14:00, 14:37) is prev-period data.
    const since = dayjs('2026-08-13T14:37:00');
    const until = dayjs('2026-08-13T18:22:00');
    const prevSince = dayjs('2026-08-13T10:37:00');
    const cutoff = dayjs('2026-08-13T19:00:00');
    const gran: 'hour' | 'day' = 'hour';
    const curRange = { key: CUSTOM_KEY, since, until, granularity: gran };
    // THE FIX: a mid-bucket since is sent RAW — the old code sent
    // exclusiveUntil(since) = 13:59:59, whose widened server window ended at
    // 14:00 and dropped the previous period's last 37 in-window minutes from
    // BOTH responses (the cur query only prorates [14:37, 15:00)).
    expect(prevWindowUntil(since, gran).toISOString()).toBe(since.toISOString());
    // The server response for the raw-since query carries the 14:00 bucket
    // with its full value.
    const prevResp = serverResp(gran, prevSince, prevWindowUntil(since, gran), 60);
    expect(prevResp.buckets).toEqual(['2026-08-13 10:00', '2026-08-13 11:00', '2026-08-13 12:00', '2026-08-13 13:00', '2026-08-13 14:00']);
    const prevOut = prorateBoundaryBuckets(prevResp, prevSince, since, prevWindowUntil(since, gran), cutoff, gran, gran);
    // Last bucket [14:00, 15:00): only [14:00, 14:37) = 37/60 lies in the
    // prev window — the exact share the Overview flow's bucketWindowShare
    // gives that row (the two pages must agree on identical windows).
    expect(prevOut.series.map(p => p.value)).toEqual([23, 60, 60, 60, 37]);
    expect(bucketWindowShare('2026-08-13T14:00:00', prevSince, since, cutoff, gran, false)).toBeCloseTo(37 / 60, 10);
    // The current period prorates the SAME bucket to its own slice [14:37,
    // 15:00) = 23/60; prev + cur tile the full hour exactly — the 37-minute
    // boundary usage is counted once, never lost.
    const curResp = serverResp(gran, since, queryWindowUntil(curRange, gran), 60);
    const curOut = prorateBoundaryBuckets(curResp, since, until, queryWindowUntil(curRange, gran), cutoff, gran, gran);
    expect(curOut.series.map(p => p.value)).toEqual([23, 60, 60, 60, 22]);
    expect(prevOut.series[4].value + curOut.series[0].value).toBeCloseTo(60, 10);
    expect(prevOut.summary[0].sum).toBeCloseTo(240, 10); // 23+60+60+60+37 = the whole 4h window
  });

  it('a custom day range with a mid-day since keeps its boundary day slice in the previous period', () => {
    // Custom May 10 09:30 - May 20 09:30 -> day buckets; the previous period
    // is [Apr 30 09:30, May 10 09:30). The day bucket CONTAINING since
    // (May 10) starts inside the prev window: its slice [May 10 00:00,
    // 09:30) is prev-period data. The old exclusiveUntil(since) = May 9
    // 23:59:59 made the server window end at May 10 00:00, losing that
    // whole 9.5-hour portion.
    const since = dayjs('2026-05-10T09:30:00');
    const until = dayjs('2026-05-20T09:30:00');
    const prevSince = dayjs('2026-04-30T09:30:00');
    const cutoff = dayjs('2026-05-21T00:00:00');
    const gran: 'hour' | 'day' = 'day';
    expect(granularityFor(since, until)).toBe(gran);
    expect(prevWindowUntil(since, gran).format('YYYY-MM-DD HH:mm')).toBe('2026-05-10 09:30');
    const prevResp = serverResp(gran, prevSince, prevWindowUntil(since, gran), 48);
    expect(prevResp.buckets).toContain('2026-05-10'); // the containing day is in the response
    const prevOut = prorateBoundaryBuckets(prevResp, prevSince, since, prevWindowUntil(since, gran), cutoff, gran, gran);
    // First day [Apr 30]: [09:30, 24:00) = 14.5/24; last day May 10:
    // [00:00, 09:30) = 9.5/24; interior days unchanged.
    expect(prevOut.series.map(p => p.value)).toEqual([29, 48, 48, 48, 48, 48, 48, 48, 48, 48, 19]);
    expect(bucketWindowShare('2026-05-10T00:00:00', prevSince, since, cutoff, gran, false)).toBeCloseTo(9.5 / 24, 10);
    // The current period takes the rest of May 10: [09:30, 24:00) = 14.5/24;
    // prev + cur tile the full day exactly (no gap, no double count).
    const curResp = serverResp(gran, since, queryWindowUntil({ key: CUSTOM_KEY, since, until, granularity: gran }, gran), 48);
    const curOut = prorateBoundaryBuckets(curResp, since, until, queryWindowUntil({ key: CUSTOM_KEY, since, until, granularity: gran }, gran), cutoff, gran, gran);
    expect(curOut.series[0].value).toBe(29);
    expect(prevOut.series[10].value + curOut.series[0].value).toBeCloseTo(48, 10);
  });

  it('keeps preset behavior byte-identical: snapped since still excludes the since-aligned bucket', () => {
    // Preset ranges snap BOTH bounds (Activity.tsx), so a snapped since lies
    // on the bucket grid and prevWindowUntil falls back to exclusiveUntil —
    // the exact same query as before the fix.
    const since1d = floorWindowUntil(dayjs('2026-08-12T16:05:00'), 'hour'); // 1d preset
    expect(prevWindowUntil(since1d, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-12 15:59:59');
    // A grid-aligned CUSTOM bound takes the same path: its since-aligned
    // bucket holds no in-window usage.
    expect(prevWindowUntil(dayjs('2026-08-13T14:00:00'), 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 13:59:59');
    expect(prevWindowUntil(dayjs('2026-08-03T00:00:00'), 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-02 23:59:59');
    expect(prevWindowUntil(dayjs('2025-01-01T00:00:00'), 'month').format('YYYY-MM-DD HH:mm:ss')).toBe('2024-12-31 23:59:59');
    // Sub-hour granularities keep passing the raw since (the re-sampling
    // data source), like the old subGran branch.
    expect(prevWindowUntil(dayjs('2026-08-13T14:37:00'), 'minute').format('YYYY-MM-DD HH:mm')).toBe('2026-08-13 14:37');
    expect(prevWindowUntil(dayjs('2026-08-13T14:37:00'), 'min15').format('YYYY-MM-DD HH:mm')).toBe('2026-08-13 14:37');
    // The whole downstream shape is unchanged for a snapped since: the prev
    // response ends at floor(since - 1s) — the since-aligned 16:00 bucket
    // never enters it — and prorateBoundaryBuckets leaves the response
    // untouched (its last bucket is fully in-window, share exactly 1).
    const prevResp = serverResp('hour', dayjs('2026-08-11T16:00:00'), prevWindowUntil(since1d, 'hour'), 60);
    expect(prevResp.buckets).toHaveLength(24);
    expect(prevResp.buckets[prevResp.buckets.length - 1]).toBe('2026-08-12 15:00');
    expect(prevResp.buckets).not.toContain('2026-08-12 16:00');
    const out = prorateBoundaryBuckets(prevResp, dayjs('2026-08-11T16:00:00'), since1d, prevWindowUntil(since1d, 'hour'), dayjs('2026-08-13T16:05:00'), 'hour', 'hour');
    expect(out).toBe(prevResp);
  });
});

describe('bucketWindowShare — rolling windows prorate boundary buckets', () => {
  it('prorates the since-boundary hour to the window overlap', () => {
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:05:00');
    expect(bucketWindowShare('2026-08-13T13:00:00', since, until, until, 'hour')).toBeCloseTo(55 / 60, 10);
  });

  it('keeps interior buckets whole and the current partial hour at its real value', () => {
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:05:00');
    // Interior hour: the whole bucket lies inside the window.
    expect(bucketWindowShare('2026-08-13T14:00:00', since, until, until, 'hour')).toBe(1);
    // Current partial hour: the recorded value IS the elapsed usage, so the
    // share is 1 — the bar must not be double-prorated.
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, until, 'hour')).toBe(1);
  });

  it('drops rows whose bucket lies outside the window', () => {
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:05:00');
    expect(bucketWindowShare('2026-08-13T12:00:00', since, until, until, 'hour')).toBe(0);
    expect(bucketWindowShare('2026-08-13T17:00:00', since, until, until, 'hour')).toBe(0);
  });

  it('divides a past boundary hour by its real coverage, not the window', () => {
    // Prev-period window [10:05, 13:05) fetched at 16:05: the 13:00 bucket's
    // recorded value covers the whole hour, so the window's share is the
    // 5 minutes inside it — dividing by the full hour, never by the window
    // length, keeps the share honest.
    const since = dayjs('2026-08-13T10:05:00');
    const until = dayjs('2026-08-13T13:05:00');
    expect(bucketWindowShare('2026-08-13T13:00:00', since, until, dayjs('2026-08-13T16:05:00'), 'hour')).toBeCloseTo(5 / 60, 10);
  });

  it('excludes the live bucket from a past calendar window (yesterday)', () => {
    // "Yesterday" ends at midnight; the 00:00 bucket holds TODAY's usage,
    // which lies entirely after the window — it must contribute nothing
    // instead of leaking a full live hour into the totals. liveExtend=false
    // is what the pages pass for past-period windows; the exclusion must not
    // depend on the fetch-time cutoff's alignment with the boundary.
    const since = dayjs('2026-08-12T00:00:00');
    const until = dayjs('2026-08-13T00:00:00');
    expect(bucketWindowShare('2026-08-13T00:00:00', since, until, dayjs('2026-08-13T16:05:00'), 'hour', false)).toBe(0);
  });

  it('keeps a bucket whose hour starts exactly at the window end when it is the LIVE bucket', () => {
    // Window ends at 16:00 sharp = the start of the CURRENT hour (cutoff
    // 16:05 lies inside it): the 16:00 bucket's recorded extent is the real
    // last in-window value, so its share is 1 — the clamped 0 would drop the
    // user's newest usage and make the line fall to 0 at its end.
    const since = dayjs('2026-08-13T13:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, dayjs('2026-08-13T16:05:00'), 'hour')).toBe(1);
    // The same bucket against a window whose end does NOT coincide with the
    // live hour's start stays dropped (past windows, mid-cell ends).
    expect(bucketWindowShare('2026-08-13T16:00:00', since, dayjs('2026-08-13T15:30:00'), dayjs('2026-08-13T16:05:00'), 'hour')).toBe(0);
  });

  it('prorates day and month boundary buckets', () => {
    const since = dayjs('2026-08-10T12:00:00');
    const until = dayjs('2026-08-13T12:00:00');
    // The 10th's usage before noon lies outside the window.
    expect(bucketWindowShare('2026-08-10T00:00:00', since, until, until, 'day')).toBeCloseTo(12 / 24, 10);
    expect(bucketWindowShare('2026-08-11T00:00:00', since, until, until, 'day')).toBe(1);
    const mSince = dayjs('2026-01-10T00:00:00');
    const mUntil = dayjs('2026-03-20T00:00:00');
    expect(bucketWindowShare('2026-01-01T00:00:00', mSince, mUntil, mUntil, 'month')).toBeCloseTo(22 / 31, 10);
    expect(bucketWindowShare('2026-02-01T00:00:00', mSince, mUntil, mUntil, 'month')).toBe(1);
  });

  it('prev and current windows never double-count the shared boundary hour', () => {
    // The 13:00 bucket is returned by BOTH the current query (since floors
    // to the containing hour) and the prev-period query (until includes it).
    // Prorated to their own windows the shares sum to exactly 1.
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:05:00');
    const prevSince = dayjs('2026-08-13T10:05:00');
    const curShare = bucketWindowShare('2026-08-13T13:00:00', since, until, until, 'hour');
    const prevShare = bucketWindowShare('2026-08-13T13:00:00', prevSince, since, until, 'hour');
    expect(curShare).toBeCloseTo(55 / 60, 10);
    expect(prevShare).toBeCloseTo(5 / 60, 10);
    expect(curShare + prevShare).toBeCloseTo(1, 10);
  });
});

describe('series — rolling windows never accumulate phantom data', () => {
  it('a 15m window shows only its own data, not the whole boundary hour', () => {
    // Uniform 1/min usage. The 15:00 bucket holds the FULL hour; only the
    // 15:50–16:00 part lies in the window. Before the fix the chart showed
    // 60 + 5 = 65 units for a 15-minute window; now it shows exactly 15.
    const since = dayjs('2026-08-13T15:50:00');
    const until = dayjs('2026-08-13T16:05:00');
    const out = series([
      { hour_bucket: '2026-08-13T15:00:00', v: 60 },
      { hour_bucket: '2026-08-13T16:00:00', v: 5 },
    ], r => r.v, since, until, until, 'hour');
    expect(out.map(p => p.value)).toEqual([10, 5]);
    expect(out.reduce((a, p) => a + p.value, 0)).toBe(15);
  });

  it('a 3h window drops the pre-window minutes of the boundary hour', () => {
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:05:00');
    const out = series([
      { hour_bucket: '2026-08-13T13:00:00', v: 60 },
      { hour_bucket: '2026-08-13T14:00:00', v: 60 },
      { hour_bucket: '2026-08-13T15:00:00', v: 60 },
      { hour_bucket: '2026-08-13T16:00:00', v: 5 },
    ], r => r.v, since, until, until, 'hour');
    expect(out.map(p => p.value)).toEqual([55, 60, 60, 5]);
  });

  it('repeated auto-refreshes grow only by real in-window usage', () => {
    // The 30s slide drops 30s of the first bucket and the current hour
    // records 0.5 more real usage — the window total must stay 180 (exactly
    // 3 hours at 1/min), never creeping with phantom boundary data.
    const rows1 = [
      { hour_bucket: '2026-08-13T13:00:00', v: 60 },
      { hour_bucket: '2026-08-13T14:00:00', v: 60 },
      { hour_bucket: '2026-08-13T15:00:00', v: 60 },
      { hour_bucket: '2026-08-13T16:00:00', v: 5 },
    ];
    const rows2 = [
      { hour_bucket: '2026-08-13T13:00:00', v: 60 },
      { hour_bucket: '2026-08-13T14:00:00', v: 60 },
      { hour_bucket: '2026-08-13T15:00:00', v: 60 },
      { hour_bucket: '2026-08-13T16:00:00', v: 5.5 },
    ];
    const w1 = series(rows1, r => r.v, dayjs('2026-08-13T13:05:00'), dayjs('2026-08-13T16:05:00'), dayjs('2026-08-13T16:05:00'), 'hour');
    const w2 = series(rows2, r => r.v, dayjs('2026-08-13T13:05:30'), dayjs('2026-08-13T16:05:30'), dayjs('2026-08-13T16:05:30'), 'hour');
    expect(w1.reduce((a, p) => a + p.value, 0)).toBeCloseTo(180, 10);
    expect(w2.reduce((a, p) => a + p.value, 0)).toBeCloseTo(180, 10);
    expect(w2[0].value).toBeCloseTo(54.5, 10);
    expect(w2[3].value).toBeCloseTo(5.5, 10);
  });

  it('prorates stacked charts and group totals with the same share', () => {
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:05:00');
    const rows = [
      { hour_bucket: '2026-08-13T13:00:00', m: 'a', n: 60 },
      { hour_bucket: '2026-08-13T14:00:00', m: 'a', n: 60 },
      { hour_bucket: '2026-08-13T15:00:00', m: 'b', n: 60 },
      { hour_bucket: '2026-08-13T16:00:00', m: 'a', n: 5 },
    ];
    const stacked = stackedData(rows, ['a', 'b'], r => r.m, r => r.n, since, until, until, 'hour');
    expect(stacked.map(r => [r.label, r.a, r.b])).toEqual([
      ['08-13 13:00', 55, 0],
      ['08-13 14:00', 60, 0],
      ['08-13 15:00', 0, 60],
      ['08-13 16:00', 5, 0],
    ]);
    const totals = groupTotals(rows, r => r.m, r => r.n, r => bucketWindowShare(r.hour_bucket, since, until, until, 'hour'));
    expect(totals).toEqual([['a', 120], ['b', 60]]);
  });
});

describe('the last chart point is the live bucket real value (hour+ axes)', () => {
  // User report: "the chart line ALWAYS falls to 0 at its end - the last
  // plotted point is 0". A current-aligned window (its until == the start of
  // the bucket that contained the window's render-time now — see hasLiveCell
  // / queryWindowUntil) was amputating the live bucket: the
  // overlap clamp read 0 for it, so the axis ended at the PREVIOUS bucket —
  // empty whenever the user's newest usage lives in the current hour/day/
  // month (the normal state when checking the dashboard). The live bucket
  // holds real recorded data (the accumulated row) and must be the chart's
  // last point.
  const NOW = dayjs('2026-08-13T16:05:30'); // Thursday

  it('a 1d window ends at the live hour with its full accumulated value', () => {
    // Window [Aug 12 16:00, Aug 13 16:00) at cutoff 16:05:30. The live hour
    // (16:00, containing cutoff) starts exactly AT until; its row v=5 is the
    // user's newest usage and must be the last point — the line must not end
    // at 15:00 (0 when the traffic lives in the current hour).
    const since = dayjs('2026-08-12T16:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    const out = series(
      [
        { hour_bucket: '2026-08-12T16:00:00', v: 10 },
        { hour_bucket: '2026-08-13T15:00:00', v: 10 },
        { hour_bucket: '2026-08-13T16:00:00', v: 5 }, // live hour
      ],
      r => r.v, since, until, NOW, 'hour',
    );
    expect(out[out.length - 1]).toMatchObject({ label: '08-13 16:00', sort: '2026-08-13 16:00', value: 5 });
    expect(out[out.length - 2].value).toBe(10);
  });

  it('a 2d window ends at the same live hour (one trailing live point only)', () => {
    const since = dayjs('2026-08-11T16:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    const out = series(
      [
        { hour_bucket: '2026-08-11T16:00:00', v: 10 },
        { hour_bucket: '2026-08-13T15:00:00', v: 10 },
        { hour_bucket: '2026-08-13T16:00:00', v: 5 },
      ],
      r => r.v, since, until, NOW, 'hour',
    );
    expect(out).toHaveLength(49); // 48 complete hours + the live hour
    expect(out[out.length - 1].value).toBe(5);
  });

  it('a 1w window ends at the LIVE day (today) with its accumulated value', () => {
    // Window [Aug 6, Aug 13) at cutoff Aug 13 16:05: today's bucket starts
    // exactly AT until. Before the fix the week chart ended at Aug 12 —
    // today's usage never appeared and the line fell to 0 whenever today
    // carried the traffic.
    const since = dayjs('2026-08-06T00:00:00');
    const until = dayjs('2026-08-13T00:00:00');
    const out = series(
      [
        { hour_bucket: '2026-08-06T10:00:00', v: 7 },
        { hour_bucket: '2026-08-12T10:00:00', v: 11 },
        { hour_bucket: '2026-08-13T08:00:00', v: 3 }, // today (live day)
        { hour_bucket: '2026-08-13T15:00:00', v: 2 }, // today, accumulated later
      ],
      r => r.v, since, until, NOW, 'day',
    );
    expect(out[out.length - 1]).toMatchObject({ label: '08-13', sort: '2026-08-13', value: 5 });
    expect(out[out.length - 2]).toMatchObject({ label: '08-12', value: 11 });
  });

  it('a 1y window ends at the LIVE month (August) with its accumulated value', () => {
    // The 1y preset snaps BOTH bounds to the month grid: [Aug 1 2025, Aug 1
    // 2026) — the live month (August 2026) starts exactly AT until.
    const since = dayjs('2025-08-01T00:00:00');
    const until = dayjs('2026-08-01T00:00:00');
    const out = series(
      [
        { hour_bucket: '2025-09-10T00:00:00', v: 6 },
        { hour_bucket: '2026-07-20T00:00:00', v: 4 },
        { hour_bucket: '2026-08-05T00:00:00', v: 9 }, // August (live month)
      ],
      r => r.v, since, until, NOW, 'month',
    );
    expect(out[out.length - 1]).toMatchObject({ label: '2026-08', sort: '2026-08', value: 9 });
    expect(out[out.length - 2]).toMatchObject({ label: '2026-07', value: 4 });
  });

  it('a fetch resolving just after a rollover still ends the chart at the window\'s live cell (Overview path)', () => {
    // The Overview path builds its axis client-side (series / bucketWindowShare
    // with the fetch-time `cutoff`). ROLLOVER RACE: the 1d window was RENDERED
    // at 16:59:59.9 (until snapped to 16:00, the hour containing the
    // render-time now) and the consumptions fetch RESOLVES at 17:00:00.05 —
    // the old live-cell gate (floorWindowUntil(cutoff) == until) floored the
    // FETCH time to 17:00 != 16:00 and dropped the cell: the chart ended at
    // 15:00 (line one bucket early, the #135 symptom) and the KPI missed the
    // COMPLETED 16:00 hour. The cell's alignment is a property of the RANGE's
    // own until (like queryWindowUntil); `cutoff` still drives only the
    // recorded-extent proration, so the completed 16:00 hour keeps its full
    // value as the chart's last point and the KPI's whole share.
    const since = dayjs('2026-08-12T16:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    const cut = dayjs('2026-08-13T17:00:00.050'); // fetch resolved one bucket later
    const out = series(
      [
        { hour_bucket: '2026-08-12T16:00:00', v: 10 },
        { hour_bucket: '2026-08-13T15:00:00', v: 10 },
        { hour_bucket: '2026-08-13T16:00:00', v: 7 }, // the render-time live hour — completed by fetch time
      ],
      r => r.v, since, until, cut, 'hour',
    );
    expect(out[out.length - 1]).toMatchObject({ label: '08-13 16:00', sort: '2026-08-13 16:00', value: 7 });
    expect(out[out.length - 2].value).toBe(10);
    // The KPI counts the same cell fully: the whole row lies inside it.
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, cut, 'hour')).toBe(1);
    // One bucket coarser: a 1w window rendered Aug 12 23:59:59.9 (until = the
    // live day Aug 12 00:00) fetched Aug 13 00:00:00.05 — the COMPLETED
    // Aug 12 day stays the chart's last point, never amputated to Aug 11.
    const dSince = dayjs('2026-08-05T00:00:00');
    const dUntil = dayjs('2026-08-12T00:00:00');
    const dCut = dayjs('2026-08-13T00:00:00.050');
    const dOut = series([{ hour_bucket: '2026-08-12T10:00:00', v: 5 }], r => r.v, dSince, dUntil, dCut, 'day');
    expect(dOut[dOut.length - 1]).toMatchObject({ label: '08-12', sort: '2026-08-12', value: 5 });
    expect(bucketWindowShare('2026-08-12T10:00:00', dSince, dUntil, dCut, 'day')).toBe(1);
  });

  it('a PAST window (yesterday shape) gains no live bucket — the boundary stays excluded', () => {
    // Yesterday [Aug 12 00:00, Aug 13 00:00) viewed at Aug 13 16:05: the
    // bucket starting AT until (Aug 13 = the live day) belongs to TODAY, not
    // to the past window — appending it would double-count today's usage in
    // both periods. Only the CURRENT-aligned window extends to the live
    // bucket; the pages pass liveExtend=false for every past-period window
    // (see liveExtensionEligible), and this test pins that same exclusion
    // explicitly instead of relying on the fetch-time cutoff's misalignment.
    const since = dayjs('2026-08-12T00:00:00');
    const until = dayjs('2026-08-13T00:00:00');
    const out = series(
      [
        { hour_bucket: '2026-08-12T10:00:00', v: 7 },
        { hour_bucket: '2026-08-13T08:00:00', v: 99 }, // today — must NOT land on this axis
      ],
      r => r.v, since, until, NOW, 'hour', false,
    );
    expect(out).toHaveLength(24);
    expect(out[out.length - 1]).toMatchObject({ label: '08-12 23:00' });
    expect(out.find(p => p.label === '08-12 10:00')!.value).toBe(7);
    expect(out.map(p => p.label)).not.toContain('08-13');
  });

  it('a PAST window whose boundary coincides with the current unit gains no live bucket (Prev Week on a Monday)', () => {
    // Prev Week [Aug 3, Aug 10) viewed ON Monday Aug 10 16:05: the bucket
    // starting AT until (Aug 10) contains the fetch time, but that day
    // belongs to THIS week, not to the past window. liveExtend=false is
    // exactly what the pages pass for past-period presets
    // (liveExtensionEligible); with it the chart must end at Aug 9.
    const since = dayjs('2026-08-03T00:00:00');
    const until = dayjs('2026-08-10T00:00:00');
    const cut = dayjs('2026-08-10T16:05:30');
    const rows = [
      { hour_bucket: '2026-08-05T10:00:00', v: 7 },
      { hour_bucket: '2026-08-10T08:00:00', v: 99 }, // the live day — NOT part of the past window
    ];
    const out = series(rows, r => r.v, since, until, cut, 'day', false);
    expect(out).toHaveLength(7);
    expect(out[out.length - 1]).toMatchObject({ label: '08-09', value: 0 });
    expect(out.find(p => p.label === '08-05')!.value).toBe(7);
    expect(out.map(p => p.label)).not.toContain('08-10');
  });

  it('a 3h-shaped min15 window ends at the LIVE 15-minute cell with the live row full value', () => {
    // Window [13:00, 16:00) at cutoff 16:05:30: the live cell [16:00, 16:15)
    // starts exactly AT until and contains the cutoff — it joins the axis
    // with the live hour row's full recorded value (the whole recorded
    // extent lies inside the cell), so the line ends at the live usage
    // instead of the previous hour's last cell.
    const since = dayjs('2026-08-13T13:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    const cut = dayjs('2026-08-13T16:05:30');
    const out = series(
      [
        { hour_bucket: '2026-08-13T13:00:00', v: 180 },
        { hour_bucket: '2026-08-13T15:00:00', v: 60 },
        { hour_bucket: '2026-08-13T16:00:00', v: 10 }, // live hour
      ],
      r => r.v, since, until, cut, 'min15',
    );
    expect(out).toHaveLength(13); // 12 complete cells + the live cell
    expect(out[out.length - 1]).toMatchObject({ label: '08-13 16:00', sort: '2026-08-13 16:00', value: 10 });
    // The whole window's recorded usage: 180 + 60 + the full live row.
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(180 + 60 + 10, 6);
  });

  it('the KPI and the chart agree on the non-hour-aligned live cell (3h at 16:17)', () => {
    // The 3h window's normal state: until = a non-hour 15-min cell (16:15),
    // cutoff 2 minutes later. The chart appends [16:15, 16:30) and puts the
    // live row's [16:15, 16:17) slice there; bucketWindowShare must count
    // the same slice (17/17 of the row), not clamp it at until (15/17) and
    // never over-count at the hour-boundary minute either.
    const since = dayjs('2026-08-13T13:00:00');
    const until = dayjs('2026-08-13T16:15:00');
    const cut = dayjs('2026-08-13T16:17:30');
    const rows = [{ hour_bucket: '2026-08-13T16:00:00', v: 17 }];
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, cut, 'min15')).toBe(1);
    const out = series(rows, r => r.v, since, until, cut, 'min15');
    expect(out[out.length - 1]).toMatchObject({ label: '08-13 16:15', value: 2 });
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(17, 6);
  });

  it('the boundary minute (snapped until == HH:00:00) never over-counts the live hour', () => {
    // Within the first minute of the hour the snapped until is HH:00:00 and
    // the live cell has NO recorded whole minute yet — hasLiveCell's
    // extent gate suppresses it, so neither the chart nor the KPI may count
    // the live hour's row (the old hour-grid check would have shown the KPI
    // 35 vs the chart 30 — the "always accumulating" phantom).
    const since = dayjs('2026-08-13T15:30:00');
    const until = dayjs('2026-08-13T16:00:00');
    const cut = dayjs('2026-08-13T16:00:30');
    const rows = [
      { hour_bucket: '2026-08-13T15:00:00', v: 60 }, // the 15:00 hour — 30 of its minutes are in-window
      { hour_bucket: '2026-08-13T16:00:00', v: 5 }, // the live hour — nothing recorded yet this minute
    ];
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, cut, 'minute')).toBe(0);
    const out = series(rows, r => r.v, since, until, cut, 'minute');
    expect(out).toHaveLength(30);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(30, 6);
  });

  it('resampleResponse re-samples the live min15 cell as the last bucket (summary.value = the live value)', () => {
    // The Trends 3h current-period response carries the live hour bucket;
    // the client resample must end at the live cell with its recorded value,
    // and summary.value (the last bucket) must read that value — never 0.
    const resp = {
      metric: 'spend', group_by: 'model', rollup: 'hour',
      series: [
        { bucket: '2026-08-13 15:00', group: 'a', value: 120, is_zero: false },
        { bucket: '2026-08-13 16:00', group: 'a', value: 10, is_zero: false },
        { bucket: '2026-08-13 16:00', group: 'b', value: 5, is_zero: false },
      ],
      summary: [],
      buckets: ['2026-08-13 15:00', '2026-08-13 16:00'],
      totals: { spend: 130, tokens: 0, requests: 0, cache: 0 },
    } as any;
    const since = dayjs('2026-08-13T13:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    const out = resampleResponse(resp, since, until, dayjs('2026-08-13T16:05:30'), 'min15');
    expect(out.buckets).toHaveLength(13);
    expect(out.buckets[12]).toBe('08-13 16:00'); // the live cell
    const a = out.series.filter(p => p.group === 'a').map(p => p.value);
    const b = out.series.filter(p => p.group === 'b').map(p => p.value);
    expect(a[12]).toBe(10);
    expect(b[12]).toBe(5);
    expect(out.summary.find(s => s.group === 'a')!.value).toBe(10);
    expect(out.summary.find(s => s.group === 'b')!.value).toBe(5);
    // The live hour's ROW is fully inside the live cell: 10 of the 120+10.
    expect(out.summary.find(s => s.group === 'a')!.sum).toBeCloseTo(120 + 10, 6);
    expect(out.totals.spend).toBeCloseTo(130 + 5, 6);
  });

  it('liveExtensionEligible splits the presets: current-period keys only', () => {
    for (const k of ['15m', '30m', '1h', '3h', '1d', '2d', '1w', '1mo', '1y', 'today', 'week', 'month', 'year']) {
      expect(liveExtensionEligible({ key: k })).toBe(true);
    }
    for (const k of ['yesterday', 'prevweek', 'prevmonth', 'prevyear', CUSTOM_KEY]) {
      expect(liveExtensionEligible({ key: k })).toBe(false);
    }
  });

  it('a prevyear range still excludes the live month (key gate)', () => {
    // The prevyear until (Jan 1) lies exactly on the month grid — without
    // the current-period key gate the range would pass it as-is and absorb
    // the live month into the previous year; the gate keeps the boundary
    // excluded.
    const r = { key: 'prevyear', label: '', badge: '', since: dayjs('2025-01-01T00:00:00'), until: dayjs('2026-01-01T00:00:00'), granularity: 'month' as Granularity };
    expect(queryWindowUntil(r, 'month').format('YYYY-MM-DD HH:mm:ss')).toBe('2025-12-31 23:59:59');
  });

  it('a prevmonth range still excludes the live day (key gate)', () => {
    const r = { key: 'prevmonth', label: '', badge: '', since: dayjs('2026-07-01T00:00:00'), until: dayjs('2026-08-01T00:00:00'), granularity: 'day' as Granularity };
    expect(queryWindowUntil(r, 'day').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-07-31 23:59:59');
  });

  it('a mid-cell window START also suppresses the live cell (the since-alignment gate)', () => {
    // The live extension is a property of the SNAPPED preset shape: a window
    // whose since starts off the bucket grid (an un-snapped custom pick that
    // happens to end on the live boundary) keeps the clean half-open axis.
    const since = dayjs('2026-08-12T09:15:00'); // mid-hour
    const until = dayjs('2026-08-13T09:00:00');
    const cut = dayjs('2026-08-13T09:05:30');
    const r = { key: '1d', label: '', badge: '', since, until, granularity: 'hour' as Granularity };
    expect(queryWindowUntil(r, 'hour').format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 08:59:59');
    const out = series([{ hour_bucket: '2026-08-13T09:00:00', v: 5 }], x => x.v, since, until, cut, 'hour');
    expect(out.map(p => p.label)).not.toContain('08-13 09:00');
    expect(out[out.length - 1]).toMatchObject({ label: '08-13 08:00' });
  });

  it('bucketWindowShare gives the live row its recorded share (1) for the CURRENT window and 0 for any PAST window', () => {
    const since = dayjs('2026-08-12T16:00:00');
    const until = dayjs('2026-08-13T16:00:00');
    // The live hour's row: its recorded extent (16:00..16:05, whole minutes
    // floored) is real in-window usage — the share is 1, never the clamped 0
    // that dropped it from the chart and the KPI.
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, NOW, 'hour')).toBe(1);
    // The SAME row must not leak into a past window (the previous period) —
    // conservation: current 1 + past 0 = the whole row, never double-counted.
    // Past-window shares pass liveExtend=false (the Overview's prev sums).
    const prevSince = dayjs('2026-08-11T16:00:00');
    expect(bucketWindowShare('2026-08-13T16:00:00', prevSince, since, NOW, 'hour', false)).toBe(0);
    // A live DAY row for a week-shaped window: included in the CURRENT
    // window, excluded from the PREVIOUS one.
    expect(bucketWindowShare('2026-08-13T08:00:00', dayjs('2026-08-06T00:00:00'), dayjs('2026-08-13T00:00:00'), NOW, 'day')).toBe(1);
    expect(bucketWindowShare('2026-08-13T08:00:00', dayjs('2026-07-30T00:00:00'), dayjs('2026-08-06T00:00:00'), NOW, 'day', false)).toBe(0);
  });
});

describe('stackedData', () => {
  it('zero-fills every group per bucket on a continuous axis', () => {
    const since = dayjs('2026-08-13T09:00:00');
    const until = dayjs('2026-08-13T11:00:00');
    const rows = [
      { hour_bucket: '2026-08-13T09:00:00', m: 'a', n: 2 },
      { hour_bucket: '2026-08-13T10:00:00', m: 'b', n: 3 },
      { hour_bucket: '2026-08-13T10:30:00', m: 'b', n: 1 },
    ];
    const out = stackedData(rows, ['a', 'b'], r => r.m, r => r.n, since, until, until, 'hour');
    expect(out).toEqual([
      { label: '08-13 09:00', sort: '2026-08-13 09:00', a: 2, b: 0 },
      { label: '08-13 10:00', sort: '2026-08-13 10:00', a: 0, b: 4 },
    ]);
  });

  it('accumulates rows whose group is outside the requested list (folded into Other by the caller) without NaN', () => {
    const since = dayjs('2026-08-13T09:00:00');
    const until = dayjs('2026-08-13T10:00:00');
    const rows = [
      { hour_bucket: '2026-08-13T09:00:00', m: 'top1', n: 1 },
      { hour_bucket: '2026-08-13T09:00:00', m: 'tail', n: 4 },   // beyond top-N
      { hour_bucket: '2026-08-13T09:30:00', m: 'tail', n: 1 },
    ];
    const out = stackedData(rows, ['top1', 'Other'], r => r.m, r => r.n, since, until, until, 'hour');
    // The caller folds 'tail' into Other: total must stay 6, never NaN.
    let other = 0;
    for (const [g, v] of Object.entries(out[0])) {
      if (g !== 'label' && g !== 'sort' && g !== 'top1') other += v as number;
    }
    expect(out[0].top1).toBe(1);
    expect(other).toBe(5);
    expect(Number.isNaN(out[0].tail)).toBe(false);
  });
});

describe('bucketAxis', () => {
  it('produces sortable continuous buckets for each granularity', () => {
    const since = dayjs('2026-08-13T09:00:00');
    const hour = bucketAxis(since, since.add(2, 'hour'), 'hour');
    // [since, until): the 11:00 bucket starting AT until is excluded.
    expect(hour.map(p => p.sort)).toEqual(['2026-08-13 09:00', '2026-08-13 10:00']);
    const month = bucketAxis(dayjs('2026-01-01'), dayjs('2026-03-31'), 'month');
    expect(month.map(p => p.label)).toEqual(['2026-01', '2026-02', '2026-03']);
  });

  it('builds exactly the 15 minute buckets inside a 15m window — no trailing empty bucket', () => {
    // The window is [since, until): a bucket starting AT until (16:05) can
    // never receive data (the overlap clamp yields 0), so the axis holds
    // exactly the 15 complete buckets 15:50..16:04. Before the fix the loop
    // also emitted 16:05 — 16 ticks for 15 minutes of data, the last always 0.
    const out = bucketAxis(dayjs('2026-08-13T15:50:00'), dayjs('2026-08-13T16:05:00'), 'minute');
    expect(out).toHaveLength(15);
    expect(out[0]).toMatchObject({ label: '08-13 15:50', sort: '2026-08-13 15:50', value: 0 });
    expect(out[14]).toMatchObject({ label: '08-13 16:04', sort: '2026-08-13 16:04', value: 0 });
    expect(out.map(p => p.label)).toEqual([
      '08-13 15:50', '08-13 15:51', '08-13 15:52', '08-13 15:53', '08-13 15:54',
      '08-13 15:55', '08-13 15:56', '08-13 15:57', '08-13 15:58', '08-13 15:59',
      '08-13 16:00', '08-13 16:01', '08-13 16:02', '08-13 16:03', '08-13 16:04',
    ]);
  });

  it('builds exactly the 12 fifteen-minute buckets inside a snapped 3h window', () => {
    // The 3h preset snaps BOTH bounds to the 15-minute grid: [13:00, 16:00).
    // The 16:00 bucket can never receive data — 13 ticks would be 3 hours of
    // data plus a phantom period. The axis holds 13:00..15:45, 12 buckets.
    const out = bucketAxis(dayjs('2026-08-13T13:00:00'), dayjs('2026-08-13T16:00:00'), 'min15');
    expect(out.map(p => p.label)).toEqual([
      '08-13 13:00', '08-13 13:15', '08-13 13:30', '08-13 13:45',
      '08-13 14:00', '08-13 14:15', '08-13 14:30', '08-13 14:45',
      '08-13 15:00', '08-13 15:15', '08-13 15:30', '08-13 15:45',
    ]);
  });

  it('keeps every bucket whose start lies inside a mid-cell until (custom ranges)', () => {
    // Custom ranges are never snapped: until = 16:05:30 keeps the 16:05
    // bucket (it overlaps the window by 30 s), only 16:06 and later are
    // excluded.
    const out = bucketAxis(dayjs('2026-08-13T15:59:00'), dayjs('2026-08-13T16:05:30'), 'minute');
    expect(out.map(p => p.label)).toEqual([
      '08-13 15:59', '08-13 16:00', '08-13 16:01', '08-13 16:02',
      '08-13 16:03', '08-13 16:04', '08-13 16:05',
    ]);
  });

  it('yields an empty axis for a zero-length window on the bucket grid', () => {
    // e.g. the 'today' preset loaded within the first second of midnight:
    // since and the floored until are the same bucket start, so no bucket
    // lies inside the half-open window — an empty axis, never one zero tick.
    const t = dayjs('2026-08-13T16:00:00');
    expect(bucketAxis(t, t, 'minute')).toEqual([]);
    expect(bucketAxis(t, t, 'hour')).toEqual([]);
  });

  it('floors the 15-minute axis to the clock cell (never 13:07, 13:22, ...)', () => {
    const out = bucketAxis(dayjs('2026-08-13T13:07:00'), dayjs('2026-08-13T14:05:00'), 'min15');
    expect(out.map(p => p.label)).toEqual([
      '08-13 13:00', '08-13 13:15', '08-13 13:30', '08-13 13:45',
      '08-13 14:00',
    ]);
  });
});

describe('series — sub-hour distribution of hourly rows', () => {
  // Consumption rows are hourly (hour_bucket); a sub-hour axis distributes
  // each row's value over the minute buckets overlapping its hour,
  // proportionally to the overlap (uniform-within-hour assumption). The
  // current hour's row only covers up to `until`, so its rate is higher.
  const since = dayjs('2026-08-13T15:50:00');
  const until = dayjs('2026-08-13T16:05:00');

  it('splits hourly totals across overlapping minute buckets, preserving window totals', () => {
    const out = series(
      [
        { hour_bucket: '2026-08-13T15:00:00', v: 120 },  // full hour 15:00-16:00
        { hour_bucket: '2026-08-13T16:00:00', v: 10 },   // current hour, only 5 min so far
      ],
      (r) => r.v, since, until, until, 'minute',
    );
    // 15:50-15:59 get 120/60 = 2 each; 16:00-16:04 get 10/5 = 2 each. The
    // bucket starting AT until (16:05) is excluded from the axis — it can
    // never hold data — so the window keeps exactly its 15 buckets.
    expect(out.map(p => p.value)).toEqual([
      2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
      2, 2, 2, 2, 2,
    ]);
    // Window total = the hour's share overlapping the window + the partial hour.
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(120 * (10 / 60) + 10, 6);
  });

  it('drops rows whose hour has no overlap with the window', () => {
    const out = series(
      [
        { hour_bucket: '2026-08-13T14:00:00', v: 500 }, // entirely before the window
        { hour_bucket: '2026-08-13T15:00:00', v: 60 },
      ],
      (r) => r.v, since, until, until, 'minute',
    );
    expect(out.map(p => p.value)).toEqual([1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0]);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(60 * (10 / 60), 6);
  });

  it('distributes onto 15-minute buckets for a 3-hour window', () => {
    const until = dayjs('2026-08-13T15:59:59');
    const out = series(
      [
        { hour_bucket: '2026-08-13T13:00:00', v: 180 },
        { hour_bucket: '2026-08-13T14:00:00', v: 60 },
      ],
      (r) => r.v,
      dayjs('2026-08-13T13:00:00'), until, until, 'min15',
    );
    // 13:00 hour -> 45 per 15-min bucket (x4), 14:00 hour -> 15 per bucket (x4).
    expect(out.map(p => p.value)).toEqual([45, 45, 45, 45, 15, 15, 15, 15, 0, 0, 0, 0]);
  });

  it('divides a PAST window boundary hour by its real coverage (cutoff), not the window end', () => {
    // The previous period ends at 10:10, but its 10:00 boundary bucket holds
    // usage recorded up to 10:30 (cutoff = fetch time). Dividing by the
    // window end would inflate 120 into every minute of 10:00-10:10; the
    // real coverage is 30 minutes, so only 40 of the 120 belong to the window.
    // liveExtend=false: this is a PAST window (the pages never extend past
    // windows), and the boundary bucket's exclusion must not depend on the
    // fetch-time cutoff's alignment with the window end.
    const since = dayjs('2026-08-13T10:00:00');
    const until = dayjs('2026-08-13T10:10:00');
    const cutoff = dayjs('2026-08-13T10:30:00');
    const out = series([{ hour_bucket: '2026-08-13T10:00:00', v: 120 }], (r) => r.v, since, until, cutoff, 'minute', false);
    // The 10:10 bucket starts AT until — excluded, like every trailing bucket.
    expect(out.map(p => p.value)).toEqual([4, 4, 4, 4, 4, 4, 4, 4, 4, 4]);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(120 * (10 / 30), 6);
  });
});

describe('sub-hour coverage — stable between refreshes (cutoff semantics)', () => {
  // A 'Past 15 Minutes' window lying INSIDE one hour: the 14:00 row's value
  // is a per-request accumulation over [14:00, fetchTime). Dividing by the
  // raw fetch time makes every 30s auto-refresh divide the SAME row value by
  // a LARGER coverage — the window's total shrinks on every refresh while
  // the row is unchanged (verified decay 2.5777 -> 2.5160 -> 2.4571 at
  // 14:20:22 / 14:20:52 / 14:21:22), violating the 'perfectly stable between
  // refreshes' contract (Activity.tsx). The coverage denominator reads the
  // row's recorded extent as WHOLE MINUTES (cutoff floored to the minute
  // grid): an unchanged row renders identical values between refreshes, and
  // a snapped preset window's until IS the floored cutoff, so its coverage
  // is exactly its own span.
  const since = dayjs('2026-08-13T14:05:00');
  const until = dayjs('2026-08-13T14:20:00');
  const row = { hour_bucket: '2026-08-13T14:00:00', v: 3.5 };

  it('a window inside one hour renders stable per-bucket values (stable-value case)', () => {
    // Coverage = 20 whole minutes (until − hour start), so every in-window
    // minute bucket carries 3.5/20 = 0.175 and the total is exactly 15/20 of
    // the row — never the raw-cutoff share (2.5777 at 14:20:22, which
    // shrank further on every refresh).
    const out = series([row], r => r.v, since, until, dayjs('2026-08-13T14:20:22'), 'minute');
    // 14:05..14:19 — the bucket starting AT until (14:20) is excluded from
    // the half-open axis.
    expect(out).toHaveLength(15);
    expect(out.every(p => Math.abs(p.value - 0.175) < 1e-12)).toBe(true);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(3.5 * (15 / 20), 10);
  });

  it('an unchanged row renders identical values between 30s refreshes', () => {
    // Two refreshes 30s apart with the SAME row and the SAME window: the raw
    // cutoff grows (14:20:22 -> 14:20:52) but the whole-minute coverage does
    // not, so every bar, the window total and the KPI share stay identical.
    const r1 = series([row], r => r.v, since, until, dayjs('2026-08-13T14:20:22'), 'minute');
    const r2 = series([row], r => r.v, since, until, dayjs('2026-08-13T14:20:52'), 'minute');
    expect(r1.map(p => p.value)).toEqual(r2.map(p => p.value));
    expect(r1.reduce((a, p) => a + p.value, 0)).toBeCloseTo(2.625, 10);
    expect(r2.reduce((a, p) => a + p.value, 0)).toBeCloseTo(2.625, 10);
    // The KPI cards use bucketWindowShare with the same cutoff — stable too.
    const s1 = bucketWindowShare('2026-08-13T14:00:00', since, until, dayjs('2026-08-13T14:20:22'), 'minute');
    const s2 = bucketWindowShare('2026-08-13T14:00:00', since, until, dayjs('2026-08-13T14:20:52'), 'minute');
    expect(s1).toBeCloseTo(15 / 20, 10);
    expect(s2).toBe(s1);
  });

  it('keeps the shared boundary hour conserved between the prev and current windows', () => {
    // Both the prev window [13:50, 14:05) and the current window [14:05,
    // 14:20) receive the same floored coverage for the shared 14:00 row, so
    // their shares still sum to exactly 1 — the fix must not double-count
    // (clamping coverage to each window's own until would split the row
    // 5/5 + 15/20 = 1.75). The PREV share is a past-period share: it passes
    // liveExtend=false exactly like the pages do (see liveExtensionEligible)
    // — its boundary cell must stay excluded even though its until lies on
    // the minute grid and the fetch-time cutoff has long passed it.
    const cur = bucketWindowShare('2026-08-13T14:00:00', since, until, dayjs('2026-08-13T14:20:22'), 'minute');
    const prev = bucketWindowShare('2026-08-13T14:00:00', dayjs('2026-08-13T13:50:00'), since, dayjs('2026-08-13T14:20:22'), 'minute', false);
    expect(cur).toBeCloseTo(15 / 20, 10);
    expect(prev).toBeCloseTo(5 / 20, 10);
    expect(cur + prev).toBeCloseTo(1, 10);
  });

  it('a window sliding to the next minute re-anchors cleanly instead of decaying', () => {
    // At 14:21:22 the snapped window slid to [14:06, 14:21): its coverage is
    // 21 whole minutes, so the total is 3.5 × 15/21 = 2.5 — NOT the raw
    // cutoff's 2.4571 (which read a 21.37-minute extent and shrank the
    // unchanged row's share).
    const out = series([row], r => r.v, dayjs('2026-08-13T14:06:00'), dayjs('2026-08-13T14:21:00'), dayjs('2026-08-13T14:21:22'), 'minute');
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(3.5 * (15 / 21), 10);
  });

  it('a row recorded within its first minute is not dropped by the whole-minute floor', () => {
    // cutoff 14:00:30 floors to 14:00:00 = the row's own start, which would
    // read zero recorded minutes and drop a row that demonstrably holds
    // data; the floor reads at least the first whole minute there, so the
    // row's value lands on its first bucket (the pre-fix behavior).
    const out = series(
      [{ hour_bucket: '2026-08-13T14:00:00', v: 2 }], r => r.v,
      dayjs('2026-08-13T14:00:00'), dayjs('2026-08-13T14:10:00'), dayjs('2026-08-13T14:00:30'), 'minute',
    );
    expect(out[0].value).toBe(2);
    expect(out.reduce((a, p) => a + p.value, 0)).toBe(2);
  });
});

describe('stackedData — sub-hour distribution per group', () => {
  it('distributes each group over the minute buckets without dropping or NaN', () => {
    const since = dayjs('2026-08-13T15:50:00');
    const until = dayjs('2026-08-13T16:00:00');
    const rows = [
      { hour_bucket: '2026-08-13T15:00:00', m: 'a', n: 120 },
      { hour_bucket: '2026-08-13T15:00:00', m: 'b', n: 60 },
      { hour_bucket: '2026-08-13T16:00:00', m: 'a', n: 10 },
    ];
    const out = stackedData(rows, ['a', 'b'], r => r.m, r => r.n, since, until, until, 'minute');
    // 15:50-15:59: a=2, b=1 each; the 16:00 bucket starts AT until — excluded.
    expect(out.map(r => ({ a: r.a, b: r.b }))).toEqual([
      { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 },
      { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 },
    ]);
    expect(Number.isNaN(out[0].a)).toBe(false);
  });
});

describe('resampleResponse — Trends hourly rollup onto a sub-hour axis', () => {
  // The server rolls up at most hourly; Trends asks for rollup=hour and
  // re-samples the response onto the client's minute axis.
  const resp = {
    metric: 'spend', group_by: 'model', rollup: 'hour',
    series: [
      { bucket: '2026-08-13 15:00', group: 'a', value: 120, is_zero: false },
      { bucket: '2026-08-13 15:00', group: 'b', value: 0, is_zero: true },
      { bucket: '2026-08-13 16:00', group: 'a', value: 10, is_zero: false },
      { bucket: '2026-08-13 16:00', group: 'b', value: 5, is_zero: false },
    ],
    summary: [{ group: 'a', min: 1, max: 2, avg: 3, sum: 130, value: 10, percent: 0 }],
    buckets: ['2026-08-13 15:00', '2026-08-13 16:00'],
    totals: { spend: 135, tokens: 0, requests: 0, cache: 0 },
  };

  it('re-buckets every group onto the minute axis with is_zero flags', () => {
    const out = resampleResponse(resp, dayjs('2026-08-13T15:50:00'), dayjs('2026-08-13T16:05:00'), dayjs('2026-08-13T16:05:00'), 'minute');
    expect(out.buckets).toHaveLength(15);
    expect(out.buckets[0]).toBe('08-13 15:50');
    expect(out.buckets[14]).toBe('08-13 16:04'); // the 16:05 bucket (start == until) is excluded
    // 15 buckets x 2 groups, all zero-filled like the server does.
    expect(out.series).toHaveLength(30);
    const a = out.series.filter(p => p.group === 'a').map(p => p.value);
    const b = out.series.filter(p => p.group === 'b').map(p => p.value);
    expect(a).toEqual([2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2]);  // 15:00 (120/60) + 16:00 (10/5)
    expect(b).toEqual([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1]);
    expect(out.series.every(p => p.is_zero === (p.value === 0))).toBe(true);
    // Summary is recomputed from the re-bucketed series (server semantics:
    // min/max/avg over non-empty buckets, value = last bucket, percent of total).
    const sa = out.summary.find(s => s.group === 'a')!;
    const sb = out.summary.find(s => s.group === 'b')!;
    expect(sa.sum).toBeCloseTo(30, 6);
    expect(sb.sum).toBeCloseTo(5, 6);
    expect(sa.min).toBeCloseTo(2, 6);
    expect(sa.max).toBeCloseTo(2, 6);
    expect(sa.avg).toBeCloseTo(2, 6);
    // value = the LAST in-window bucket (16:04): before the fix the trailing
    // 16:05 bucket forced it to 0 for every sub-hour window.
    expect(sa.value).toBe(2);
    expect(sb.value).toBe(1);
    expect(sa.percent).toBeCloseTo(30 / 35 * 100, 6);
    // The re-sampled metric's total matches the series; the others carry over.
    expect(out.totals.spend).toBeCloseTo(35, 6);
    expect(out.totals.tokens).toBe(0);
    expect(out.totals.requests).toBe(0);
    expect(out.totals.cache).toBe(0);
  });

  it('keeps rollup/resp shape fields for chart consumption', () => {
    const out = resampleResponse(resp, dayjs('2026-08-13T15:50:00'), dayjs('2026-08-13T16:05:00'), dayjs('2026-08-13T16:05:00'), 'minute');
    expect(out.metric).toBe('spend');
    expect(out.group_by).toBe('model');
  });

  it('preserves summary rows for groups beyond the server top-N (not in the series grid)', () => {
    // The current-period response only carries top-5 + Other series, but its
    // summary lists every group; dropped groups must keep their original
    // (raw) row so Trends deltas don't read them as zero and show -100%.
    const withTail = {
      ...resp,
      summary: [
        ...resp.summary,
        { group: 'tail-model', min: 9, max: 9, avg: 9, sum: 9, value: 9, percent: 4 },
      ],
    };
    const out = resampleResponse(withTail, dayjs('2026-08-13T15:50:00'), dayjs('2026-08-13T16:05:00'), dayjs('2026-08-13T16:05:00'), 'minute');
    const tail = out.summary.find(s => s.group === 'tail-model')!;
    expect(tail).toEqual({ group: 'tail-model', min: 9, max: 9, avg: 9, sum: 9, value: 9, percent: 4 });
    // Every group appears exactly once: the 2 resampled grid groups plus the
    // preserved tail row.
    expect(out.summary).toHaveLength(3);
    expect(new Set(out.summary.map(s => s.group)).size).toBe(3);
    // The resampled groups still get recomputed rows.
    expect(out.summary.find(s => s.group === 'a')!.sum).toBeCloseTo(30, 6);
  });

  it('re-buckets onto a clock-aligned 15-minute axis from a mid-cell window start', () => {
    const hourResp = {
      ...resp,
      series: [
        { bucket: '2026-08-13 13:00', group: 'a', value: 180, is_zero: false },
        { bucket: '2026-08-13 14:00', group: 'a', value: 60, is_zero: false },
        { bucket: '2026-08-13 15:00', group: 'a', value: 120, is_zero: false },
      ],
      buckets: ['2026-08-13 13:00', '2026-08-13 14:00', '2026-08-13 15:00'],
    };
    const since = dayjs('2026-08-13T13:07:00');
    const until = dayjs('2026-08-13T16:00:00');
    const out = resampleResponse(hourResp, since, until, until, 'min15');
    expect(out.buckets).toEqual([
      '08-13 13:00', '08-13 13:15', '08-13 13:30', '08-13 13:45',
      '08-13 14:00', '08-13 14:15', '08-13 14:30', '08-13 14:45',
      '08-13 15:00', '08-13 15:15', '08-13 15:30', '08-13 15:45',
    ]);
    // 13:00 hour: 53 of its 60 minutes fall in the window (13:07-14:00)
    // -> 24 + 45x3; 14:00 hour: 15 x4; 15:00 hour: 30 x4. The 16:00 bucket
    // starts AT until — excluded, and the totals are unchanged.
    expect(out.series.map(p => p.value)).toEqual([24, 45, 45, 45, 15, 15, 15, 15, 30, 30, 30, 30]);
    expect(out.series.reduce((a, p) => a + p.value, 0)).toBeCloseTo(180 * (53 / 60) + 60 + 120, 6);
  });
});

describe('prorateBoundaryBuckets — server-bucketed custom ranges', () => {
  // The exact shape the activity endpoint returns for a custom range whose
  // bounds cut mid-bucket: the server widened the query window to the
  // buckets CONTAINING the raw bounds (activityWindow in admin.go) and
  // summed the FULL boundary buckets into the response.
  const hourResp = (vals: number[], buckets = ['2026-08-13 14:00', '2026-08-13 15:00', '2026-08-13 16:00', '2026-08-13 17:00', '2026-08-13 18:00']): ActivityResponse => ({
    metric: 'spend', group_by: 'model', rollup: 'hour',
    series: [
      ...buckets.map((b, i) => ({ bucket: b, group: 'a', value: vals[i], is_zero: vals[i] === 0 })),
      ...buckets.map((b, i) => ({ bucket: b, group: 'b', value: vals[i] / 2, is_zero: vals[i] === 0 })),
    ],
    summary: [
      { group: 'a', min: 60, max: 60, avg: 60, sum: 300, value: 60, percent: 66.7 },
      { group: 'b', min: 30, max: 30, avg: 30, sum: 150, value: 30, percent: 33.3 },
    ],
    buckets,
    totals: { spend: 450, tokens: 0, requests: 0, cache: 0 },
  });

  it('prorates the boundary buckets of a 14:37-18:22 hour range and matches the Overview computation', () => {
    const since = dayjs('2026-08-13T14:37:00');
    const until = dayjs('2026-08-13T18:22:00');
    const cutoff = dayjs('2026-08-13T19:00:00');
    const out = prorateBoundaryBuckets(hourResp([60, 60, 60, 60, 60]), since, until, until, cutoff, 'hour', 'hour');
    // First bucket [14:00, 15:00): only [14:37, 15:00) = 23 min inside the
    // window; last [18:00, 19:00): [18:00, 18:22) = 22 min. Interior buckets
    // keep their full values.
    expect(out.series.filter(p => p.group === 'a').map(p => p.value)).toEqual([23, 60, 60, 60, 22]);
    expect(out.series.filter(p => p.group === 'b').map(p => p.value)).toEqual([11.5, 30, 30, 30, 11]);
    // Summary rebuilt with server semantics over the prorated buckets.
    const a = out.summary.find(s => s.group === 'a')!;
    const b = out.summary.find(s => s.group === 'b')!;
    expect(a.sum).toBeCloseTo(225, 10);
    expect(a.min).toBe(22);
    expect(a.max).toBe(60);
    expect(a.avg).toBe(45);
    expect(a.value).toBe(22); // last bucket, like the chart's last bar
    expect(a.percent).toBeCloseTo((225 / 337.5) * 100, 10);
    expect(b.sum).toBeCloseTo(112.5, 10);
    expect(out.totals.spend).toBeCloseTo(337.5, 10);
    // The Overview flow prorates the SAME boundary rows with
    // bucketWindowShare (which reads them directly); the prorated response
    // must agree exactly with that per-row computation.
    const hours = ['2026-08-13T14:00:00', '2026-08-13T15:00:00', '2026-08-13T16:00:00', '2026-08-13T17:00:00', '2026-08-13T18:00:00'];
    const overviewA = hours.reduce((acc, h) => acc + 60 * bucketWindowShare(h, since, until, cutoff, 'hour', false), 0);
    const overviewB = hours.reduce((acc, h) => acc + 30 * bucketWindowShare(h, since, until, cutoff, 'hour', false), 0);
    expect(a.sum).toBeCloseTo(overviewA, 10);
    expect(b.sum).toBeCloseTo(overviewB, 10);
    expect(out.totals.spend).toBeCloseTo(overviewA + overviewB, 10);
  });

  it('leaves a grid-aligned custom range untouched (the query already excluded the boundary buckets)', () => {
    const since = dayjs('2026-08-13T14:00:00');
    const until = dayjs('2026-08-13T18:00:00');
    // queryWindowUntil sends exclusiveUntil (17:59:59) for an aligned custom
    // end, so the widened server window ends exactly at 18:00 — the
    // response carries only full in-window buckets.
    const untilSent = queryWindowUntil(
      { key: CUSTOM_KEY, since, until, granularity: 'hour' as Granularity },
      'hour',
    );
    expect(untilSent.format('YYYY-MM-DD HH:mm:ss')).toBe('2026-08-13 17:59:59');
    const r = hourResp([60, 60, 60, 60], ['2026-08-13 14:00', '2026-08-13 15:00', '2026-08-13 16:00', '2026-08-13 17:00']);
    const out = prorateBoundaryBuckets(r, since, until, untilSent, dayjs('2026-08-13T19:00:00'), 'hour', 'hour');
    expect(out).toBe(r);
  });

  it('prorates the Explore equal-rollup case (day-granularity custom, day rollup)', () => {
    const since = dayjs('2026-08-10T14:00:00');
    const until = dayjs('2026-08-14T12:30:00');
    expect(granularityFor(since, until)).toBe('day');
    const buckets = ['2026-08-10', '2026-08-11', '2026-08-12', '2026-08-13', '2026-08-14'];
    const r: ActivityResponse = {
      metric: 'spend', group_by: 'model', rollup: 'day',
      series: buckets.map(b => ({ bucket: b, group: 'a', value: 48, is_zero: false })),
      summary: [{ group: 'a', min: 48, max: 48, avg: 48, sum: 240, value: 48, percent: 100 }],
      buckets,
      totals: { spend: 240, tokens: 0, requests: 0, cache: 0 },
    };
    const out = prorateBoundaryBuckets(r, since, until, until, dayjs('2026-08-15T10:00:00'), 'day', 'day');
    // First day [Aug 10 00:00, Aug 11 00:00): [14:00, 24:00) = 10/24; last
    // day: [00:00, 12:30) = 12.5/24 (the fetch on Aug 15 means the boundary
    // days' rows are complete); interior days unchanged.
    expect(out.series.map(p => p.value)).toEqual([20, 48, 48, 48, 25]);
    expect(out.summary[0].sum).toBeCloseTo(189, 10);
    expect(out.summary[0].value).toBe(25);
    expect(out.summary[0].min).toBe(20);
    expect(out.totals.spend).toBeCloseTo(189, 10);
  });

  it('leaves coarser AND finer rollups untouched (accepted residual behavior)', () => {
    const since = dayjs('2026-08-13T14:37:00');
    const until = dayjs('2026-08-13T18:22:00');
    const r = hourResp([60, 60, 60, 60, 60]);
    const cutoff = dayjs('2026-08-13T19:00:00');
    // Explore's day/week/total rollup over an hour-granularity range: whole
    // boundary days in the boundary bars by design — never prorated.
    expect(prorateBoundaryBuckets(r, since, until, until, cutoff, 'hour', 'day')).toBe(r);
    expect(prorateBoundaryBuckets(r, since, until, until, cutoff, 'hour', 'week')).toBe(r);
    expect(prorateBoundaryBuckets(r, since, until, until, cutoff, 'hour', 'total')).toBe(r);
    // A FINER rollup (hour over a day-granularity range) overcounts its own
    // hourly boundary bars the same way but is outside this fix's scope —
    // the response must stay exactly as the server returned it. The bounds
    // are MID-DAY so the first/last-bucket conditions would fire if the
    // gate were removed — this pins the gate itself.
    const daySince = dayjs('2026-08-10T14:00:00');
    const dayUntil = dayjs('2026-08-14T12:30:00');
    expect(prorateBoundaryBuckets(r, daySince, dayUntil, dayUntil, cutoff, 'day', 'hour')).toBe(r);
  });

  it('prorates a month-granularity custom range at the month scale', () => {
    const since = dayjs('2026-03-15T00:00:00');
    const until = dayjs('2026-06-20T00:00:00');
    expect(granularityFor(since, until)).toBe('month');
    const buckets = ['2026-03', '2026-04', '2026-05', '2026-06'];
    const r: ActivityResponse = {
      metric: 'spend', group_by: 'model', rollup: 'month',
      series: buckets.map(b => ({ bucket: b, group: 'a', value: 62, is_zero: false })),
      summary: [{ group: 'a', min: 62, max: 62, avg: 62, sum: 248, value: 62, percent: 100 }],
      buckets,
      totals: { spend: 248, tokens: 0, requests: 0, cache: 0 },
    };
    const out = prorateBoundaryBuckets(r, since, until, until, dayjs('2026-07-01T00:00:00'), 'month', 'month');
    // March: [Mar 15, Apr 1) = 17/31; June: [Jun 1, Jun 20) = 19/30; the
    // boundary months' rows are complete (fetch on Jul 1).
    expect(out.series.map(p => p.value)).toEqual([
      62 * (17 / 31),
      62,
      62,
      62 * (19 / 30),
    ]);
    expect(out.summary[0].sum).toBeCloseTo(62 * (17 / 31) + 62 + 62 + 62 * (19 / 30), 10);
  });

  it('keeps the last boundary DAY at its full recorded value when the cutoff lies inside it', () => {
    // The day-scale twin of the live-hour test: [Aug 10 14:00, Aug 14 12:30)
    // viewed at Aug 14 11:00 — the last day's rows hold only [00:00, 11:00)
    // of recorded usage, all of it in-window, so the share is 1 and the day
    // bucketed at 12.5/24 by a naive window overlap would wrongly over-cut.
    const since = dayjs('2026-08-10T14:00:00');
    const until = dayjs('2026-08-14T12:30:00');
    const cutoff = dayjs('2026-08-14T11:00:00');
    const buckets = ['2026-08-10', '2026-08-11', '2026-08-12', '2026-08-13', '2026-08-14'];
    const r: ActivityResponse = {
      metric: 'spend', group_by: 'model', rollup: 'day',
      series: buckets.map(b => ({ bucket: b, group: 'a', value: 48, is_zero: false })),
      summary: [{ group: 'a', min: 48, max: 48, avg: 48, sum: 240, value: 48, percent: 100 }],
      buckets,
      totals: { spend: 240, tokens: 0, requests: 0, cache: 0 },
    };
    const out = prorateBoundaryBuckets(r, since, until, until, cutoff, 'day', 'day');
    expect(out.series.map(p => p.value)).toEqual([20, 48, 48, 48, 48]);
    // The same share the Overview flow gives that day's rows.
    expect(bucketWindowShare('2026-08-14T00:00:00', since, until, cutoff, 'day', false)).toBe(1);
  });

  it('leaves the blended metric untouched (rate cells are invariant under the window overlap)', () => {
    const since = dayjs('2026-08-13T14:37:00');
    const until = dayjs('2026-08-13T18:22:00');
    const r: ActivityResponse = {
      metric: 'blended', group_by: 'model', rollup: 'hour',
      series: [
        ...['2026-08-13 14:00', '2026-08-13 15:00', '2026-08-13 16:00', '2026-08-13 17:00', '2026-08-13 18:00']
          .map(b => ({ bucket: b, group: 'a', value: 2.5, is_zero: false })),
      ],
      summary: [{ group: 'a', min: 2.5, max: 2.5, avg: 2.5, sum: 2.5, value: 2.5, percent: 100 }],
      buckets: ['2026-08-13 14:00', '2026-08-13 15:00', '2026-08-13 16:00', '2026-08-13 17:00', '2026-08-13 18:00'],
      totals: { spend: 0, tokens: 0, requests: 0, cache: 0 },
    };
    expect(prorateBoundaryBuckets(r, since, until, until, dayjs('2026-08-13T19:00:00'), 'hour', 'hour')).toBe(r);
  });

  it('leaves sub-hour custom ranges untouched (resampleResponse already clips them)', () => {
    const since = dayjs('2026-08-13T13:00:00');
    const until = dayjs('2026-08-13T15:00:00');
    expect(granularityFor(since, until)).toBe('min15');
    const r = hourResp([60, 60, 60, 60, 60]);
    expect(prorateBoundaryBuckets(r, since, until, until, dayjs('2026-08-13T15:30:00'), 'min15', 'hour')).toBe(r);
  });

  it('prorates BOTH boundary buckets of the Trends PREVIOUS-period window (its query sends the raw mid-bucket since)', () => {
    // Trends' prev window for the 14:37-18:22 custom is [10:37, 14:37) and
    // sends the RAW since (prevWindowUntil: a mid-bucket since keeps the
    // bucket containing it in the widened server window), so the response's
    // last bucket (14:00) holds the prev-period slice [14:00, 14:37) — it
    // must be prorated by 37/60 on top of the first bucket's 23/60.
    const prevSince = dayjs('2026-08-13T10:37:00');
    const since = dayjs('2026-08-13T14:37:00'); // the prev window's end
    const untilSent = prevWindowUntil(since, 'hour');
    expect(untilSent.toISOString()).toBe(since.toISOString());
    const buckets = ['2026-08-13 10:00', '2026-08-13 11:00', '2026-08-13 12:00', '2026-08-13 13:00', '2026-08-13 14:00'];
    const r: ActivityResponse = {
      metric: 'spend', group_by: 'model', rollup: 'hour',
      series: buckets.map(b => ({ bucket: b, group: 'a', value: 60, is_zero: false })),
      summary: [{ group: 'a', min: 60, max: 60, avg: 60, sum: 300, value: 60, percent: 100 }],
      buckets,
      totals: { spend: 300, tokens: 0, requests: 0, cache: 0 },
    };
    const out = prorateBoundaryBuckets(r, prevSince, since, untilSent, dayjs('2026-08-13T19:00:00'), 'hour', 'hour');
    expect(out.series.map(p => p.value)).toEqual([23, 60, 60, 60, 37]);
    expect(out.summary[0].sum).toBeCloseTo(240, 10);
  });

  it('keeps the live boundary bucket at its full recorded value when the cutoff lies inside it', () => {
    // Custom [16:37, 19:22) fetched at 19:10: the 19:00 bucket's rows hold
    // only [19:00, 19:10) of recorded usage — all of it in-window — so the
    // share is 1 (the same value bucketWindowShare gives the Overview flow
    // for that row), and the bucket must NOT be cut.
    const since = dayjs('2026-08-13T16:37:00');
    const until = dayjs('2026-08-13T19:22:00');
    const cutoff = dayjs('2026-08-13T19:10:00');
    const out = prorateBoundaryBuckets(hourResp([60, 60, 60, 60, 60]), since, until, until, cutoff, 'hour', 'hour');
    expect(out.series.filter(p => p.group === 'a').map(p => p.value)).toEqual([23, 60, 60, 60, 60]);
    expect(bucketWindowShare('2026-08-13T19:00:00', since, until, cutoff, 'hour', false)).toBe(1);
  });
});

describe('groupTotals — optional proration factor', () => {
  it('multiplies each row by the factor when provided', () => {
    const rows = [
      { hour_bucket: '2026-08-13T15:00:00', m: 'a', n: 10 },
      { hour_bucket: '2026-08-13T15:00:00', m: 'b', n: 6 },
    ];
    const factor = (r: { hour_bucket: string; m: string; n: number }) => (r.m === 'a' ? 0.5 : 1);
    expect(groupTotals(rows, r => r.m, r => r.n, factor)).toEqual([['b', 6], ['a', 5]]);
    // Without a factor the sums stay raw (existing behavior).
    expect(groupTotals(rows, r => r.m, r => r.n)).toEqual([['a', 10], ['b', 6]]);
  });
});

describe('label formatters', () => {
  it('formats hour ticks and tooltips', () => {
    expect(fmtTick('hour', '08-13 15:00')).toBe('15:00');
    expect(fmtBucket('hour', '08-13 15:00')).toBe('Aug 13, 15:00');
  });

  it('formats year-qualified hour labels (backend rollup)', () => {
    expect(fmtTick('hour', '2026-08-13 15:00')).toBe('15:00');
    expect(fmtBucket('hour', '2026-08-13 15:00')).toBe('Aug 13, 15:00');
    expect(fmtDayLabel('2026-08-13')).toBe('Aug 13');
  });

  it('formats minute ticks and tooltips like hours', () => {
    expect(fmtTick('minute', '08-13 15:50')).toBe('15:50');
    expect(fmtTick('min15', '2026-08-13 15:50')).toBe('15:50');
    expect(fmtBucket('minute', '08-13 15:50')).toBe('Aug 13, 15:50');
    expect(fmtBucket('min15', '2026-08-13 15:45')).toBe('Aug 13, 15:45');
  });

  it('formats day, week and month buckets', () => {
    expect(fmtTick('day', '08-13')).toBe('Aug 13');
    expect(fmtBucket('day', '08-13')).toBe('Aug 13');
    expect(fmtTick('day', '2026-08-10')).toBe('Aug 10');   // week bucket label
    expect(fmtBucket('day', '2026-08-10')).toBe('Aug 10, 2026');
    // Month ticks must not read as day labels ("Aug 26" is ambiguous on a
    // year-long axis); "Aug '26" shows the year explicitly.
    expect(fmtTick('month', '2026-08')).toBe("Aug '26");
    expect(fmtBucket('month', '2026-08')).toBe('Aug 2026');
  });

  it('falls back to the raw label for unparseable input', () => {
    expect(fmtDayLabel('nonsense')).toBe('nonsense');
    expect(fmtTick('day', 'nonsense')).toBe('nonsense');
  });
});
// --- computeTrending (Trends "Trending" card) ------------------------------

// resp builds a minimal ActivityResponse with the given summary/series.
function resp(summary: Array<{ group: string; sum: number }>, series: Array<{ bucket: string; group: string; value: number }>, buckets: string[]): ActivityResponse {
  return {
    metric: 'spend', group_by: 'model', rollup: 'day',
    series: series.map(p => ({ ...p, is_zero: p.value === 0 })),
    summary: summary.map(s => ({ group: s.group, min: s.sum, max: s.sum, avg: s.sum, sum: s.sum, value: s.sum, percent: 0 })),
    buckets, totals: { spend: 0, tokens: 0, requests: 0, cache: 0 },
  };
}

describe('computeTrending', () => {
  it('ranks by absolute drop (prev − cur), New rows by current value', () => {
    const cur = resp(
      [{ group: 'a', sum: 4 }, { group: 'b', sum: 10 }, { group: 'c', sum: 5 }, { group: 'd', sum: 3 }],
      [], ['01-01', '01-02']);
    const prev = resp(
      [{ group: 'a', sum: 10 }, { group: 'b', sum: 5 }, { group: 'd', sum: 100 }],
      [], ['01-01', '01-02']);
    // drops: d 100−3=97, a 10−4=6; gainer b 5−10=−5 sinks; new c ranks by cur 5
    const rows = computeTrending(cur, prev);
    expect(rows.map(r => r.group)).toEqual(['d', 'a', 'c', 'b']);
    expect(rows[0]).toMatchObject({ group: 'd', pct: -97, isNew: false });
    expect(rows[2]).toMatchObject({ group: 'c', pct: 100, isNew: true });
  });

  it('excludes "Other" and caps at 6 rows', () => {
    const groups = ['m1', 'm2', 'm3', 'm4', 'm5', 'm6', 'm7', 'Other'];
    const cur = resp(groups.map(g => ({ group: g, sum: 1 })), [], ['01-01']);
    const prev = resp(groups.map(g => ({ group: g, sum: 10 })), [], ['01-01']);
    const rows = computeTrending(cur, prev);
    expect(rows).toHaveLength(6);
    expect(rows.some(r => r.group === 'Other')).toBe(false);
  });

  it('skips entities with no usage in either period', () => {
    const cur = resp([{ group: 'a', sum: 5 }], [], ['01-01']);
    const prev = resp([{ group: 'b', sum: 0 }], [], ['01-01']);
    expect(computeTrending(cur, prev)).toHaveLength(1);
  });

  it('sparkline = previous-period series, flat zeros when no prior usage', () => {
    const cur = resp([{ group: 'a', sum: 5 }, { group: 'b', sum: 5 }], [], ['01-01', '01-02']);
    const prev = resp(
      [{ group: 'a', sum: 10 }],
      [{ bucket: '01-01', group: 'a', value: 2 }, { bucket: '01-02', group: 'a', value: 8 }],
      ['01-01', '01-02']);
    const rows = computeTrending(cur, prev);
    expect(rows.find(r => r.group === 'a')!.spark).toEqual([2, 8]);
    expect(rows.find(r => r.group === 'b')!.spark).toEqual([0, 0]);
  });
});

describe('cacheHitRate', () => {
  // The backend stores input_tokens as TOTAL input incl. cached for every
  // provider (OpenAI's prompt_tokens already includes cached tokens;
  // Anthropic's input_tokens is folded at record time), so the rate is
  // cache / input. Adding cache to the denominator again — the old buggy
  // cache / (input + cache) — double-counts OpenAI cache tokens and halves
  // high rates.
  it('returns the real hit rate when input already includes cached tokens', () => {
    // 2450 of 2500 prompt tokens cached = 98%. The old formula produced
    // 2450 / (2500 + 2450) = 49.5% — the stuck ~49.6% the user saw.
    expect(cacheHitRate(2500, 2450)).toBeCloseTo(98.0, 5);
  });

  it('reports 100% when every input token is cached', () => {
    expect(cacheHitRate(100, 100)).toBe(100);
  });

  it('reports 0 with no input', () => {
    expect(cacheHitRate(0, 0)).toBe(0);
    expect(cacheHitRate(0, 5)).toBe(0);
  });

  it('clamps to 100 for legacy Anthropic rows whose input excluded cache', () => {
    // Old Anthropic-native rows stored input WITHOUT cache, so cache can
    // exceed input there (98 reads vs 2 uncached). Must clamp, not print
    // 4900%.
    expect(cacheHitRate(2, 98)).toBe(100);
  });
});

describe('toChartData', () => {
  it('zero-fills every group per bucket in series order', () => {
    const r = resp(
      [{ group: 'a', sum: 1 }, { group: 'b', sum: 2 }],
      [
        { bucket: '01-01', group: 'a', value: 1 },
        { bucket: '01-01', group: 'b', value: 2 },
        { bucket: '01-02', group: 'a', value: 0 },
        { bucket: '01-02', group: 'b', value: 0 },
      ],
      ['01-01', '01-02']);
    expect(toChartData(r)).toEqual([
      { label: '01-01', a: 1, b: 2 },
      { label: '01-02', a: 0, b: 0 },
    ]);
  });
});