import { describe, it, expect } from 'vitest';
import dayjs from 'dayjs';
import {
  makeRanges, customRange, granularityFor, series, stackedData, bucketAxis,
  bucketWindowShare, groupTotals, fmtTick, fmtBucket, fmtDayLabel, CUSTOM_KEY,
  computeTrending, toChartData, cacheHitRate, resampleResponse,
} from './activityShared';
import type { ActivityResponse } from '../api/client';

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
    expect(out.map(p => p.label)).toEqual(['08-13 09:00', '08-13 10:00', '08-13 11:00', '08-13 12:00', '08-13 13:00']);
    expect(out.map(p => p.value)).toEqual([0, 2, 0, 1, 0]);
  });

  it('buckets a week daily and a year monthly', () => {
    const since = dayjs('2026-08-10T00:00:00');
    const out = series([row('2026-08-11T03:00:00'), row('2026-08-13T22:00:00')], () => 5, since, since.add(4, 'day'), since.add(4, 'day'), 'day');
    expect(out.map(p => p.label)).toEqual(['08-10', '08-11', '08-12', '08-13', '08-14']);
    expect(out.map(p => p.value)).toEqual([0, 5, 0, 5, 0]);

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
    expect(out.length).toBe(7);
    expect(out[0].label).toBe('08-01');
    expect(out[0].value).toBe(0);
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
    // instead of leaking a full live hour into the totals.
    const since = dayjs('2026-08-12T00:00:00');
    const until = dayjs('2026-08-13T00:00:00');
    expect(bucketWindowShare('2026-08-13T00:00:00', since, until, dayjs('2026-08-13T16:05:00'), 'hour')).toBe(0);
  });

  it('drops a bucket whose hour starts exactly at the window end', () => {
    // Window ends at 16:00 sharp; the 16:00 bucket's recorded value lies
    // entirely after the window (cutoff later) — share must be 0, never a
    // leak of the hour's usage into the totals.
    const since = dayjs('2026-08-13T13:05:00');
    const until = dayjs('2026-08-13T16:00:00');
    expect(bucketWindowShare('2026-08-13T16:00:00', since, until, dayjs('2026-08-13T16:05:00'), 'hour')).toBe(0);
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
      { label: '08-13 11:00', sort: '2026-08-13 11:00', a: 0, b: 0 },
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
    expect(hour.map(p => p.sort)).toEqual(['2026-08-13 09:00', '2026-08-13 10:00', '2026-08-13 11:00']);
    const month = bucketAxis(dayjs('2026-01-01'), dayjs('2026-03-31'), 'month');
    expect(month.map(p => p.label)).toEqual(['2026-01', '2026-02', '2026-03']);
  });

  it('builds a 1-minute axis covering the window start to its containing minute', () => {
    const out = bucketAxis(dayjs('2026-08-13T15:50:00'), dayjs('2026-08-13T16:05:00'), 'minute');
    expect(out).toHaveLength(16);
    expect(out[0]).toMatchObject({ label: '08-13 15:50', sort: '2026-08-13 15:50', value: 0 });
    expect(out[15]).toMatchObject({ label: '08-13 16:05', sort: '2026-08-13 16:05', value: 0 });
    expect(out.map(p => p.label)).toEqual([
      '08-13 15:50', '08-13 15:51', '08-13 15:52', '08-13 15:53', '08-13 15:54',
      '08-13 15:55', '08-13 15:56', '08-13 15:57', '08-13 15:58', '08-13 15:59',
      '08-13 16:00', '08-13 16:01', '08-13 16:02', '08-13 16:03', '08-13 16:04',
      '08-13 16:05',
    ]);
  });

  it('builds a 15-minute axis for a 3-hour window', () => {
    const out = bucketAxis(dayjs('2026-08-13T13:00:00'), dayjs('2026-08-13T15:59:59'), 'min15');
    expect(out.map(p => p.label)).toEqual([
      '08-13 13:00', '08-13 13:15', '08-13 13:30', '08-13 13:45',
      '08-13 14:00', '08-13 14:15', '08-13 14:30', '08-13 14:45',
      '08-13 15:00', '08-13 15:15', '08-13 15:30', '08-13 15:45',
    ]);
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
    // 15:50-15:59 get 120/60 = 2 each; 16:00-16:04 get 10/5 = 2 each; 16:05 (the
    // bucket containing `until`, no row coverage) stays 0.
    expect(out.map(p => p.value)).toEqual([
      2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
      2, 2, 2, 2, 2,
      0,
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
    expect(out.map(p => p.value)).toEqual([1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0]);
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
    const since = dayjs('2026-08-13T10:00:00');
    const until = dayjs('2026-08-13T10:10:00');
    const cutoff = dayjs('2026-08-13T10:30:00');
    const out = series([{ hour_bucket: '2026-08-13T10:00:00', v: 120 }], (r) => r.v, since, until, cutoff, 'minute');
    expect(out.map(p => p.value)).toEqual([4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 0]);
    expect(out.reduce((a, p) => a + p.value, 0)).toBeCloseTo(120 * (10 / 30), 6);
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
    // 15:50-15:59: a=2, b=1; 16:00 (containing `until`, no coverage): 0/0.
    expect(out.map(r => ({ a: r.a, b: r.b }))).toEqual([
      { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 },
      { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 }, { a: 2, b: 1 },
      { a: 0, b: 0 },
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
    expect(out.buckets).toHaveLength(16);
    expect(out.buckets[0]).toBe('08-13 15:50');
    // 16 buckets x 2 groups, all zero-filled like the server does.
    expect(out.series).toHaveLength(32);
    const a = out.series.filter(p => p.group === 'a').map(p => p.value);
    const b = out.series.filter(p => p.group === 'b').map(p => p.value);
    expect(a).toEqual([2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 0]);  // 15:00 (120/60) + 16:00 (10/5)
    expect(b).toEqual([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 0]);
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
    expect(sa.value).toBe(0);   // last bucket (16:05) is empty
    expect(sb.value).toBe(0);
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
      '08-13 16:00',
    ]);
    // 13:00 hour: 53 of its 60 minutes fall in the window (13:07-14:00)
    // -> 24 + 45x3; 14:00 hour: 15 x4; 15:00 hour: 30 x4; 16:00 bucket: 0.
    expect(out.series.map(p => p.value)).toEqual([24, 45, 45, 45, 15, 15, 15, 15, 30, 30, 30, 30, 0]);
    expect(out.series.reduce((a, p) => a + p.value, 0)).toBeCloseTo(180 * (53 / 60) + 60 + 120, 6);
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