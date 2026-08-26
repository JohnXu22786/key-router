// Lord Howe DST tests run in their OWN file because they must run under
// Australia/Lord_Howe — the half-hour DST shift (+10:30 standard / +11:00
// daylight) that breaks the hour floor's minute-field subtraction. The TZ
// override is applied via vi.stubEnv before any test parses a date, and
// vitest gives each test file its own worker, so the override never leaks
// into the other suites (which run in the machine's local zone or under
// America/New_York via activityShared.dst.test.ts).
import { describe, it, expect, vi, beforeAll } from 'vitest';
import dayjs from 'dayjs';
import { bucketWindowShare, floorWindowUntil } from './activityShared';

beforeAll(() => {
  vi.stubEnv('TZ', 'Australia/Lord_Howe');
});

// Fall-back night: Apr 5 2026, 02:00 +11:00 -> 01:30 +10:30 (the year's
// first Sunday in April). The wall-clock HALF-HOUR 01:30 repeats — both
// occurrences truncate to the same hourly 01:00 row, which therefore spans
// 90 elapsed minutes [01:00 +11:00 (14:00Z), 02:00 +10:30 (15:30Z)); only
// the first 30 minutes of the row's wall-clock hour (01:00..01:30) exist
// once. The second occurrence runs 15:00Z..15:30Z, reading 01:30..02:00
// +10:30.
//
// The hour floor's old minute-field subtraction (floorMinute(until)
// .subtract(until.minute(), 'minute')) is exact epoch arithmetic ONLY while
// the offset is constant across the subtraction span. It holds for
// whole-hour-shift zones (New York, Chatham) and for ordinary half-hour-zone
// hours, but on this fall-back night it maps every second-occurrence instant
// to 14:30Z — wall-clock 01:30 +11:00, MID-HOUR inside the first occurrence
// — instead of the true containing hour start 15:00Z (the transition
// instant). Those tests pin the fixed floor and the live-window behavior it
// restores.
describe('floorWindowUntil hour branch — Lord Howe half-hour fall-back', () => {
  it('floors a second-occurrence instant to the true hour start (15:00Z, not 14:30Z)', () => {
    // 01:40:30 +10:30 on the fall-back night = 2026-04-04T15:10:30Z. The
    // displayed hour 01:00–02:00 begins its SECOND pass at the transition
    // instant 15:00Z (the clock then reads 01:30 +10:30); the old minute
    // subtraction landed at 14:30Z (01:30 +11:00, mid-hour inside the FIRST
    // occurrence).
    const until = dayjs('2026-04-04T15:10:30.000Z');
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-04-04T15:00:00.000Z');
  });

  it('keeps a first-occurrence instant on the first occurrence grid (bit-identical)', () => {
    // 01:40:30 +11:00 (14:40:30Z): the minute subtraction is exact across
    // the constant-offset span, so the floor stays 01:00 +11:00 = 14:00Z.
    expect(floorWindowUntil(dayjs('2026-04-04T14:40:30.000Z'), 'hour').toISOString())
      .toBe('2026-04-04T14:00:00.000Z');
  });

  it('keeps a constant-offset +10:30 day on the :30 hour grid (bit-identical)', () => {
    // Apr 6 2026 is a plain +10:30 day: hour 01:00 starts at 14:30Z, exactly
    // where the minute subtraction lands. The control-day floor pins the
    // unambiguous behavior the fix must not move.
    const now = dayjs('2026-04-05T15:10:30.000Z'); // Apr 6 01:40:30 +10:30
    expect(floorWindowUntil(now, 'hour').toISOString()).toBe('2026-04-05T14:30:00.000Z');
  });
});

// Spring-forward gap: Oct 4 2026, 02:00 +10:30 -> 02:30 +11:00 at 15:30Z
// (Oct 3 UTC) — a 30-minute gap where wall-clock 02:00..02:29 never
// displays. The post-gap hour is UNAMBIGUOUS, yet its subtraction span
// crosses the jump: the old minute subtraction floored 02:40 +11:00
// (15:40:30Z) to 15:00Z — wall-clock 01:30 +10:30, mid-hour inside the
// PREVIOUS hour (the same off-grid break as the fall-back, share 0). These
// pins fix that floor at the transition point — the true start of the
// displayed hour — so a refactor cannot silently reintroduce the amputation.
describe('floorWindowUntil hour branch — Lord Howe spring-forward gap', () => {
  it('floors a post-gap instant to the transition point (15:30Z, not 15:00Z)', () => {
    // 02:40:30 +11:00 on Oct 4 = 2026-10-03T15:40:30Z; the displayed hour
    // 02:30–03:00 (+11:00) begins at the transition instant 15:30Z — a
    // fixed point of the floor, unlike the old subtraction's 15:00Z
    // (mid-hour inside 01:30 +10:30, the previous wall-clock hour).
    const until = dayjs('2026-10-03T15:40:30.000Z');
    expect(floorWindowUntil(until, 'hour').toISOString()).toBe('2026-10-03T15:30:00.000Z');
    expect(floorWindowUntil(dayjs('2026-10-03T15:30:00.000Z'), 'hour').toISOString())
      .toBe('2026-10-03T15:30:00.000Z');
  });

  it('shares the whole recorded slice on a live window over the post-gap hour (1, was 0)', () => {
    // 1d preset at 02:40:30 +11:00 (15:40:30Z): until floors to 15:30Z, the
    // live hour [15:30Z, 16:30Z) holds the row's whole recorded slice
    // [15:30Z, 15:40Z) — share 1, where the old floor's 15:00Z failed the
    // gate and read 0.
    const now = dayjs('2026-10-03T15:40:30.000Z');
    const until = floorWindowUntil(now, 'hour');
    const since = floorWindowUntil(now.subtract(24, 'hour'), 'hour');
    expect(until.toISOString()).toBe('2026-10-03T15:30:00.000Z');
    const share = bucketWindowShare('2026-10-04T02:00:00', since, until, now, 'hour');
    expect(share).toBeCloseTo(1, 10);
  });
});

// The 1d/2d/today/yesterday presets (hour granularity) snap `until` to the
// hour floor; the live cell — the hour starting AT the snapped until — then
// carries the second occurrence's recorded minutes. The old 14:30Z floor
// failed hasLiveCell's grid gate, so the live slice was suppressed and the
// base overlap clamped before the repeat: the row's newest ~40 recorded
// minutes (the entire second occurrence up to the fetch) vanished from the
// KPI and the hour chart (share 30/70 instead of 1).
describe('bucketWindowShare — Lord Howe hour-granularity live window on the repeat', () => {
  it('shares the whole repeated row when the live cell counts the second occurrence', () => {
    // 1d preset at 01:40:30 +10:30 (15:10:30Z): until floors to 15:00Z, the
    // live hour [15:00Z, 16:00Z) holds the row's recorded slice [15:00Z,
    // 15:10Z) on top of the in-window [14:00Z, 15:00Z) — the full 70
    // recorded minutes of the merged 01:00 row (90-minute coverage).
    const now = dayjs('2026-04-04T15:10:30.000Z');
    const until = floorWindowUntil(now, 'hour');
    const since = floorWindowUntil(now.subtract(24, 'hour'), 'hour');
    expect(until.toISOString()).toBe('2026-04-04T15:00:00.000Z');
    const share = bucketWindowShare('2026-04-05T01:00:00', since, until, now, 'hour');
    expect(share).toBeCloseTo(1, 10);
  });

  it('keeps a constant-offset +10:30 day live window at share 1 (control)', () => {
    // Same 1d-live-window shape on a plain +10:30 day well away from the
    // transition (Apr 7/8 — the fall-back was Apr 5, and a 24h window
    // ending at Apr 6 01:40 local would reach back into the fall-back
    // night): the row's whole 40 recorded minutes lie inside the live hour
    // [14:30Z, 15:30Z) — the share stays 1 exactly as before the fix.
    const now = dayjs('2026-04-07T15:10:30.000Z'); // Apr 8 01:40:30 +10:30
    const until = floorWindowUntil(now, 'hour');
    const since = floorWindowUntil(now.subtract(24, 'hour'), 'hour');
    expect(until.toISOString()).toBe('2026-04-07T14:30:00.000Z');
    const share = bucketWindowShare('2026-04-08T01:00:00', since, until, now, 'hour');
    expect(share).toBeCloseTo(1, 10);
  });
});