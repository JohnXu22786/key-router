import { describe, it, expect } from 'vitest';
import dayjs from 'dayjs';
import {
  makeRanges, customRange, granularityFor, series, stackedData, bucketAxis,
  fmtTick, fmtBucket, fmtDayLabel, CUSTOM_KEY,
} from './activityShared';

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

  it('assigns granularity by scale: sub-day/hourly, weeks/months daily, years monthly', () => {
    const byKey = new Map(makeRanges(NOW).map(r => [r.key, r]));
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
    expect(customRange(since, since.add(1, 'hour')).granularity).toBe('hour');
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
      () => 1, since, until, 'hour',
    );
    expect(out.map(p => p.label)).toEqual(['08-13 09:00', '08-13 10:00', '08-13 11:00', '08-13 12:00', '08-13 13:00']);
    expect(out.map(p => p.value)).toEqual([0, 2, 0, 1, 0]);
  });

  it('buckets a week daily and a year monthly', () => {
    const since = dayjs('2026-08-10T00:00:00');
    const out = series([row('2026-08-11T03:00:00'), row('2026-08-13T22:00:00')], () => 5, since, since.add(4, 'day'), 'day');
    expect(out.map(p => p.label)).toEqual(['08-10', '08-11', '08-12', '08-13', '08-14']);
    expect(out.map(p => p.value)).toEqual([0, 5, 0, 5, 0]);

    const ySince = dayjs('2026-01-01T00:00:00');
    const yOut = series([row('2026-01-15T00:00:00'), row('2026-03-02T00:00:00')], () => 7, ySince, dayjs('2026-03-01T23:59:59'), 'month');
    expect(yOut.map(p => p.label)).toEqual(['2026-01', '2026-02', '2026-03']);
    expect(yOut.map(p => p.value)).toEqual([7, 0, 7]);
  });

  it('starts the axis at the range start, not at the first data point', () => {
    const since = dayjs('2026-08-01T00:00:00');
    const out = series([row('2026-08-05T00:00:00')], () => 1, since, since.add(6, 'day'), 'day');
    expect(out.length).toBe(7);
    expect(out[0].label).toBe('08-01');
    expect(out[0].value).toBe(0);
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
    const out = stackedData(rows, ['a', 'b'], r => r.m, r => r.n, since, until, 'hour');
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
    const out = stackedData(rows, ['top1', 'Other'], r => r.m, r => r.n, since, until, 'hour');
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

  it('formats day, week and month buckets', () => {
    expect(fmtTick('day', '08-13')).toBe('Aug 13');
    expect(fmtBucket('day', '08-13')).toBe('Aug 13');
    expect(fmtTick('day', '2026-08-10')).toBe('Aug 10');   // week bucket label
    expect(fmtBucket('day', '2026-08-10')).toBe('Aug 10, 2026');
    expect(fmtTick('month', '2026-08')).toBe('Aug 26');
    expect(fmtBucket('month', '2026-08')).toBe('Aug 2026');
  });

  it('falls back to the raw label for unparseable input', () => {
    expect(fmtDayLabel('nonsense')).toBe('nonsense');
    expect(fmtTick('day', 'nonsense')).toBe('nonsense');
  });
});
