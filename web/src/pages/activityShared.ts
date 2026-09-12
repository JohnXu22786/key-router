import dayjs from 'dayjs';
import { ActivityResponse, ActivitySeriesPoint, ActivityGroupSummary } from '../api/client';

// Shared helpers for the Activity pages. Kept OUT of Activity.tsx so no page
// ever imports from it (Activity -> child -> activityShared would be a
// circular import, which can hand the children undefined constants during
// module init and crash the page). activityShared itself only imports
// dayjs and the API client — a leaf module.

// Bucket granularity for a range. Mirrors OpenRouter's rollup ladder
// (Hourly / Daily / Weekly / Monthly) but adds sub-hour scales so short
// windows (15m / 30m / 1h / 3h) get a fine-grained time axis instead of one
// or two hourly bars: up to 1h buckets by minute, up to 3h by 15 minutes,
// sub-3-day windows by hour, up to two months by day, longer by month.
export type Granularity = 'minute' | 'min15' | 'hour' | 'day' | 'month';

export interface DateRange {
  key: string;
  label: string;
  // badge is the compact chip text on the picker ("1mo", "4d", "16h").
  // Rolling presets carry a static badge; calendar-anchored presets compute
  // the actual length of the range (e.g. This Week on a Thursday -> "4d"),
  // exactly like OpenRouter. Empty for the custom range (calendar icon).
  badge: string;
  since: dayjs.Dayjs;
  until: dayjs.Dayjs;
  granularity: Granularity;
}

// ExploreOpts carries Trends -> Explore navigation state (the reference
// "Explore" links pass metric/dimension query params).
export interface ExploreOpts {
  metric?: string;
  groupBy?: string;
}

// --- Activity page entity filter ------------------------------------------
// The filter button (left of the date range) narrows every tab to a single
// entity: one model, one API key or one app. Passed to both the activity
// and consumptions endpoints as filter_type/filter_value so the server
// excludes rows before aggregating (Trends' per-key breakdowns stay correct
// under a model filter).
export type ActivityFilterType = 'model' | 'key' | 'app';

export interface ActivityFilter {
  type: ActivityFilterType;
  // model/app name, or the key's numeric id for type 'key'.
  value: string;
  // Display label for the filter button (key name / model / app name).
  label: string;
}

export const FILTER_TYPES: { value: ActivityFilterType; label: string }[] = [
  { value: 'model', label: 'Model' },
  { value: 'key', label: 'API Key' },
  { value: 'app', label: 'App' },
];

// filterKey serializes a filter for fetch keys ("model:gpt-4o", "" when
// none) so a filter change drops stale data instead of merging it.
export const filterKey = (f: ActivityFilter | null | undefined): string =>
  f ? `${f.type}:${f.value}` : '';

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// Monday 00:00 of now's week (dayjs starts weeks on Sunday).
const mondayOf = (now: dayjs.Dayjs): dayjs.Dayjs => {
  const startOfDay = now.startOf('day');
  return startOfDay.subtract((startOfDay.day() + 6) % 7, 'day');
};

// makeRanges builds the date presets relative to a reference time (now).
// The set, the order, the labels and the badge texts are copied from
// OpenRouter's Activity date-range picker (saved page 2026-08 + the user's
// dump of the dropdown): nine rolling windows, eight calendar-anchored
// windows. The Activity page re-runs this on Refresh so every window slides
// to the current moment instead of staying frozen at page-load time.
export function makeRanges(now: dayjs.Dayjs): DateRange[] {
  const startOfDay = now.startOf('day');
  const monday = mondayOf(now);
  const monthStart = now.startOf('month');
  const yearStart = now.startOf('year');
  return [
    // Rolling windows (badge = the preset's own compact duration). Short
    // windows bucket sub-hour so the axis carries a real time scale.
    { key: '15m', label: 'Past 15 Minutes', badge: '15m', since: now.subtract(15, 'minute'), until: now, granularity: 'minute' },
    { key: '30m', label: 'Past 30 Minutes', badge: '30m', since: now.subtract(30, 'minute'), until: now, granularity: 'minute' },
    { key: '1h', label: 'Past 1 Hour', badge: '1h', since: now.subtract(1, 'hour'), until: now, granularity: 'minute' },
    { key: '3h', label: 'Past 3 Hours', badge: '3h', since: now.subtract(3, 'hour'), until: now, granularity: 'min15' },
    { key: '1d', label: 'Past 24 Hours', badge: '1d', since: now.subtract(24, 'hour'), until: now, granularity: 'hour' },
    { key: '2d', label: 'Past 48 Hours', badge: '2d', since: now.subtract(48, 'hour'), until: now, granularity: 'hour' },
    { key: '1w', label: 'Past 1 Week', badge: '1w', since: now.subtract(7, 'day'), until: now, granularity: 'day' },
    { key: '1mo', label: 'Past 1 Month', badge: '1mo', since: now.subtract(1, 'month'), until: now, granularity: 'day' },
    { key: '1y', label: 'Past 1 Year', badge: '1y', since: now.subtract(1, 'year'), until: now, granularity: 'month' },

    // Calendar-anchored windows (badge = the range's real length, rounded;
    // floor of 1 so a fresh midnight never reads "0h"/"0d"/"0mo").
    { key: 'today', label: 'Today', badge: `${Math.max(1, Math.round(now.diff(startOfDay, 'hour', true)))}h`, since: startOfDay, until: now, granularity: 'hour' },
    { key: 'yesterday', label: 'Yesterday', badge: '24h', since: startOfDay.subtract(1, 'day'), until: startOfDay, granularity: 'hour' },
    { key: 'week', label: 'This Week', badge: `${Math.max(1, Math.round(now.diff(monday, 'day', true)))}d`, since: monday, until: now, granularity: 'day' },
    { key: 'prevweek', label: 'Prev Week', badge: '7d', since: monday.subtract(7, 'day'), until: monday, granularity: 'day' },
    { key: 'month', label: 'This Month', badge: `${Math.max(1, Math.round(now.diff(monthStart, 'day', true)))}d`, since: monthStart, until: now, granularity: 'day' },
    { key: 'prevmonth', label: 'Prev Month', badge: `${monthStart.subtract(1, 'day').daysInMonth()}d`, since: monthStart.subtract(1, 'month'), until: monthStart, granularity: 'day' },
    { key: 'year', label: 'This Year', badge: `${Math.max(1, now.month())}mo`, since: yearStart, until: now, granularity: 'month' },
    { key: 'prevyear', label: 'Prev Year', badge: '1y', since: yearStart.subtract(1, 'year'), until: yearStart, granularity: 'month' },
  ];
}

export const CUSTOM_KEY = 'custom';
export const CUSTOM_LABEL = 'Custom range';

// floorMinute floors a time to the start of its LOCAL minute on the EPOCH
// grid — subtracting the local second+ms fields, never through dayjs's
// startOf('minute'), whose local-field reconstruction re-anchors an
// ambiguous wall-clock to the FIRST occurrence on the fall-back's repeated
// hour (01:20:30 EST reconstructs as 01:20:30 EDT, one hour earlier — the
// same setter disambiguation rowCoverageEnd's clamp must dodge). On
// unambiguous times both give the same instant: the subtraction is exactly
// what startOf does to the fields, minus the ambiguous reconstruction.
// Shared by the window-side floors (floorWindowUntil, hasLiveCell) and the
// coverage clamp (rowCoverageEnd) so every grid snaps to the same epochs.
function floorMinute(t: dayjs.Dayjs): dayjs.Dayjs {
  return dayjs(t.valueOf() - (t.second() * 1000 + t.millisecond()));
}

// --- DST transition machinery -------------------------------------------
// An hourly row's recorded value covers the elapsed instants whose LOCAL
// wall-clock hour field equals the row's hour. On a fall-back night the
// clock jumps back at a transition instant T (the offset DROPS from
// `before` to `after`, Δ = before - after minutes) and the wall-clock span
// [τ+, τ+ + Δ) — τ+ = the wall time displayed AT T, post-jump — displays
// twice. Whole-hour shifts (New York 02:00 -> 01:00) and half-hour shifts
// (Lord Howe 02:00 -> 01:30) keep that span inside ONE hour field, so the
// affected row's extent is a single contiguous [start, end) — the shape the
// old hour-field-equality walk modeled. But the 45-minute-offset shift
// (Pacific/Chatham +13:45/+12:45, the only real zone) repeats the span
// [02:45, 03:45) on the Apr 5 2026 fall-back — it CROSSES the 02/03 field
// boundary: row '02:00' then covers [02:00, 03:00) +13:45 ([12:15Z, 13:15Z))
// plus the 15-minute slice [02:45, 03:00) +12:45 ([14:00Z, 14:15Z)) — a
// NON-CONTIGUOUS 75-minute extent with a 45-minute gap — and row '03:00'
// covers 45 + 60 = 105 minutes ([13:15Z, 14:00Z) plus [14:15Z, 15:15Z),
// the [14:00Z, 14:15Z) gap being row '02:00''s data). The old detection
// (`start.add(1,'hour').hour() === start.hour()`) assumed every repeat is
// hour-field-aligned and got BOTH Chatham rows wrong: row '03:00''s +1h
// step crossed the transition and absorbed 15 minutes of row '02:00''s
// second pass into a contiguous 120-minute span, and row '02:00''s +1h
// step lands on field 03 (no match), so its second-pass slice was never
// represented at all.
//
// The fixed machinery derives the repeat from the OFFSET CHANGE instead:
// findFallBack locates the transition, and repeatRuns maps the repeated
// span onto the row's field to return its TRUE extent as epoch runs. For
// New York and Lord Howe the runs merge into exactly the contiguous span
// the old walk produced (120 / 90 minutes) — bit-identical coverage — and
// the misaligned Chatham rows get their true two-run extents.
//
// Real zones have at most one offset transition per day, so a single offset
// comparison 179 minutes out proves whether any transition can sit inside
// the 180-minute window (an offset difference there can only be a
// spring-forward or a fall-back; the walk resolves which).
function findFallBack(start: dayjs.Dayjs): { t: dayjs.Dayjs; before: number; after: number } | null {
  if (start.add(179, 'minute').utcOffset() === start.utcOffset()) return null;
  let t = start;
  let prevOff = start.utcOffset();
  for (let i = 0; i < 180; i++) {
    t = t.add(1, 'minute');
    const off = t.utcOffset();
    if (off !== prevOff) return off < prevOff ? { t, before: prevOff, after: off } : null;
    prevOff = off;
  }
  return null;
}

// findSpringForward is the forward twin of findFallBack: the first
// spring-forward T AFTER `start` within 180 minutes. Walking forward across
// a spring-forward shows an offset increase. It is used for hourly rows whose
// wall field begins before the gap; the backward twin below handles a row
// label that V8 normalizes to the first valid post-gap instant.
function findSpringForward(start: dayjs.Dayjs): { t: dayjs.Dayjs; before: number; after: number } | null {
  if (start.add(179, 'minute').utcOffset() === start.utcOffset()) return null;
  let t = start;
  let prevOff = start.utcOffset();
  for (let i = 0; i < 180; i++) {
    t = t.add(1, 'minute');
    const off = t.utcOffset();
    if (off !== prevOff) return off > prevOff ? { t, before: prevOff, after: off } : null;
    prevOff = off;
  }
  return null;
}

// findFallBackBefore is the backward twin of findFallBack: the first
// transition BEFORE `until` within 180 minutes (used only on the hour
// floor's second-pass branch, so no probe shortcut). Walking BACKWARD a
// fall-back shows up as an offset INCREASE (from the post-jump offset to
// the pre-jump one).
function findFallBackBefore(until: dayjs.Dayjs): { t: dayjs.Dayjs; before: number; after: number } | null {
  // Walk the WHOLE-MINUTE grid (floorMinute(until)): the boundary rows of
  // the transition sit on it, while stepping from a second-bearing until
  // would land every probe :30 later and shift the found T by half a
  // minute.
  let t = floorMinute(until);
  const prevOff = until.utcOffset();
  for (let i = 0; i < 180; i++) {
    const off = t.utcOffset();
    if (off !== prevOff) return off > prevOff ? { t: t.add(1, 'minute'), before: off, after: prevOff } : null;
    t = t.subtract(1, 'minute');
  }
  return null;
}

// findSpringForwardBefore is the spring-forward mirror of
// findFallBackBefore: the first spring-forward T BEFORE `start` within 180
// minutes. Walking BACKWARD across a spring-forward, the offset DROPS
// (we step from the post-jump higher offset to the pre-jump lower one) —
// the opposite of the fall-back pattern. Used by both the hour floor's
// spring-forward rebuild and springRuns to recover T when V8's setter
// resolution of a nonexistent wall time landed past the gap. The
// 45-minute-offset Chatham shift is the only real zone where the hour-floor
// branch needs this: V8 rolls `03:00+13:45` forward to `04:00+13:45`
// = 14:15Z, so the post-jump offset comparison is inert and the transition
// must be found by walking back.
function findSpringForwardBefore(start: dayjs.Dayjs): { t: dayjs.Dayjs; before: number; after: number } | null {
  let t = floorMinute(start);
  const startOff = start.utcOffset();
  let prevOff = startOff;
  for (let i = 0; i < 180; i++) {
    const off = t.utcOffset();
    if (off !== prevOff) return off < prevOff ? { t: t.add(1, 'minute'), before: off, after: prevOff } : null;
    prevOff = off;
    t = t.subtract(1, 'minute');
  }
  return null;
}

// repeatRuns returns the epoch runs an hourly row's recorded value covers
// when a fall-back affects it — the elapsed instants displaying the row's
// field, decomposed into contiguous runs — or null when the row is
// unaffected (the caller keeps the plain [start, start+1h) model). `start`
// must be the row's FIRST-pass anchor (the first instant displaying its
// hour field). The runs, for an affected row:
//   - [start, min(start+60min, T)): the first pass (its wall hour runs to
//     its own field end, or to the jump when the jump cuts the field short
//     — the τ- field, e.g. Chatham row '03:00' whose [03:00, 03:45) pre-
//     jump pass ends at T).
//   - the field's slice of the second pass: wall [max(field, τ+),
//     min(field+60, τ+ + Δ)) at the post-jump offset.
//   - the post-repeat continuation: for the field CONTAINING the span's
//     end (τ+ + Δ < field + 60 — e.g. Chatham row '03:00', whose wall
//     [03:45, 04:00) displays only once, after the repeat, up to its field
//     end 04:00).
// Whole-hour and half-hour shifts produce adjacent runs that merge into one
// span; the Chatham shift produces two runs with a gap. Wall times are
// minute-of-day values; real fall-backs happen 01:00–03:45 wall, so the
// repeated span never crosses midnight.
function repeatRuns(start: dayjs.Dayjs): Array<{ from: number; to: number }> | null {
  const fb = findFallBack(start);
  if (!fb) return null;
  const delta = fb.before - fb.after;                     // jump size in minutes
  const tauPlus = fb.t.hour() * 60 + fb.t.minute();       // wall time displayed AT the jump (post-jump offset)
  const field = start.hour() * 60 + start.minute();       // the row's field on the minute-of-day grid
  if (field + 60 <= tauPlus || field >= tauPlus + delta) return null; // the repeated span misses the row's field
  const runs: Array<{ from: number; to: number }> = [{
    from: start.valueOf(),
    to: Math.min(start.add(1, 'hour').valueOf(), fb.t.valueOf()),
  }];
  const w1 = Math.max(field, tauPlus);
  const w2 = Math.min(field + 60, tauPlus + delta);
  if (w2 > w1) runs.push({ from: fb.t.valueOf() + (w1 - tauPlus) * 60000, to: fb.t.valueOf() + (w2 - tauPlus) * 60000 });
  if (tauPlus + delta < field + 60) {
    runs.push({ from: fb.t.valueOf() + delta * 60000, to: fb.t.valueOf() + (field + 60 - tauPlus) * 60000 });
  }
  const merged: Array<{ from: number; to: number }> = [runs[0]];
  for (let i = 1; i < runs.length; i++) {
    const last = merged[merged.length - 1];
    if (runs[i].from === last.to) last.to = runs[i].to;
    else merged.push(runs[i]);
  }
  return merged;
}

// springRuns returns the epoch runs an hourly row covers when its local wall
// field intersects a spring-forward gap. `field` is the row's wall-clock
// minute-of-day, supplied from the serialized bucket label when possible:
// V8 normalizes a nonexistent Chatham `03:00` label to `04:00`, so the
// normalized Dayjs value alone cannot identify which row was returned.
//
// The skipped wall span is [tau-, tau+), where tau+ is the wall time shown at
// the transition and tau- is the last pre-jump wall boundary. The row keeps
// any valid pre-gap and post-gap pieces, never the skipped interval itself.
// A non-intersecting row returns null so the ordinary one-hour model remains
// unchanged; an entirely skipped row returns an empty array and therefore has
// zero recorded coverage.
function springRuns(start: dayjs.Dayjs, field = start.hour() * 60 + start.minute()): Array<{ from: number; to: number }> | null {
  const sf = findSpringForward(start) ?? findSpringForwardBefore(start);
  if (!sf) return null;
  const delta = sf.after - sf.before;
  const tauPlus = sf.t.hour() * 60 + sf.t.minute();
  const tauMinus = tauPlus - delta;
  const fieldEnd = field + 60;
  if (fieldEnd <= tauMinus || field >= tauPlus) return null;

  const transition = sf.t.valueOf();
  const runs: Array<{ from: number; to: number }> = [];
  if (field < tauMinus) {
    const preEnd = Math.min(fieldEnd, tauMinus);
    runs.push({
      from: transition - (tauMinus - field) * 60000,
      to: transition - (tauMinus - preEnd) * 60000,
    });
  }
  if (fieldEnd > tauPlus) {
    const postStart = Math.max(field, tauPlus);
    runs.push({
      from: transition + (postStart - tauPlus) * 60000,
      to: transition + (fieldEnd - tauPlus) * 60000,
    });
  }
  return runs;
}

// hourFieldFromBucket preserves the wall-clock field that a DST-gap bucket
// label had before the host Date parser normalized it. Hour buckets are
// serialized as local timestamps, with either T or a space between date and
// time depending on the response/test fixture.
function hourFieldFromBucket(hourBucket: string): number | undefined {
  const match = /[T ](\d{2}):(\d{2})/.exec(hourBucket);
  if (!match) return undefined;
  return Number(match[1]) * 60 + Number(match[2]);
}

// hourSortFromBucket keeps the serialized wall-clock hour for hourly axis
// assignment. A nonexistent spring-forward label such as Chatham's 03:00 is
// normalized by dayjs to 04:00, but the response row still belongs to the
// serialized 03:00 bucket.
function hourSortFromBucket(hourBucket: string): string | undefined {
  const match = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}):\d{2}/.exec(hourBucket);
  if (!match) return undefined;
  return `${match[1]} ${match[2]}:00`;
}

interface HourWallLabel {
  field: number;
  start: dayjs.Dayjs;
}

// hourWallLabelsForInstant reconstructs every local hour label that resolves
// to the persisted bucket's instant. RecordConsumption builds HourBucket with
// time.Date in the IANA local zone, so a spring-gap label can be normalized to
// a later valid label (Lord Howe 02:00 -> 02:30; Chatham 03:00 -> 04:00).
// The serialized value only contains the normalized timestamp; matching local
// hour candidates back to that instant recovers both the source label and any
// colliding valid label. The scan is limited to transition-near rows so normal
// activity responses do not pay for a 72-candidate search per row.
function hourWallLabelsForInstant(start: dayjs.Dayjs, wallField?: number): HourWallLabel[] {
  const instant = start.valueOf();
  const local = dayjs(instant);
  const labels: HourWallLabel[] = [];
  const seen = new Set<string>();
  const add = (field: number, labelStart: dayjs.Dayjs) => {
    if (field < 0 || field >= 24 * 60 || field % 60 !== 0) return;
    const key = `${field}:${labelStart.valueOf()}`;
    if (seen.has(key)) return;
    seen.add(key);
    labels.push({ field, start: labelStart });
  };

  // A minute-aligned serialized label is still useful as a direct fallback
  // (and preserves the existing fixed-offset fall-back convention). A
  // normalized spring label such as 02:30 is deliberately not treated as an
  // ordinary 02:30 hour; the candidate scan below supplies its source 02:00
  // field instead.
  if (wallField !== undefined && wallField % 60 === 0) {
    const hour = wallField / 60;
    const labelStart = dayjs(new Date(local.year(), local.month(), local.date(), hour, 0, 0, 0));
    add(wallField, labelStart);
    // A hand-built/legacy response can retain a nonexistent source label
    // (for example Chatham 03:00) even though Date normalizes its instant to
    // 04:00. In that shape the serialized field is the authoritative source
    // label. A real persisted normalized row either has a non-zero minute
    // (Lord Howe 02:30) or has the normalized field itself (Chatham 04:00),
    // both of which continue through the alias scan below.
    if (wallField !== local.hour() * 60) return labels;
  }

  const nearTransition = dayjs(instant - 4 * 60 * 60 * 1000).utcOffset()
    !== dayjs(instant + 4 * 60 * 60 * 1000).utcOffset();
  if (nearTransition) {
    // Date's local-field constructor has the same DST normalization rules as
    // RecordConsumption's time.Date. Compare epochs, not formatted strings,
    // so a normalized candidate and a valid colliding candidate are both
    // retained when they resolve to this persisted instant.
    for (let dayDelta = -1; dayDelta <= 1; dayDelta++) {
      for (let hour = 0; hour < 24; hour++) {
        const candidate = new Date(
          local.year(), local.month(), local.date() + dayDelta, hour, 0, 0, 0,
        );
        if (candidate.valueOf() === instant) {
          add(hour * 60, dayjs(candidate));
        }
      }
    }
  }

  if (labels.length === 0) {
    add(local.hour() * 60, dayjs(new Date(local.year(), local.month(), local.date(), local.hour(), 0, 0, 0)));
  }
  return labels.sort((a, b) => a.field - b.field || a.start.valueOf() - b.start.valueOf());
}

function mergeCoverageRuns(runs: Array<{ from: number; to: number }>): Array<{ from: number; to: number }> {
  const sorted = runs
    .filter(run => run.to > run.from)
    .sort((a, b) => a.from - b.from || a.to - b.to);
  const merged: Array<{ from: number; to: number }> = [];
  for (const run of sorted) {
    const last = merged[merged.length - 1];
    if (last && run.from <= last.to) {
      last.to = Math.max(last.to, run.to);
    } else {
      merged.push({ ...run });
    }
  }
  return merged;
}

interface HourWallCoverage {
  sort: string;
  runs: Array<{ from: number; to: number }>;
}

function hourWallRunsForBucket(hourBucket: string): HourWallCoverage[] {
  const start = dayjs(hourBucket).startOf('hour');
  const date = /^(\d{4}-\d{2}-\d{2})/.exec(hourBucket)?.[1] ?? start.format('YYYY-MM-DD');
  return hourWallLabelsForInstant(start, hourFieldFromBucket(hourBucket)).map(label => {
    const repeated = repeatRuns(label.start);
    const spring = repeated ? null : springRuns(label.start, label.field);
    const runs = repeated ?? spring ?? [{
      from: label.start.valueOf(),
      to: label.start.add(1, 'hour').valueOf(),
    }];
    const hour = String(Math.floor(label.field / 60)).padStart(2, '0');
    return { sort: `${date} ${hour}:00`, runs };
  });
}

// hourSortForWindow keeps a normalized spring-forward row on the wall-hour
// label whose recorded run overlaps the requested window. On Chatham, a
// persisted 04:00 row can contain both the normalized 03:00 alias
// (03:45..04:00) and the real 04:00 hour. If a window ends at 03:50, the
// serialized 04:00 label is outside the window and the visible data belongs
// under 03:00 instead. Once the serialized label itself overlaps, it remains
// authoritative so one merged row is not shown in two buckets.
export function hourSortForWindow(hourBucket: string, since: dayjs.Dayjs, until: dayjs.Dayjs): string {
  const serializedSort = hourSortFromBucket(hourBucket) ?? dayjs(hourBucket).format('YYYY-MM-DD HH:00');
  const labels = hourWallRunsForBucket(hourBucket);
  if (labels.length <= 1) return serializedSort;

  const overlaps = (label: HourWallCoverage): boolean => label.runs.some(run =>
    run.from < until.valueOf() && run.to > since.valueOf(),
  );
  if (labels.some(label => label.sort === serializedSort && overlaps(label))) return serializedSort;
  return labels.find(overlaps)?.sort ?? serializedSort;
}

function clampCoverageRuns(
  base: Array<{ from: number; to: number }>,
  cutoff: dayjs.Dayjs,
): Array<{ from: number; to: number }> {
  const clamp = floorMinute(cutoff).valueOf();
  const runs: Array<{ from: number; to: number }> = [];
  for (const run of base) {
    const to = Math.min(run.to, clamp);
    if (to > run.from) {
      runs.push({ from: run.from, to });
      continue;
    }
    // A row created inside the first minute of a run is still represented by
    // one recorded minute. Apply that rule per run: a normalized spring row
    // can have a later alias run beginning exactly at the cutoff boundary.
    if (cutoff.isAfter(run.from)) {
      const rescuedTo = Math.min(run.to, run.from + 60000);
      if (rescuedTo > run.from) runs.push({ from: run.from, to: rescuedTo });
    }
  }
  return mergeCoverageRuns(runs);
}

// floorWindowUntil snaps a time to the START of the bucket that contains it
// at the given granularity. Preset windows snap BOTH bounds to the bucket
// grid so the window keeps its exact nominal length and the COMPLETED cells
// stay identical between 30s auto-refreshes. Sub-hour granularities snap to
// their own step (min15 -> the 15-minute clock cell). Custom ranges keep
// their exact user-picked bounds — never snapped.
export function floorWindowUntil(until: dayjs.Dayjs, granularity: Granularity): dayjs.Dayjs {
  // The sub-hour floors must land on the EPOCH minute grid, not dayjs's
  // startOf: on the fall-back's repeated hour a second-occurrence until
  // (01:20:30 EST) would be re-anchored to the FIRST occurrence (01:20:00
  // EDT, one hour earlier — see floorMinute), snapping the whole window one
  // hour too early while the coverage reads the true extent — amputating
  // the newest usage (the entire second occurrence up to the fetch) from
  // the KPI and the chart. The day/month floors reconstruct unambiguous
  // fields (midnight / the 1st) and stay on startOf.
  if (granularity === 'minute') return floorMinute(until);
  if (granularity === 'min15') return floorMinute(until).subtract(until.minute() % 15, 'minute');
  if (granularity === 'hour') {
    // The hour floor must snap to the start of the wall-clock hour
    // CONTAINING `until`, in ELAPSED time. Subtracting until.minute() as
    // elapsed minutes is exact only while the offset stays uniform across
    // the subtraction SPAN: whole-hour-shift zones (New York, Chatham) and
    // ordinary half-hour-zone hours (whose :30 hour starts the subtraction
    // lands on) all satisfy it. On a half-hour-shift FALL-BACK (Lord Howe
    // +10:30/+11:00) it does not: 01:40:30 +10:30 (15:10:30Z) subtracts 40
    // elapsed minutes to 14:30Z — wall-clock 01:30 +11:00, MID-HOUR inside
    // the FIRST occurrence — instead of the true containing hour start
    // 15:00Z (the transition instant where the displayed hour's second pass
    // begins). The floor then sits off the hour grid, hasLiveCell's gate
    // (floor(until).isSame(until)) fails, and the live cell amputates the
    // whole second occurrence from the KPI and the hour chart. Rebuild the
    // hour start in zoned arithmetic instead: the JS Date setters resolve
    // the ambiguous wall-clock to the FIRST occurrence, and an instant on
    // the second pass carries a lower offset than that first start — push
    // it one elapsed hour forward, where a repeated DISPLAYED hour begins
    // its second pass (14:00Z -> 15:00Z for Lord Howe, 05:00Z -> 06:00Z
    // for New York). Anything else takes the first branch: while the
    // offset is uniform across the subtraction span the rebuilt start
    // coincides exactly with the old minute subtraction, so every
    // unambiguous floor whose span stays inside one offset regime is
    // bit-identical (whole-hour zones, constant-offset days, ordinary
    // hours). Where the span DOES cross a transition the new floor is
    // the CORRECTION, never a regression: the spring-forward post-gap
    // hour is unambiguous but its subtraction span crosses the jump —
    // Lord Howe 02:40 +11:00 (local Oct 4 2026, 15:40:30Z) subtracted 40
    // minutes across the 15:30Z transition and floored to 15:00Z, mid-
    // hour inside the PREVIOUS wall-clock hour (01:30 +10:30, the same
    // off-grid break as the fall-back), while the rebuilt start lands on
    // the transition point 15:30Z — the true start of the displayed
    // hour — and the live cell keeps the whole recorded slice (share 1
    // where the old floor read 0).
    //
    // The +1h push itself assumes the second pass REALIGNS with the hour
    // field (first + 1h = the second pass's start) — true for New York
    // and Lord Howe, whose fall-backs land on a field boundary, but not
    // for the 15-minute-misaligned Pacific/Chatham shift (+13:45/+12:45,
    // Apr 5 2026: the jump 03:45 +13:45 -> 02:45 +12:45 at 14:00Z repeats
    // the span [02:45, 03:45), crossing the 02/03 field boundary). There,
    // a second-pass instant like 02:50 +12:45 (14:05Z) reconstructs
    // 02:00:00, which exists only in the FIRST pass (12:15Z), and the +1h
    // push lands on 13:15Z — one full hour before the true containing-
    // hour start 14:00Z (wall 02:45:00 +12:45, the instant the second
    // pass begins; 02:00:00 +12:45 never occurs). Rebuild from the
    // transition instead: walk back to the fall-back T, take τ+ (the wall
    // time the clock reads AT T) and the field `first` resolved to, and
    // floor to the first post-jump instant displaying that field —
    // T + max(0, field - τ+). For aligned shifts this degenerates to T =
    // first + 1h (the pre-jump pass ran to the field's end), so New York
    // and Lord Howe floors are bit-identical; for Chatham field 02 it
    // lands on T (14:00Z) and for field 03 on T + 15min (14:15Z — the
    // real start of the second 03:00–04:00 pass).
    const first = until.minute(0).second(0).millisecond(0);
    if (first.utcOffset() > until.utcOffset()) {
      const fb = findFallBackBefore(until);
      if (fb) {
        const tauPlus = fb.t.hour() * 60 + fb.t.minute();
        const field = first.hour() * 60 + first.minute();
        return fb.t.add(Math.max(0, field - tauPlus), 'minute');
      }
      return first.add(1, 'hour');
    }
    // Spring-forward mirror: the fall-back branch above fires when
    // `first` resolved to the PRE-jump grid (a higher offset than until's
    // post-jump one). The symmetric spring-forward case is when the V8
    // setter rolls the non-existent minute-zeroed wall time FORWARD by
    // the gap's length — a Chatham spring `03:00+13:45` (in the gap
    // [02:45, 03:45) at +12:45) lands on `04:00+13:45` = 14:15Z, with
    // first's POST-jump offset EQUAL to until's (+13:45), so neither
    // offset comparison flags it. Walk back from `first` to locate the
    // transition T and rebuild the containing-hour start from it: T +
    // max(0, field - τ+) where τ+ is the wall the clock reads AT T
    // (post-jump). `field` is the wall hour field of `until` (the
    // containing wall hour in the post-jump world) — `until` is a real
    // post-jump instant with an unambiguous wall display, unlike V8's
    // rolled-forward `first`. The criterion `first > sf.t` (strict) keeps
    // the bit-identical whole-hour / half-hour / Chatham-fall-back cases
    // from going through the new branch: NY and Lord Howe spring land
    // first at T (first == T, no overshoot), and the Chatham fall-back
    // row's first is in the pre-jump world (T is much earlier, walk-back
    // finds no spring-forward). The misaligned Chatham spring overshoots
    // T by the 15-min τ+ − field-03 gap, and the rebuild pulls the floor
    // back to T = 14:00Z — the true start of the post-gap 03:00 wall
    // hour, whose [03:00, 03:45) portion is skipped by the gap.
    const sf = findSpringForwardBefore(first);
    if (sf && first.valueOf() > sf.t.valueOf()) {
      const tauPlus = sf.t.hour() * 60 + sf.t.minute();
      const field = until.hour() * 60;
      return sf.t.add(Math.max(0, field - tauPlus), 'minute');
    }
    return first;
  }
  if (granularity === 'day') return until.startOf('day');
  return until.startOf('month');
}

// exclusiveUntil is floorWindowUntil minus one second: the activity endpoint
// widens the query window to the bucket CONTAINING `until` (see
// activityWindow in admin.go), so one second before the floored bucket makes
// the widened window end exactly AT the floored bucket — the server then
// excludes the live bucket from its response instead of the client having to
// filter it. Used for the hourly-or-coarser server queries (Trends/Explore);
// sub-hour ranges keep the live hour in the response because its rows are
// the data source the client re-samples onto its minute axis.
export function exclusiveUntil(until: dayjs.Dayjs, granularity: Granularity): dayjs.Dayjs {
  return floorWindowUntil(until, granularity).subtract(1, 'second');
}

// ROLLUP_GRAN maps each API rollup to the granularity to align `until` to.
// Hour and total cut per hour (the server shapes both windows hourly — see
// activityWindow in admin.go); day and week cut per day (the week bucket is
// anchored to Monday, and the day floor never lands before `since` — the
// week rollup's boundary EXCLUSION then aligns to the week grid for
// past-period presets, see queryWindowUntil); month cuts per month.
const ROLLUP_GRAN: Record<string, 'hour' | 'day' | 'month'> = {
  hour: 'hour',
  day: 'day',
  week: 'day',
  month: 'month',
  total: 'hour',
};

// queryWindowUntil returns the `until` the activity server should receive
// for a range's current-period query. Sub-hour ranges pass the raw (live)
// time: their rows live in the current hour and are the data source the
// client re-samples onto its minute axis. Every other range passes one
// second before the ROLLUP bucket floor ONLY when range.until lies exactly
// on that boundary — the endpoint then excludes its live bucket cleanly
// (stable rolling views). A mid-bucket until (custom ranges, or a rollup
// coarser than the range, e.g. Explore's default day rollup on an
// hour-granularity 1d/today range) must NOT be floored to the rollup
// boundary: the endpoint widens the window to the bucket containing until
// (see activityWindow in admin.go), so flooring would amputate the whole
// in-progress day/week/month the range covers. Those ranges pass range.until
// as-is, letting the widened window keep every in-range bucket. (A WEEK
// rollup adds one exception for past-period presets: its buckets cut on the
// Monday grid, so the day-floor exclusion is aligned to the week grid — one
// second before the boundary week's Monday — excluding the whole boundary
// week; see the exclusion branch below.)
//
export function queryWindowUntil(range: Pick<DateRange, 'key' | 'granularity' | 'since' | 'until'>, rollup: string): dayjs.Dayjs {
  if (range.granularity === 'minute' || range.granularity === 'min15') return range.until;
  const gran = ROLLUP_GRAN[rollup] ?? 'hour';
  if (floorWindowUntil(range.until, gran).isSame(range.until)) {
    // A WEEK rollup anchors its buckets to MONDAY (activityWindow's week
    // branch), so whenever `until` is not itself a Monday a day-grid
    // exclusion (until-1s) still lies INSIDE the boundary week
    // [mondayOf(until), +7d): the widened server window then
    // includes that whole week, its bucket renders on the axis labeled with
    // the boundary Monday, and the CURRENT period's rows (everything after
    // range.until up to the next Monday) aggregate into it — the #137
    // "the previous period never picks up the current period's buckets"
    // violation (Prev Month + Weekly shows the next month's first days in
    // its last bucket; 1-6 days of leakage). PAST-period presets floor the
    // exclusion to the WEEK grid instead: one second before the boundary
    // week's Monday makes the server's widened window end exactly AT the
    // boundary week, excluding it entirely while the axis stays
    // Monday-anchored. (The day floor and the week floor coincide when
    // until IS a Monday — Prev Week — so those ranges are unchanged, as is
    // a range that lives wholly INSIDE the boundary week — the week starts
    // before range.since, e.g. a 24h Yesterday window: dropping the week
    // would amputate the entire range.) Custom ranges keep the day-aligned
    // exclusion: their picked bounds are never re-aligned, and the boundary
    // week's in-range days are their own chosen data (the leak past a
    // mid-week custom end is the inherent atomic-bucket trade-off).
    if (rollup === 'week' && PAST_RANGE_KEYS.has(range.key) && mondayOf(range.until).isAfter(range.since)) {
      return mondayOf(range.until).subtract(1, 'second');
    }
    return exclusiveUntil(range.until, gran);
  }
  return range.until;
}

// Preset and custom ranges use the same half-open window on every Activity
// tab. Keep this shared gate false so no caller can add a bucket that starts
// at the displayed end boundary and make the statistics exceed the range.
const PAST_RANGE_KEYS = new Set(['yesterday', 'prevweek', 'prevmonth', 'prevyear']);
export const liveExtensionEligible = (_range: Pick<DateRange, 'key'>): boolean => false;

// prevWindowUntil returns the `until` the activity server should receive for
// a range's PREVIOUS-period query — the since-side mirror of
// queryWindowUntil's until handling. The previous period is the half-open
// window [since - len, since); its query must never reach into the CURRENT
// period's buckets, but it must keep every in-window slice.
// Sub-hour granularities pass the raw `since` (as the old sub-hour branch
// of ActivityTrends did): their rows live in the containing hour and are the
// data source the client re-samples onto its minute axis (resampleResponse
// clips them to the window). Hourly-and-coarser queries pass one second
// before the bucket floor — exclusiveUntil — ONLY when `since` lies exactly
// on the bucket grid (preset ranges snap both bounds, see Activity.tsx):
// the endpoint's widened window (activityWindow: to = floor(until) + 1 unit)
// then ends exactly AT `since`, so the bucket starting there — which holds
// no in-window usage — stays out of the response. A mid-bucket `since`
// (custom ranges) must be passed RAW instead: the bucket CONTAINING it
// starts INSIDE the previous window, so its slice [floor(since), since) is
// previous-period data, and only the widened window (to = floor(since) + 1
// unit) can deliver that bucket's rows; normalizeHourlyResponse then applies
// the exact hourly slice share — the same share the
// Overview flow's bucketWindowShare computes for the same rows. The old
// exclusiveUntil path dropped that bucket ENTIRELY: the slice appeared in
// NEITHER the prev nor the cur response (the cur query prorates only
// [since, +1 unit)), so the Trends deltas/sparklines disagreed with the
// Overview KPIs on identical windows and the boundary usage was counted
// nowhere.
export function prevWindowUntil(since: dayjs.Dayjs, granularity: Granularity): dayjs.Dayjs {
  if (granularity === 'minute' || granularity === 'min15') return since;
  return floorWindowUntil(since, granularity).isSame(since)
    ? exclusiveUntil(since, granularity)
    : since;
}

// customRange wraps a user-picked from/to window; the bucket granularity is
// derived from the window's length.
export function customRange(since: dayjs.Dayjs, until: dayjs.Dayjs): DateRange {
  return { key: CUSTOM_KEY, label: CUSTOM_LABEL, badge: '', since, until, granularity: granularityFor(since, until) };
}

// granularityFor picks the bucket size for an arbitrary (custom) window:
// up to 1h per minute, up to 3h per 15 minutes, sub-3-day windows hourly,
// up to two months daily, longer monthly.
export function granularityFor(since: dayjs.Dayjs, until: dayjs.Dayjs): Granularity {
  const hours = until.diff(since, 'hour', true);
  if (hours <= 1) return 'minute';
  if (hours <= 3) return 'min15';
  if (hours < 72) return 'hour';
  if (hours < 24 * 60) return 'day';
  return 'month';
}

export const fmtCompact = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e12) return (v / 1e12).toFixed(2).replace(/\.?0+$/, '') + 'T';
  if (abs >= 1e9) return (v / 1e9).toFixed(2).replace(/\.?0+$/, '') + 'B';
  if (abs >= 1e6) return (v / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M';
  if (abs >= 1e3) return (v / 1e3).toFixed(1).replace(/\.?0$/, '') + 'K';
  return String(Math.round(v));
};

export const fmtUSD = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return `$${(v / 1e3).toFixed(1)}k`;
  return `$${v.toFixed(2)}`;
};

// fmtUSDInt renders integer-dollar axis ticks like OpenRouter ($0, $2, $4).
export const fmtUSDInt = (v: number): string => {
  if (v >= 1000) return `$${Math.round(v / 1000)}k`;
  return `$${Math.round(v)}`;
};

// fmtDayLabel renders "MMM D" (Jul 11) for a "MM-DD" bucket string or a
// year-qualified "YYYY-MM-DD" one.
export const fmtDayLabel = (mmdd: string): string => {
  const parts = mmdd.split('-').map(Number);
  const m = parts.length >= 3 ? parts[1] : parts[0];
  const d = parts.length >= 3 ? parts[2] : parts[1];
  if (!m || !d) return mmdd;
  return `${MONTHS[m - 1] || m} ${d}`;
};

// fmtTick renders a bucket label for chart axes: minute -> "15:50",
// hour -> "15:00", day ("MM-DD" or "YYYY-MM-DD") -> "Jul 13",
// week ("YYYY-MM-DD") -> "Aug 13", month ("YYYY-MM") -> "Aug '26".
export function fmtTick(granularity: Granularity, label: string): string {
  if (granularity === 'hour' || granularity === 'minute' || granularity === 'min15') {
    // "YYYY-MM-DD HH:00"/"YYYY-MM-DD HH:mm" -> slice(11) = the time;
    // client-side "MM-DD HH:mm" -> slice(6).
    return label.indexOf('-') === 4 ? label.slice(11) : label.slice(6);
  }
  if (label.length >= 7 && label.indexOf('-') === 4) {
    const [y, m, d] = label.split('-');
    // Month buckets ("YYYY-MM") render the year explicitly — a bare "Aug 26"
    // reads as a DAY on a year-long axis.
    return d ? `${MONTHS[Number(m) - 1] || m} ${Number(d)}` : `${MONTHS[Number(m) - 1] || m} '${y.slice(2)}`;
  }
  return fmtDayLabel(label);
}

// fmtBucket renders a bucket label for tooltips: "Jul 13, 15:50" /
// "Jul 13" / "Aug 13, 2026" / "Aug 2026".
export function fmtBucket(granularity: Granularity, label: string): string {
  if (granularity === 'hour' || granularity === 'minute' || granularity === 'min15') {
    const [md, hm] = label.split(' ');
    return `${fmtDayLabel(md)}, ${hm}`;
  }
  if (label.length >= 7 && label.indexOf('-') === 4) {
    const [y, m, d] = label.split('-');
    return d ? `${MONTHS[Number(m) - 1] || m} ${Number(d)}, ${y}` : `${MONTHS[Number(m) - 1] || m} ${y}`;
  }
  return fmtDayLabel(label);
}

// fmtTokensNoSuffix renders bare compact tokens (362M) for KPI values.
export const fmtTokensBare = (v: number): string => fmtCompact(v);

export const fmtTokens = (v: number): string => fmtCompact(v) + ' tok';

export const fmtPercent = (v: number): string => {
  if (v >= 100) return `${Math.round(v)}%`;
  if (v >= 1) return `${v.toFixed(1)}%`;
  return `${v.toFixed(2)}%`;
};

// cacheHitRate = cached / TOTAL input tokens (incl. cached), in percent.
// The backend stores input_tokens under one convention for every provider:
// total input including cached tokens (OpenAI's prompt_tokens includes
// cached_tokens; Anthropic's input_tokens is folded at record time), so the
// denominator is input alone — adding cache again would double-count it and
// collapse high rates toward ~50%. Clamped to 100: legacy Anthropic rows
// stored input WITHOUT cache, where cached tokens can exceed input.
export const cacheHitRate = (input: number, cache: number): number => {
  if (input <= 0) return 0;
  return Math.min(100, (cache / input) * 100);
};

export const GRID = 'rgba(120,120,140,0.14)';
export const AXIS = 'rgba(120,120,140,0.75)';

// OpenRouter chart palette (from the saved page CSS, chart-1..chart-20)
export const CHART_COLORS = [
  '#0088fe', '#00c49f', '#ffbb28', '#ff8042', 'tomato', '#4682b4',
  '#9acd32', 'orchid', '#40e0d0', '#ff69b4', '#daa520', '#7b68ee',
  '#f08080', '#6b8e23', '#db7093', '#3cb371', '#bdb76b', 'purple',
  '#ff4500', '#2e8b57',
];
export const OTHER_COLOR = '#94a3b8';

// fmt3sig formats money to 3 significant figures like OpenRouter
// ($0.00325, $0.0502, $0.478, $1.15, $3.11, $41.2k, $1.5M).
export const fmt3sig = (v: number): string => {
  if (v === 0) return '$0';
  const abs = Math.abs(v);
  if (abs >= 1e9) return `$${(v / 1e9).toPrecision(3)}B`;
  if (abs >= 1e6) return `$${(v / 1e6).toPrecision(3)}M`;
  if (abs >= 1e3) return `$${(v / 1e3).toPrecision(3)}k`;
  const s = v.toPrecision(3);
  return `$${parseFloat(s)}`;
};

export interface FaviconInfo { url: string | null; letter: string; color: string; }

// Vendor icons saved from the OpenRouter reference page (web/public/icons).
const VENDOR_ICONS: { match: RegExp; url: string }[] = [
  { match: /deepseek/i, url: '/icons/DeepSeek.png' },
  { match: /claude|anthropic/i, url: '/icons/Anthropic.svg' },
  { match: /gemini|google/i, url: '/icons/GoogleGemini.svg' },
  { match: /gpt|o1[-\s]|o3[-\s]|o4[-\s]|openai|chatgpt/i, url: '/icons/OpenAI.svg' },
];

// modelFavicon resolves a model name to a vendor favicon (OpenRouter shows
// one per row in the Explore table). Unknown vendors fall back to a
// deterministic letter avatar so the cell never looks empty.
export function modelFavicon(name: string): FaviconInfo {
  const m = VENDOR_ICONS.find(v => v.match.test(name));
  if (m) return { url: m.url, letter: '', color: '' };
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return {
    url: null,
    letter: (name.charAt(0) || '?').toUpperCase(),
    color: CHART_COLORS[h % CHART_COLORS.length],
  };
}

// --- Time-series bucketing -------------------------------------------------
// The backend stores hourly consumption rows; the charts bucket them by the
// selected range's granularity so a 24h view draws 24 hourly points, a
// month view draws daily points and a year view monthly ones (OR behavior).
// Sub-hour granularities (minute / min15) re-sample the hourly rows onto a
// fine axis — see overlapFractions.

// A row needs only hour_bucket for bucketing; callers pass their own value
// extractor.
export interface BucketedRow { hour_bucket: string; }

export interface SeriesPoint {
  label: string; // human label: "MM-DD HH:mm" / "MM-DD HH:00" / "MM-DD" / "YYYY-MM"
  sort: string;  // sortable key
  value: number;
}

// STEP_MIN is the fixed minute step of the sub-hour granularities; the
// calendar-unit ones (null) advance by that unit instead.
const STEP_MIN: Record<Granularity, number | null> = {
  minute: 1,
  min15: 15,
  hour: null,
  day: null,
  month: null,
};

// bucketLabel maps a bucket start to its axis label/sort key.
function bucketLabel(granularity: Granularity, t: dayjs.Dayjs): { label: string; sort: string } {
  switch (granularity) {
    case 'minute':
    case 'min15':
      return { label: t.format('MM-DD HH:mm'), sort: t.format('YYYY-MM-DD HH:mm') };
    case 'hour': return { label: t.format('MM-DD HH:00'), sort: t.format('YYYY-MM-DD HH:00') };
    case 'day': return { label: t.format('MM-DD'), sort: t.format('YYYY-MM-DD') };
    case 'month': return { label: t.format('YYYY-MM'), sort: t.format('YYYY-MM') };
  }
}

// bucketStarts lists the bucket START times strictly inside the half-open
// window [since, until) at the granularity's step (sub-hour granularities
// advance by STEP_MIN minutes and the min15 axis is floored to the
// 15-minute clock cell; the others advance by their calendar unit). The
// bucket starting AT `until` is excluded from this list: it lies outside the
// window, and the overlap clamps in overlapFractions / bucketWindowShare
// yield 0 for it — so it would only render an always-empty trailing tick
// (callers may opt into an explicit live-cell extension, but preset views use
// the default half-open axis). Buckets are kept only while their sort key strictly
// increases: DST fall-back repeats wall-clock times (dayjs steps in elapsed
// time) — for hour granularity the repeat is consecutive ("01:00" twice),
// for minute steps the whole repeated hour re-appears non-consecutively
// ("01:59" then "01:00" again) — dropping the repeats keeps the axis
// monotonic and the repeated hour's usage lands on its first occurrence. The
// initial floor must preserve the instant, however: startOf('minute')/
// startOf('hour') reconstructs an ambiguous second-occurrence wall time as
// the first occurrence, moving a short window such as [01:15 EST, 01:30 EST)
// onto the wrong axis.
function bucketStarts(since: dayjs.Dayjs, until: dayjs.Dayjs, granularity: Granularity): dayjs.Dayjs[] {
  const stepMin = STEP_MIN[granularity];
  const unit = granularity === 'hour' ? 'hour' : granularity === 'day' ? 'day' : 'month';
  const out: dayjs.Dayjs[] = [];
  let cur = stepMin != null ? floorMinute(since)
    : granularity === 'hour' ? floorWindowUntil(since, 'hour')
    : granularity === 'day' ? since.startOf('day')
    : since.startOf('month');
  if (granularity === 'min15') cur = cur.subtract(cur.minute() % 15, 'minute');
  let prevSort = '';
  while (cur.isBefore(until)) {
    const sort = bucketLabel(granularity, cur).sort;
    if (sort > prevSort) { out.push(cur); prevSort = sort; }
    if (stepMin != null) {
      cur = cur.add(stepMin, 'minute');
      continue;
    }
    const next = cur.add(1, unit);
    if (granularity === 'hour' && next.minute() !== 0
      && (findFallBackBefore(cur) || findSpringForwardBefore(cur))) {
      // A fractional DST transition can leave the elapsed next hour inside
      // the next wall-clock hour (Chatham fall-back: 02:45 -> 03:45; Lord
      // Howe spring-forward: 02:30 -> 03:30). Advance to that hour's real
      // start so an empty response bucket is still represented.
      cur = next.subtract(next.minute(), 'minute');
    } else {
      cur = next;
    }
  }
  return out;
}

// hasLiveCell is true only when a caller explicitly opts into adding the
// bucket that starts at the range's snapped `until` as an extra trailing
// point. The default Activity views keep the half-open [since, until) range:
// the bucket at the displayed end is outside the preset's statistics. The
// opt-in path remains available for the legacy/DST coverage tests below; its
// alignment is judged from the RANGE's own until, never from fetch-time
// `cutoff`, and the recorded extent is still capped at whole minutes.
function hasLiveCell(
  until: dayjs.Dayjs,
  since: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
  liveExtend: boolean,
): boolean {
  if (!liveExtend) return false;
  if (!floorWindowUntil(until, granularity).isSame(until)) return false;
  if (!floorWindowUntil(since, granularity).isSame(since)) return false;
  const unit = granularity === 'day' ? 'day' : granularity === 'month' ? 'month' : 'hour';
  // The recorded extent reads the whole-minute floor on the EPOCH grid
  // (floorMinute — a second-occurrence cutoff must keep its true minute,
  // never the first-occurrence re-anchor startOf would give), so a live
  // cell with data on the repeated hour's second grid is never mistaken
  // for an empty one.
  const extent = (granularity === 'minute' || granularity === 'min15')
    ? floorMinute(cutoff).diff(until, 'minute', true)
    : rowCoverageEnd(until, unit, cutoff).diff(until, 'minute', true);
  return extent > 0;
}

// livePoint builds the CURRENT window's live-bucket SeriesPoint, or null
// when the window must not extend (see hasLiveCell) or the point's label
// would collide with a bucket already on the axis (on a DST fall-back the
// axis holds the FIRST occurrence's labels, so a live cell landing on the
// repeated hour's second occurrence would duplicate a tick — the point is
// skipped, but the cell's recorded slice still counts: overlapFractions
// folds it onto the existing same-label tick, so the chart total keeps
// agreeing with the KPI's proration, which counts the slice the same way).
function livePoint(
  until: dayjs.Dayjs,
  since: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
  existingLabels: Set<string>,
  liveExtend: boolean,
): SeriesPoint | null {
  if (!hasLiveCell(until, since, cutoff, granularity, liveExtend)) return null;
  const f = bucketLabel(granularity, until);
  if (existingLabels.has(f.label)) return null;
  return { label: f.label, sort: f.sort, value: 0 };
}

// bucketAxis builds a CONTINUOUS bucket axis over since..until at the given
// granularity (empty buckets included, like OR's full-range charts).
export function bucketAxis(since: dayjs.Dayjs, until: dayjs.Dayjs, granularity: Granularity): SeriesPoint[] {
  return bucketStarts(since, until, granularity).map(t => {
    const f = bucketLabel(granularity, t);
    return { label: f.label, sort: f.sort, value: 0 };
  });
}

// axisForRows adds serialized hourly labels that the elapsed-time walk cannot
// represent on a spring-forward transition. It is intentionally row-driven:
// a normal zone keeps bucketAxis's existing DST behavior (including skipping
// a nonexistent empty hour), while a real response row such as Chatham's
// partial 03:00 bucket gets a visible axis point and a matching sort key.
function axisForRows<T extends BucketedRow>(
  list: T[],
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  granularity: Granularity,
): SeriesPoint[] {
  const axis = bucketAxis(since, until, granularity);
  if (granularity !== 'hour' || list.length === 0) return axis;

  const points = new Map(axis.map(p => [p.sort, p]));

  for (const row of list) {
    // Use the real coverage runs rather than lexicographic wall-clock bounds.
    // A normalized Chatham 04:00 row has a 03:00 alias beginning at the spring
    // transition, even when the serialized 04:00 label is at the window end.
    const labels = hourWallRunsForBucket(row.hour_bucket);
    const serializedSort = hourSortFromBucket(row.hour_bucket);
    const serializedOverlaps = serializedSort !== undefined
      && labels.some(label => label.sort === serializedSort
        && label.runs.some(run => run.from < until.valueOf() && run.to > since.valueOf()));
    for (const label of labels) {
      // A normalized row is indistinguishable from a valid row once the API
      // formats it as `YYYY-MM-DD HH:00`. If the serialized label itself has
      // visible coverage, keep that label authoritative; only expose an
      // alias when the serialized label starts at the displayed end and the
      // alias is the part of the merged row inside the window.
      if (serializedOverlaps && label.sort !== serializedSort) continue;
      if (!label.runs.some(run => run.from < until.valueOf() && run.to > since.valueOf())) continue;
      if (!points.has(label.sort)) {
        points.set(label.sort, {
          label: `${label.sort.slice(5, 10)} ${label.sort.slice(11)}`,
          sort: label.sort,
          value: 0,
        });
      }
    }
  }
  return [...points.values()].sort((a, b) => a.sort.localeCompare(b.sort));
}

// isRepeatHour is true when the hourly row anchored at `start` is affected
// by a DST fall-back repeat: the repeated wall-clock span intersects the
// row's hour field (see repeatRuns). The detection derives from the OFFSET
// CHANGE at the transition, never from hour-field equality — the old
// `start.add(1,'hour').hour() === start.hour()` test modeled every repeat
// as hour-field-aligned. That held for New York and Lord Howe (their
// repeated spans stay inside one field) but broke on the 45-minute-offset
// Pacific/Chatham shift, whose repeated span [02:45, 03:45) crosses the
// 02/03 field boundary: row '02:00' (its +1h step lands on field 03 — no
// match) lost its second-pass slice [14:00Z, 14:15Z) entirely, while row
// '03:00' matched only by accident.
function isRepeatHour(start: dayjs.Dayjs): boolean {
  return repeatRuns(start) !== null;
}

// RowCoverage is an hourly (or coarser calendar-bucket) row's recorded
// extent: the epoch runs (whole-minute cutoff-clamped) plus the coverage in
// whole minutes.
export interface RowCoverage {
  runs: Array<{ from: number; to: number }>;
  coverage: number;
}

// rowCoverage returns the extent an hourly row's recorded value covers: the
// fall-back runs from repeatRuns, the spring-forward runs from springRuns, or
// the plain one-row-unit span, each run capped at the whole-minute cutoff
// floor and the whole extent rescued to one minute when the cutoff lands
// inside the unit's FIRST minute — the same cutoff semantics the old
// rowCoverageEnd applied to its single contiguous span, now per run. Shared by
// bucketWindowShare, overlapFractions and boundaryShare so the KPI
// proration and the sub-hour chart split divide by the SAME coverage and
// clamp to the SAME runs (the gap between a misaligned row's runs displays
// the OTHER row's hour and must never count for either). `wallField` keeps a
// nonexistent spring-forward label such as Chatham's `03:00` distinct from
// the valid `04:00` instant to which the host Date parser rolls it.
//
// The floor must NOT go through dayjs's startOf('minute'): on the
// fall-back's repeated hour a second-occurrence cutoff (01:15:45 EST)
// floors to 01:15 EST — wall-clock fields that ALSO exist in the first
// occurrence — and the ambiguous-local reconstruction inside the JS
// Date setter re-anchors the instant to the FIRST occurrence (01:15
// EDT, one hour earlier). The coverage would then end before a live
// window's start and the whole recent usage reads 0 (the "#135 line
// ends at the real last in-window value" contract), prorated wrong
// everywhere else. Floor on the epoch instead (floorMinute): whole-
// minute offsets keep the epoch and the local minute grids on the same
// boundaries, preserving the instant — 01:15:45 EST floors to 01:15
// EST, its real minute.
export function rowCoverage(
  start: dayjs.Dayjs,
  rowUnit: 'hour' | 'day' | 'week' | 'month',
  cutoff: dayjs.Dayjs,
  wallField?: number,
): RowCoverage {
  let base: Array<{ from: number; to: number }>;
  if (rowUnit !== 'hour') {
    base = [{ from: start.valueOf(), to: start.add(1, rowUnit).valueOf() }];
  } else {
    // A normalized spring-forward bucket can represent more than one local
    // label at the same instant. Build each label's real epoch coverage and
    // merge touching runs: Lord Howe's normalized 02:30 row is 30 minutes,
    // while Chatham's normalized 04:00 row combines 03:45..04:00 with the
    // valid 04:00..05:00 hour for a 75-minute run.
    base = mergeCoverageRuns(
      hourWallLabelsForInstant(start, wallField).flatMap(label => {
        const repeated = repeatRuns(label.start);
        const spring = repeated ? null : springRuns(label.start, label.field);
        return repeated ?? spring ?? [{
          from: label.start.valueOf(),
          to: label.start.add(1, 'hour').valueOf(),
        }];
      }),
    );
  }
  // Whole-minute extent (see floorMinute above): a cutoff inside any run's
  // first minute must retain one minute of that run. This also keeps the UI
  // and backend in sync when a spring-normalized row has multiple aliases.
  const runs = clampCoverageRuns(base, cutoff);
  let coverage = 0;
  for (const r of runs) coverage += (r.to - r.from) / 60000;
  return { runs, coverage };
}

// rowCoverageEnd returns the instant an hourly row's recorded value extends
// to: the end of its LAST coverage run (see rowCoverage), capped at `cutoff`
// read as WHOLE MINUTES. Only the >-0 gate of hasLiveCell consumes the bare
// instant; every extent-aware consumer reads the runs' coverage directly.
function rowCoverageEnd(start: dayjs.Dayjs, rowUnit: 'hour' | 'day' | 'week' | 'month', cutoff: dayjs.Dayjs): dayjs.Dayjs {
  const { runs } = rowCoverage(start, rowUnit, cutoff);
  return runs.length > 0 ? dayjs(runs[runs.length - 1].to) : dayjs(start.valueOf());
}

function hourAxisStarts<T extends BucketedRow>(
  list: T[],
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  axis: SeriesPoint[],
): Array<dayjs.Dayjs | undefined> {
  const starts = new Map<string, dayjs.Dayjs>();
  for (const start of bucketStarts(since, until, 'hour')) {
    starts.set(bucketLabel('hour', start).sort, start);
  }
  for (const row of list) {
    for (const label of hourWallRunsForBucket(row.hour_bucket)) {
      if (!label.runs.length || starts.has(label.sort)) continue;
      starts.set(label.sort, dayjs(label.runs[0].from));
    }
  }
  // A live extension may append the point at `until` after axisForRows has
  // built its row-derived map. Its instant is unambiguous because the point
  // was created from the range boundary itself.
  const untilSort = bucketLabel('hour', until).sort;
  if (!starts.has(untilSort)) starts.set(untilSort, until);
  return axis.map(point => starts.get(point.sort));
}

function hourlyAxisFractions<T extends BucketedRow>(
  row: T,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  axis: SeriesPoint[],
  starts: Array<dayjs.Dayjs | undefined>,
  liveCell: boolean,
): Array<[number, number]> {
  const start = dayjs(row.hour_bucket).startOf('hour');
  const coverage = rowCoverage(start, 'hour', cutoff, hourFieldFromBucket(row.hour_bucket));
  if (coverage.coverage <= 0) return [];
  const fractions = new Map<number, number>();
  const addOverlap = (index: number, lo: number, hi: number) => {
    if (hi > lo) fractions.set(index, (fractions.get(index) ?? 0) + (hi - lo) / 60000 / coverage.coverage);
  };

  const serializedSort = hourSortFromBucket(row.hour_bucket);
  const serializedIndex = serializedSort === undefined
    ? -1
    : axis.findIndex(point => point.sort === serializedSort);
  if (serializedIndex >= 0) {
    let overlap = 0;
    for (const run of coverage.runs) {
      overlap += Math.max(0, Math.min(run.to, until.valueOf()) - Math.max(run.from, since.valueOf()));
    }
    if (liveCell) {
      const cellEnd = until.add(1, 'hour').valueOf();
      for (const run of coverage.runs) {
        overlap += Math.max(0, Math.min(run.to, cellEnd) - Math.max(run.from, until.valueOf()));
      }
    }
    if (overlap > 0) return [[serializedIndex, overlap / 60000 / coverage.coverage]];
    return [];
  }

  // The serialized label is outside the half-open axis. This is the
  // normalized Chatham shape: the persisted 04:00 label starts at the end of
  // the skipped 03:00 row, while its alias carries the visible boundary
  // slice. Split only this fallback by real coverage intervals.
  for (const [index, cellStart] of starts.entries()) {
    if (!cellStart) continue;
    const cellEnd = starts[index + 1]?.valueOf() ?? cellStart.add(1, 'hour').valueOf();
    if (cellEnd <= cellStart.valueOf()) continue;
    for (const run of coverage.runs) {
      addOverlap(
        index,
        Math.max(run.from, since.valueOf(), cellStart.valueOf()),
        Math.min(run.to, until.valueOf(), cellEnd),
      );
    }
  }

  if (liveCell) {
    const liveSort = bucketLabel('hour', until).sort;
    const index = axis.findIndex(point => point.sort === liveSort);
    if (index >= 0) {
      const cellEnd = until.add(1, 'hour').valueOf();
      for (const run of coverage.runs) {
        addOverlap(index, Math.max(run.from, until.valueOf()), Math.min(run.to, cellEnd));
      }
    }
  }
  return [...fractions.entries()];
}

// overlapFractions splits ONE hourly row across the sub-hour axis buckets
// overlapping the WINDOW [since, until), returning [axisIndex, fraction]
// pairs. A normal row covers [hour, hour+1h). On DST transitions its real
// coverage comes from rowCoverage: a fall-back may have 120 contiguous
// minutes (or two runs with a gap for the 45-minute-misaligned Chatham
// shift), while a spring-forward row may be shorter than an hour. It is
// still capped at `cutoff`: the
// time its value was recorded: the current hour accumulates live usage, so
// a PAST window (previous period, calendar preset) shares its boundary hour
// with the live window, and that hour's value must be divided by its real
// coverage (cutoff - hour), not by (until - hour). The coverage reads the
// recorded extent as WHOLE MINUTES (cutoff floored to the minute grid):
// the row value is a per-request accumulation whose last event time is
// unknowable, so clamping to the raw fetch clock would divide the SAME
// value by a growing coverage on every 30s auto-refresh and shrink every
// window total (2.5777 -> 2.5160 -> 2.4571 on an unchanged row, verified
// at 14:20:22 / 14:20:52 / 14:21:22). Whole-minute coverage is a function
// of the window grid instead — a snapped minute-granularity preset's until
// IS the floored cutoff, so unchanged rows render identical values between
// refreshes (the 'perfectly stable' contract in Activity.tsx; min15 windows
// and custom ranges re-read the extent at each minute roll — the row's last
// event time is unknowable, and any fixed read of it trades accuracy for
// stability). Both windows sharing the live boundary hour divide by the same
// coverage so their shares still sum to the whole row, never double-counted.
// Each bucket gets the fraction of the row proportional to its overlap with
// the row's data and the window — the uniform-within-hour assumption needed
// because the stored data is hourly while the axis is finer. Values
// outside the window are never counted, so the window total is preserved.
function overlapFractions(
  hourBucket: string,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  starts: dayjs.Dayjs[],
  stepMin: number,
  liveCell: boolean,
): Array<[number, number]> {
  const h = dayjs(hourBucket).startOf('hour');
  const row = rowCoverage(h, 'hour', cutoff, hourFieldFromBucket(hourBucket));
  if (row.coverage <= 0) return [];
  const perMin = 1 / row.coverage;
  const out: Array<[number, number]> = [];

  // A normalized spring row is already aggregated under its serialized
  // successor label by the API. When that label is visible, keep the whole
  // corrected row on that source label rather than moving its pre-successor
  // alias onto a different minute cell. If the source label is outside the
  // half-open axis, the ordinary run-based fallback below places the visible
  // boundary slice on the axis instead.
  const labels = hourWallRunsForBucket(hourBucket);
  const serializedSort = hourSortFromBucket(hourBucket);
  if (labels.length > 1 && serializedSort !== undefined) {
    const target = starts.findIndex(start =>
      dayjs(start).format('YYYY-MM-DD HH:00') === serializedSort,
    );
    if (target >= 0) {
      let overlap = 0;
      for (const run of row.runs) {
        overlap += Math.max(0, Math.min(run.to, until.valueOf()) - Math.max(run.from, since.valueOf()));
      }
      if (liveCell) {
        const cellEnd = until.add(stepMin, 'minute').valueOf();
        for (const run of row.runs) {
          overlap += Math.max(0, Math.min(run.to, cellEnd) - Math.max(run.from, until.valueOf()));
        }
      }
      if (overlap > 0) return [[target, overlap / 60000 * perMin]];
      return [];
    }
  }

  if (isRepeatHour(h) || isRepeatHour(h.subtract(1, 'hour'))) {
    // DST fall-back: the repeated wall-clock hour covers two elapsed hours
    // (a non-contiguous pair of runs for the 15-minute-misaligned Chatham
    // shift — see repeatRuns; each run iterated separately here, so the gap
    // between them, which displays the OTHER row's hour, never counts for
    // this row). isRepeatHour(h) = the row anchors on the FIRST occurrence;
    // the subtract(1,'hour') variant catches a row SERIALIZED with the
    // second occurrence's offset (hour_bucket "01:00 EST" — today's server
    // truncates to the local hour and merges both occurrences into the
    // first's row, but a future backend or serialization variant could
    // keep the -05:00 row). dayjs's startOf('hour') above re-anchors the
    // second row onto the first on current engines (ambiguous wall-clock —
    // the same setter disambiguation the cutoff floor must dodge), which
    // makes the variant a safety net for engines without the re-anchor —
    // either way the row must take this path: the axis is a MONOTONIC
    // wall-clock grid — bucketStarts dropped the second occurrence's
    // labels (their sort keys sort below the already-emitted
    // first-occurrence cells), so those minutes fold onto the same-label
    // first-occurrence buckets (the repeated hour's usage lands on its
    // first occurrence, like the hour-granularity axis). Each covered
    // MILLISECOND of the row inside the window counts once, under its
    // wall-clock cell's label (partial edge minutes are split at the
    // wall-clock minute boundaries, so second-precision fetch times stay
    // exact). Milliseconds whose label the window's axis does not contain
    // (the transition's fringe — a window starting mid-hour and ending in
    // the repeat) are spread evenly over the row's visible buckets, so the
    // chart total still equals bucketWindowShare's KPI proration for the
    // same window and rows.
    const idx = new Map<string, number>();
    for (let i = 0; i < starts.length; i++) idx.set(starts[i].format('YYYY-MM-DD HH:mm'), i);
    // When the explicit live-cell extension is enabled, the cell lies past
    // until: the row's coverage must reach its end there (never past it —
    // beyond the cell the row belongs to the next window's data). On a DST
    // repeat the label can collide with an existing tick; the cell's minutes
    // then fold onto that tick so the chart still matches the KPI.
    const cellHi = liveCell ? until.add(stepMin, 'minute').valueOf() : until.valueOf();
    const counts = new Map<number, number>(); // axis index -> covered ms
    let lost = 0; // ms whose wall-clock label the axis cannot show
    for (const run of row.runs) {
      const lo = Math.max(run.from, since.valueOf());
      const hi = Math.min(run.to, cellHi);
      for (let t = lo; t < hi; ) {
        // Next WALL-clock minute boundary (TZ-aware — half-hour zones like
        // Lord Howe do not align with epoch minutes); on a boundary the
        // whole minute counts.
        const wallMs = new Date(t).getSeconds() * 1000 + new Date(t).getMilliseconds();
        const next = Math.min(t + (wallMs === 0 ? 60000 : 60000 - wallMs), hi);
        const minute = new Date(t).getMinutes();
        const cellMs = stepMin === 15 ? t - (minute % 15) * 60000 : t;
        const i = idx.get(dayjs(cellMs).format('YYYY-MM-DD HH:mm'));
        const ms = next - t;
        if (i !== undefined) counts.set(i, (counts.get(i) ?? 0) + ms);
        else lost += ms;
        t = next;
      }
    }
    if (lost > 0 && counts.size > 0) {
      const per = Math.floor(lost / counts.size);
      let rem = lost % counts.size;
      for (const [i, c] of counts) counts.set(i, c + per + (rem-- > 0 ? 1 : 0));
    } else if (lost > 0 && starts.length > 0) {
      // Total-safety arm: on runtimes where dayjs keeps the second
      // occurrence's offset (no startOf re-anchor — the V8 setter
      // disambiguation is engine-dependent), or for any row whose covered
      // slice's wall-clock labels the axis lacks ENTIRELY, the old
      // counts.size > 0 gate dropped the row's whole value while
      // bucketWindowShare's KPI proration counted it — the chart total
      // fell below the KPI by exactly the row's in-window slice. Spread
      // the covered ms evenly over the visible buckets instead, mirroring
      // the KPI's uniform-within-hour semantics; the total stays equal.
      const per = Math.floor(lost / starts.length);
      let rem = lost % starts.length;
      for (let i = 0; i < starts.length; i++) counts.set(i, per + (rem-- > 0 ? 1 : 0));
    }
    const coverageMs = row.runs.reduce((a, r) => a + (r.to - r.from), 0);
    for (const [i, c] of counts) out.push([i, c / coverageMs]);
    return out;
  }
  const covStart = row.runs[0].from;
  const covEnd = row.runs[0].to;
  for (let i = 0; i < starts.length; i++) {
    const s = starts[i];
    const e = s.add(stepMin, 'minute');
    // If the explicit live-cell extension appended a cell at `until`, its
    // recorded slice reaches to the row's coverage end, not to `until`.
    const clampEnd = s.valueOf() >= until.valueOf() ? covEnd : until.valueOf();
    const overlap = Math.min(e.valueOf(), covEnd, clampEnd)
      - Math.max(s.valueOf(), covStart, since.valueOf());
    if (overlap > 0) out.push([i, perMin * (overlap / 60000)]);
  }
  return out;
}

// bucketWindowShare returns the fraction of a bucket's recorded value that
// lies inside [since, until]. hour_bucket rows are truncated to the LOCAL
// hour; chart granularity only chooses the axis bucket. Every chart scale
// therefore shares the same hourly-row coverage math.
//
// A bucket covers [start, start+unit); its recorded value only covers up to
// `cutoff` (the fetch time) — the bucket containing cutoff accumulates live
// usage, so its value is divided by the real coverage (rowCoverage, which
// also spans a DST fall-back's repeated hour — as one contiguous run for
// whole-hour and half-hour shifts, two runs for the misaligned Chatham
// shift), never by a window boundary (a
// past window sharing that hour must not inflate it). The coverage reads the
// recorded extent as WHOLE MINUTES (cutoff floored to the minute grid — see
// overlapFractions): the row value is a per-request accumulation whose last
// event time is unknowable, so the raw fetch clock would shrink every window
// total between 30s auto-refreshes even when the rows are unchanged.
// Whole-minute coverage stays put while the fetch clock slides, so identical
// windows + identical rows render identical values for minute-granularity
// presets (whose snapped until IS the floored cutoff; min15/custom re-read
// at each minute roll — see overlapFractions), and every window sharing the
// live boundary hour divides by the same coverage (their shares still sum to
// the whole row).
//
// Interior buckets lie fully inside the window and keep share 1; boundary
// buckets are prorated to the overlap, so a rolling window shows exactly
// the data inside its span and repeated auto-refreshes never accumulate
// pre-window (or post-window) usage into the totals.
//
  // When `liveExtend` is explicitly true, the bucket that starts at the
  // snapped `until` is treated as an extra cell: its recorded slice is added
  // on top of the until-clamped overlap, mirroring livePoint and
  // overlapFractions. Activity views leave this opt-in disabled so the KPI
  // remains exactly within the displayed half-open range.
export function bucketWindowShare(
  hourBucket: string,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
  liveExtend = false,
): number {
  const h = dayjs(hourBucket);
  // The API returns one row per hour even when the chart aggregates those
  // rows into daily or monthly points. Prorate each row against its actual
  // hour; using the containing day/month here makes every row on a boundary
  // inherit the same calendar share.
  const start = h.startOf('hour');
  const row = rowCoverage(start, 'hour', cutoff, hourFieldFromBucket(hourBucket));
  const coverage = row.coverage;
  if (coverage <= 0) return 0;
  // The optional live cell uses the SAME hasLiveCell gate as the chart side.
  // On the repeated hour its slice still counts even though its wall-clock
  // label duplicates a first-occurrence tick: livePoint skips the duplicate
  // point, but overlapFractions folds the slice onto that existing tick, so
  // the KPI and chart always count the same coverage. The default
  // liveExtend=false keeps the displayed half-open range for every preset.
  const liveCell = hasLiveCell(until, since, cutoff, granularity, liveExtend);
  const stepMin = STEP_MIN[granularity];
  // Overlap is summed over the row's coverage RUNS, so the gap between a
  // misaligned repeat's runs (displaying the OTHER row's hour) never counts
  // for this row — the chart's fold iterates the same runs.
  let overlap = 0;
  for (const r of row.runs) {
    overlap += Math.max(0, Math.min(r.to, until.valueOf()) - Math.max(r.from, since.valueOf()));
  }
  if (liveCell) {
    // The row's recorded slice inside the live cell [until, until+step)
    // counts too — the chart distributes it there (overlapFractions clamps
    // the appended cell at covEnd), so the KPI must match.
    const cellEnd = stepMin != null
      ? until.add(stepMin, 'minute')
      : until.add(1, granularity === 'hour' ? 'hour' : granularity === 'day' ? 'day' : 'month');
    for (const r of row.runs) {
      overlap += Math.max(0, Math.min(r.to, cellEnd.valueOf()) - Math.max(r.from, until.valueOf()));
    }
  }
  if (overlap <= 0) return 0;
  return (overlap / 60000) / coverage;
}

// series buckets rows by the range's granularity onto a continuous axis.
// `cutoff` is the time the rows' values were recorded (see
// bucketWindowShare) — pass the fetch time; past windows must NOT pass
// their own `until`. Boundary buckets are prorated to the window overlap so
// the chart shows only the data inside the window.)
export function series<T extends BucketedRow>(
  list: T[],
  valFn: (c: T) => number,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
  liveExtend = false,
): SeriesPoint[] {
  const axis = axisForRows(list, since, until, granularity);
  // An explicitly enabled live-cell extension may append the bucket starting
  // at the range's snapped until; the default axis stays half-open.
  const liveP = livePoint(until, since, cutoff, granularity, new Set(axis.map(p => p.label)), liveExtend);
  if (liveP) axis.push(liveP);
  const stepMin = STEP_MIN[granularity];
  if (granularity === 'hour') {
    const starts = hourAxisStarts(list, since, until, axis);
    const liveCell = hasLiveCell(until, since, cutoff, granularity, liveExtend);
    for (const r of list) {
      for (const [i, f] of hourlyAxisFractions(r, since, until, cutoff, axis, starts, liveCell)) {
        axis[i].value += valFn(r) * f;
      }
    }
    return axis;
  }
  if (stepMin != null) {
    // Sub-hour axis: the rows are hourly, so distribute each row's value
    // over the minute buckets overlapping its hour.
    const starts = bucketStarts(since, until, granularity);
    if (liveP) starts.push(until);
    const liveCell = hasLiveCell(until, since, cutoff, granularity, liveExtend);
    for (const r of list) {
      for (const [i, f] of overlapFractions(r.hour_bucket, since, until, cutoff, starts, stepMin, liveCell)) {
        axis[i].value += valFn(r) * f;
      }
    }
    return axis;
  }
  const keyOf = (hourBucket: string): string => {
    const t = dayjs(hourBucket);
    return granularity === 'day' ? t.format('YYYY-MM-DD') : t.format('YYYY-MM');
  };
  const idx = new Map(axis.map((p, i) => [p.sort, i]));
  for (const r of list) {
    const i = idx.get(keyOf(r.hour_bucket));
    if (i !== undefined) {
      axis[i].value += valFn(r) * bucketWindowShare(r.hour_bucket, since, until, cutoff, granularity, liveExtend);
    }
  }
  return axis;
}

// stackedData buckets rows per group onto a continuous axis; every group
// gets a column in each bucket row (zero-filled), like OR's stacked charts.
// Boundary buckets are prorated like series() so the stacks stay consistent
// with the KPI cards.
export function stackedData<T extends BucketedRow>(
  list: T[],
  groups: string[],
  keyFn: (c: T) => string,
  valFn: (c: T) => number,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
  liveExtend = false,
): Array<Record<string, any>> {
  const axis = axisForRows(list, since, until, granularity);
  // An explicitly enabled live-cell extension may append the bucket at until;
  // the default axis stays half-open (see series / livePoint).
  const liveP = livePoint(until, since, cutoff, granularity, new Set(axis.map(p => p.label)), liveExtend);
  if (liveP) axis.push(liveP);
  const rows = axis.map(p => {
    const row: Record<string, any> = { label: p.label, sort: p.sort };
    groups.forEach(g => { row[g] = 0; });
    return row;
  });
  const stepMin = STEP_MIN[granularity];
  if (granularity === 'hour') {
    const starts = hourAxisStarts(list, since, until, axis);
    const liveCell = hasLiveCell(until, since, cutoff, granularity, liveExtend);
    for (const r of list) {
      const g = keyFn(r);
      const v = valFn(r);
      for (const [i, f] of hourlyAxisFractions(r, since, until, cutoff, axis, starts, liveCell)) {
        rows[i][g] = (rows[i][g] ?? 0) + v * f;
      }
    }
    return rows;
  }
  if (stepMin != null) {
    // Sub-hour axis: distribute each hourly row over its overlapping
    // minute buckets (see series).
    const starts = bucketStarts(since, until, granularity);
    if (liveP) starts.push(until);
    const liveCell = hasLiveCell(until, since, cutoff, granularity, liveExtend);
    for (const r of list) {
      const g = keyFn(r);
      const v = valFn(r);
      for (const [i, f] of overlapFractions(r.hour_bucket, since, until, cutoff, starts, stepMin, liveCell)) {
        rows[i][g] = (rows[i][g] ?? 0) + v * f;
      }
    }
    return rows;
  }
  const keyOf = (hourBucket: string): string => {
    const t = dayjs(hourBucket);
    return granularity === 'day' ? t.format('YYYY-MM-DD') : t.format('YYYY-MM');
  };
  const idx = new Map(axis.map((p, i) => [p.sort, i]));
  for (const r of list) {
    const i = idx.get(keyOf(r.hour_bucket));
    if (i !== undefined) {
      const g = keyFn(r);
      // Rows whose group is outside `groups` still accumulate under their
      // own key — callers fold those keys into "Other". Guarding against
      // an uninitialized key keeps the fold from summing NaN into Other.
      rows[i][g] = (rows[i][g] ?? 0) + valFn(r) * bucketWindowShare(r.hour_bucket, since, until, cutoff, granularity, liveExtend);
    }
  }
  return rows;
}

interface SeriesCell {
  key: string;
  group: string;
  subgroup?: string;
}

const seriesCellKey = (group: string, subgroup?: string): string => `${group}\u0000${subgroup ?? ''}`;

// Activity responses use a dense bucket x group grid, so a zero-valued point
// can either be an actual consumption cell or a zero-fill for a missing cell.
// `has_data` distinguishes those cases on current API responses. Treat an
// omitted marker as present so hand-built fixtures and older responses keep
// their historical semantics.
function seriesPointHasData(point: ActivitySeriesPoint | undefined): boolean {
  return point !== undefined && point.has_data !== false;
}

function hasPresenceMetadata(series: ActivitySeriesPoint[]): boolean {
  return series.some(point => point.has_data !== undefined);
}

function addPresence<T extends ActivitySeriesPoint>(
  point: T,
  includePresence: boolean,
  hasData: boolean,
): T {
  return includePresence ? { ...point, has_data: hasData } : point;
}

function collectSeriesCells(series: ActivitySeriesPoint[]): SeriesCell[] {
  const cells = new Map<string, SeriesCell>();
  for (const p of series) {
    const key = seriesCellKey(p.group, p.subgroup);
    if (!cells.has(key)) {
      cells.set(key, { key, group: p.group, ...(p.subgroup ? { subgroup: p.subgroup } : {}) });
    }
  }
  return [...cells.values()];
}

// recomputeFromSeries rebuilds a response's summary and metric total from
// its bucket x group series grid, following the server's aggregation shape
// (see admin.go): min/max/avg over every represented data cell, including
// explicit zero-valued cells but excluding dense-grid zero-fills, value = the
// LAST bucket, percent = the group's share of the grid's total (top-N + Other;
// the server divides by every group's total).
// Shared by resampleResponse, aggregateHourlyResponse, and
// prorateBoundaryBuckets — all modify the
// series values and must re-derive the summed fields so every consumer
// (Trends' deltas, Explore's table) reads the PRORATED numbers, never the
// server's raw widened-window ones. Summary rows for groups OUTSIDE the
// series grid (beyond the server's top-N) cannot be re-derived bucket-by-
// bucket — the response only carries their totals — so their SUMS are
// moved to the corrected scale by the grid's aggregate window factor (see
// the scaling block below): without it the response's summary stays a
// HYBRID whose raw widened-window sums inflate Trends' deltas and the
// beyond-selection Explore rows.
function recomputeFromSeries(
  resp: ActivityResponse,
  series: ActivitySeriesPoint[],
  buckets: string[],
): { summary: ActivityGroupSummary[]; totals: ActivityResponse['totals'] } {
  const groups = Array.from(new Set(series.map(s => s.group)));
  const acc = new Map<string, Map<string, number>>();
  const present = new Map<string, Set<string>>();
  for (const p of series) {
    if (!seriesPointHasData(p)) continue;
    let base = acc.get(p.group);
    if (!base) { base = new Map(); acc.set(p.group, base); }
    base.set(p.bucket, (base.get(p.bucket) ?? 0) + p.value);
    let groupPresent = present.get(p.group);
    if (!groupPresent) { groupPresent = new Set(); present.set(p.group, groupPresent); }
    groupPresent.add(p.bucket);
  }
  const groupSum = new Map<string, number>();
  let totalSum = 0;
  for (const g of groups) {
    const s = buckets.reduce((a, b) => a + (acc.get(g)?.get(b) ?? 0), 0);
    groupSum.set(g, s);
    totalSum += s;
  }
  const summary: ActivityGroupSummary[] = groups.map(g => {
    const vals = buckets
      .filter(b => present.get(g)?.has(b))
      .map(b => acc.get(g)?.get(b) ?? 0);
    const sum = groupSum.get(g) ?? 0;
    return {
      group: g,
      min: vals.length ? Math.min(...vals) : 0,
      max: vals.length ? Math.max(...vals) : 0,
      avg: vals.length ? sum / vals.length : 0,
      sum,
      value: buckets.length ? acc.get(g)?.get(buckets[buckets.length - 1]) ?? 0 : 0,
      percent: totalSum > 0 ? (sum / totalSum) * 100 : 0,
    };
  });
  const gridGroups = new Set(groups);
  const beyond = resp.summary.filter(s => !gridGroups.has(s.group));
  if (beyond.length > 0) {
    // The retained fold-away rows carry the SERVER's raw widened-window
    // sums (full boundary buckets / full hourly rows — the response has no
    // per-bucket series for them, so they cannot be re-derived bucket-by-
    // bucket). Left as-is they would keep the response's summary a HYBRID:
    // the grid rows sit at the window scale while these stay raw, so
    // computeTrending pitted RAW current sums against the previous
    // period's FULLY re-sampled ones on sub-hour windows (pct =
    // (rawHourSum - prevResampledSum)/prev — inflated by the whole-hour
    // accumulation, able to FLIP the arrow for entities that actually
    // dropped). Both callers shrink every raw VALUE by its window share,
    // so the ratio of the re-derived grid total to the raw grid total is
    // the weighted-average share the grid applied — under the same
    // uniform-within-bucket assumption the resample itself uses, the
    // fold-away groups' sums shrink by that factor too. The basis is the
    // "Other" fold when the response carries it: Other IS the beyond-grid
    // groups' aggregate, so its raw/scaled pair is exactly their share;
    // the whole grid is the fallback (only reachable on hand-built
    // responses — a real top-N response always carries the fold when
    // beyond rows exist). The per-bucket statistics (min/max/avg) and the
    // last-bucket value stay as the server reported them — an aggregate
    // factor cannot faithfully re-derive bucket-level numbers — and
    // percent stays too: it is a share whose numerator and denominator
    // shrink by the same factor, so the server's value already holds at
    // the new scale.
    const rawOther = resp.series.reduce((a, p) => (p.group === 'Other' ? a + p.value : a), 0);
    let rawBasis = rawOther;
    let scaledBasis = series.reduce((a, p) => (p.group === 'Other' ? a + p.value : a), 0);
    if (rawOther <= 0) {
      // When the raw response carries cells for the groups that are now
      // folded, use every raw cell as the basis. A top=0 response takes this
      // path in limitActivityResponse; restricting the basis to the retained
      // groups would make the newly-created Other value look artificially
      // larger than its raw tail. If the raw response has no beyond-grid
      // cells, retain the legacy grid-only basis for hand-built top-N data.
      const rawGroups = new Set(resp.series.map(p => p.group));
      const hasRawBeyond = [...rawGroups].some(g => !gridGroups.has(g));
      rawBasis = resp.series.reduce((a, p) => hasRawBeyond || gridGroups.has(p.group) ? a + p.value : a, 0);
      scaledBasis = series.reduce((a, p) => (gridGroups.has(p.group) ? a + p.value : a), 0);
    }
    if (rawBasis > 0 && scaledBasis !== rawBasis) {
      const scale = scaledBasis / rawBasis;
      for (let i = 0; i < beyond.length; i++) {
        const s = beyond[i];
        beyond[i] = { ...s, sum: s.sum * scale };
      }
    }
  }
  summary.push(...beyond);
  const totals = { ...resp.totals };
  const metricTotal = series.reduce((a, p) => a + p.value, 0);
  if (resp.metric === 'spend') totals.spend = metricTotal;
  else if (resp.metric === 'tokens') totals.tokens = metricTotal;
  else if (resp.metric === 'requests') totals.requests = metricTotal;
  else if (resp.metric === 'cache') totals.cache = metricTotal;
  return { summary, totals };
}

function blendedRate(spend: number, tokens: number): number {
  return tokens > 0 ? (spend / tokens) * 1e6 : 0;
}

// Blended cells are rates. A resampled rate can keep the source hourly rate
// for every overlapping output cell, but the rate-only response does not carry
// the spend/token weights needed to derive a new primary-group total rate.
// Preserve that aggregate rate and percent from the server while recomputing
// the min/max/avg of the visible non-subgroup rate cells. The main blended
// Explore path combines normalized spend and token responses instead, so it
// can derive every rate exactly after boundary trimming.
function recomputeBlendedFromSeries(
  resp: ActivityResponse,
  series: ActivitySeriesPoint[],
  buckets: string[],
): { summary: ActivityGroupSummary[]; totals: ActivityResponse['totals'] } {
  const groups = Array.from(new Set(series.map(s => s.group)));
  const hasSubgroups = series.some(s => Boolean(s.subgroup));
  const original = new Map(resp.summary.map(s => [s.group, s]));
  const values = new Map<string, number>();
  const present = new Map<string, Set<string>>();
  for (const point of series) {
    if (!seriesPointHasData(point)) continue;
    const key = `${point.group}\u0001${point.bucket}`;
    values.set(key, (values.get(key) ?? 0) + point.value);
    let groupPresent = present.get(point.group);
    if (!groupPresent) { groupPresent = new Set(); present.set(point.group, groupPresent); }
    groupPresent.add(point.bucket);
  }
  const summary: ActivityGroupSummary[] = groups.map(group => {
    const source = original.get(group);
    if (hasSubgroups) {
      // A primary group's overall rate cannot be reconstructed from subgroup
      // rates without their token weights. The source summary already has the
      // correct aggregate values for the same server response.
      return source ? { ...source } : {
        group, min: 0, max: 0, avg: 0, sum: 0, value: 0, percent: 0,
      };
    }
    const activeBuckets = buckets.filter(bucket => present.get(group)?.has(bucket));
    const rates = activeBuckets.map(bucket => values.get(`${group}\u0001${bucket}`) ?? 0);
    return {
      group,
      min: rates.length ? Math.min(...rates) : 0,
      max: rates.length ? Math.max(...rates) : 0,
      avg: rates.length ? rates.reduce((a, v) => a + v, 0) / rates.length : 0,
      sum: source?.sum ?? 0,
      value: buckets.length ? values.get(`${group}\u0001${buckets[buckets.length - 1]}`) ?? 0 : source?.value ?? 0,
      percent: source?.percent ?? 0,
    };
  });
  summary.push(...resp.summary.filter(s => !groups.includes(s.group)));
  return { summary, totals: { ...resp.totals } };
}

// resampleResponse re-samples an HOURLY-rolled ActivityResponse onto a
// sub-hour client axis (the Trends/Explore API rolls up at most hourly —
// see activityWindow in admin.go). Every hourly series point is distributed
// over the minute buckets overlapping the window exactly like series() does
// for raw rows (cutoff = the fetch time, see overlapFractions); every group
// stays zero-filled per bucket (the server emits full bucket x group grids
// with is_zero flags). Summary and totals are RECOMPUTED from the resampled
// buckets (server semantics: min/max/avg over represented data cells,
// including explicit zero-valued cells but excluding zero-fills, value = last
// bucket, percent of the total) so
// the Trends "Trending" deltas agree
// with the re-bucketed chart; summary rows beyond the series grid (groups
// ranked 6+ under top-5) are moved to the same scale by the grid's
// aggregate factor inside recomputeFromSeries — without that, their raw
// widened-window sums inflated the deltas against the previous period's
// fully re-sampled ones. `sourcePrecise` is used when the server has already
// applied the hourly window share: visible overlap fractions are then
// normalized by their sum instead of applying the boundary share again.
export function resampleResponse(
  resp: ActivityResponse,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: 'minute' | 'min15',
  liveExtend = false,
  sourcePrecise = false,
): ActivityResponse {
  const stepMin = STEP_MIN[granularity]!;
  const starts = bucketStarts(since, until, granularity);
  const buckets = bucketAxis(since, until, granularity).map(p => p.label);
  // An explicitly enabled live-cell extension may append the current min15
  // cell; the default response keeps only buckets before the displayed until.
  const liveP = livePoint(until, since, cutoff, granularity, new Set(buckets), liveExtend);
  if (liveP) {
    starts.push(until);
    buckets.push(liveP.label);
  }
  const cells = collectSeriesCells(resp.series);
  const includePresence = hasPresenceMetadata(resp.series);
  // (group, subgroup) -> bucket label -> value
  const acc = new Map<string, Map<string, number>>();
  const present = new Map<string, Set<string>>();
  const liveCell = hasLiveCell(until, since, cutoff, granularity, liveExtend);
  for (const p of resp.series) {
    if (!seriesPointHasData(p)) continue;
    const key = seriesCellKey(p.group, p.subgroup);
    let base = acc.get(key);
    if (!base) { base = new Map(); acc.set(key, base); }
    const windowShare = sourcePrecise
      ? bucketWindowShare(p.bucket, since, until, cutoff, granularity, liveExtend)
      : 1;
    for (const [i, f] of overlapFractions(p.bucket, since, until, cutoff, starts, stepMin, liveCell)) {
      const b = buckets[i];
      let cellPresent = present.get(key);
      if (!cellPresent) { cellPresent = new Set(); present.set(key, cellPresent); }
      cellPresent.add(b);
      if (resp.metric === 'blended') {
        // A blended value is a rate, not an hourly amount. The hourly rate is
        // the value for every overlapping sub-hour cell; multiplying it by a
        // time fraction and summing would turn a constant rate into a larger
        // rate as the number of cells grows.
        base.set(b, base.get(b) ?? p.value);
      } else {
        const factor = sourcePrecise ? (windowShare > 0 ? f / windowShare : 0) : f;
        base.set(b, (base.get(b) ?? 0) + p.value * factor);
      }
    }
  }
  const series: ActivitySeriesPoint[] = [];
  for (const b of buckets) {
    for (const cell of cells) {
      const v = acc.get(cell.key)?.get(b) ?? 0;
      series.push(addPresence({
        bucket: b,
        group: cell.group,
        ...(cell.subgroup ? { subgroup: cell.subgroup } : {}),
        value: v,
        is_zero: v === 0,
      }, includePresence, present.get(cell.key)?.has(b) ?? false));
    }
  }
  const { summary, totals } = resp.metric === 'blended'
    ? recomputeBlendedFromSeries(resp, series, buckets)
    : recomputeFromSeries(resp, series, buckets);
  return { ...resp, rollup: granularity, buckets, series, summary, totals };
}

export type ActivityRollup = 'hour' | 'day' | 'week' | 'month' | 'total';
export type ActivityOutputRollup = ActivityRollup | 'minute' | 'min15';

function rollupBucketLabel(bucket: string, rollup: ActivityRollup): string {
  if (rollup === 'total') return 'Total';
  if (rollup === 'hour') return hourSortFromBucket(bucket) ?? dayjs(bucket).format('YYYY-MM-DD HH:00');
  const t = dayjs(bucket);
  if (rollup === 'week') return mondayOf(t).format('YYYY-MM-DD');
  return rollup === 'day' ? t.format('YYYY-MM-DD') : t.format('YYYY-MM');
}

// A long-range chart should not force the client to materialize every stored
// hourly cell. Coarse output is bounded by the requested rollup; only the
// target buckets whose edges cut the requested window need hourly rows for
// exact boundary correction.
const MAX_FULL_HOURLY_SOURCE_HOURS = 7 * 24;

function rollupForGranularity(granularity: Granularity): ActivityRollup {
  if (granularity === 'month') return 'month';
  return 'day';
}

function rollupBucketStart(time: dayjs.Dayjs, rollup: ActivityRollup): dayjs.Dayjs {
  if (rollup === 'hour') return floorWindowUntil(time, 'hour');
  if (rollup === 'day') return time.startOf('day');
  if (rollup === 'week') return mondayOf(time);
  return time.startOf('month');
}

function rollupBucketEnd(start: dayjs.Dayjs, rollup: ActivityRollup): dayjs.Dayjs {
  if (rollup === 'hour') return start.add(1, 'hour');
  if (rollup === 'day') return start.add(1, 'day');
  if (rollup === 'week') return start.add(7, 'day');
  return start.add(1, 'month');
}

export interface ActivitySourcePlan {
  sourceRollup: ActivityRollup;
  // When set, this small hourly query covers only the source buckets whose
  // coarse values may include data outside the requested window.
  boundary?: Array<{ bucket: string; since: dayjs.Dayjs; until: dayjs.Dayjs }>;
}

// activitySourcePlan chooses a bounded source response for an activity view.
// Hourly data remains the source for short/fine views, where the output itself
// is small. For long day/week/month/total views, the selected coarse rollup is
// fetched across the range and an optional hourly boundary query corrects the
// first/last coarse buckets before the caller renders or collapses Total.
export function activitySourcePlan(
  range: Pick<DateRange, 'since' | 'until' | 'granularity'>,
  outputRollup: ActivityOutputRollup,
  liveExtend: boolean,
): ActivitySourcePlan {
  const hours = range.until.diff(range.since, 'hour', true);
  const fullHourly = hours <= MAX_FULL_HOURLY_SOURCE_HOURS;
  if (fullHourly || outputRollup === 'hour' || outputRollup === 'minute' || outputRollup === 'min15') {
    return { sourceRollup: 'hour' };
  }

  // Total needs a rollup that still has multiple buckets so interior values
  // are preserved; aggregateTotalResponse collapses the corrected result only
  // after the boundary merge.
  const sourceRollup = outputRollup === 'total'
    ? rollupForGranularity(range.granularity)
    : outputRollup;
  const boundaryStarts: dayjs.Dayjs[] = [];
  const addBoundary = (start: dayjs.Dayjs) => {
    if (!boundaryStarts.some(existing => existing.valueOf() === start.valueOf())) {
      boundaryStarts.push(start);
    }
  };
  const sinceStart = rollupBucketStart(range.since, sourceRollup);
  if (!range.since.isSame(sinceStart)) addBoundary(sinceStart);

  const untilStart = rollupBucketStart(range.until, sourceRollup);
  // An aligned live boundary is intentional: the coarse query contains the
  // current live unit only up to the fetch cutoff, so there is no outside
  // value to remove. An unaligned end always needs the containing bucket.
  if (!range.until.isSame(untilStart)) addBoundary(untilStart);

  if (boundaryStarts.length === 0) return { sourceRollup };
  boundaryStarts.sort((a, b) => a.valueOf() - b.valueOf());
  // Keep the first and last coarse buckets as separate requests. Combining
  // them into one hourly range would turn a one-year custom month query back
  // into roughly 8,760 hourly cells and recreate the regression this plan is
  // meant to avoid.
  return {
    sourceRollup,
    boundary: boundaryStarts.map(start => ({
      bucket: rollupBucketLabel(start.format('YYYY-MM-DD HH:mm:ss'), sourceRollup),
      since: start,
      // The activity endpoint widens its upper bound to the containing hour.
      // Stop one second before the coarse boundary so the hourly query
      // contains the final needed hour without adding the next one.
      until: rollupBucketEnd(start, sourceRollup).subtract(1, 'second'),
    })),
  };
}

// activitySourceQueryUntil keeps a current-period source query inside the
// live range unit. This matters when a request crosses a clock rollover after
// the range was rendered: a stale range must not fetch the newly-started day,
// month, or other source bucket just because the request resolves later.
export function activitySourceQueryUntil(
  range: Pick<DateRange, 'key' | 'since' | 'until' | 'granularity'>,
  sourceRollup: ActivityRollup,
  cutoff: dayjs.Dayjs,
  liveExtend: boolean,
): dayjs.Dayjs {
  if (!liveExtend) return queryWindowUntil(range, sourceRollup);
  const unitEnd = range.until.add(
    range.granularity === 'month' ? 1 : range.granularity === 'day' ? 1 : range.granularity === 'hour' ? 1 : range.granularity === 'min15' ? 15 : 1,
    range.granularity === 'month' ? 'month' : range.granularity === 'day' ? 'day' : range.granularity === 'hour' ? 'hour' : 'minute',
  ).subtract(1, 'second');
  const now = floorWindowUntil(cutoff, 'minute');
  return now.isBefore(unitEnd) ? now : unitEnd;
}

function rebucketHourlyResponse(
  resp: ActivityResponse,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  liveExtend: boolean,
  sourcePrecise: boolean,
): ActivityResponse {
  const rows = resp.series.map(point => ({ hour_bucket: point.bucket }));
  const axis = axisForRows(rows, since, until, 'hour');
  const liveP = livePoint(until, since, cutoff, 'hour', new Set(axis.map(point => point.label)), liveExtend);
  if (liveP) axis.push(liveP);
  const starts = hourAxisStarts(rows, since, until, axis);
  const cells = collectSeriesCells(resp.series);
  const includePresence = hasPresenceMetadata(resp.series);
  const acc = new Map<string, Map<string, number>>();
  const present = new Map<string, Set<string>>();

  for (const point of resp.series) {
    if (!seriesPointHasData(point)) continue;
    const sourceRow = { hour_bucket: point.bucket };
    const windowShare = sourcePrecise
      ? bucketWindowShare(point.bucket, since, until, cutoff, 'hour', liveExtend)
      : 1;
    if (sourcePrecise && windowShare <= 0) continue;
    const factorFor = (fraction: number) => sourcePrecise ? fraction / windowShare : fraction;
    const key = seriesCellKey(point.group, point.subgroup);
    let base = acc.get(key);
    if (!base) { base = new Map(); acc.set(key, base); }
    for (const [index, fraction] of hourlyAxisFractions(
      sourceRow, since, until, cutoff, axis, starts,
      hasLiveCell(until, since, cutoff, 'hour', liveExtend),
    )) {
      const bucket = axis[index].sort;
      base.set(bucket, (base.get(bucket) ?? 0) + point.value * factorFor(fraction));
      let cellPresent = present.get(key);
      if (!cellPresent) { cellPresent = new Set(); present.set(key, cellPresent); }
      cellPresent.add(bucket);
    }
  }

  // Activity API responses use year-qualified hourly bucket labels. Keep that
  // shape here even though the chart-axis helper also carries short labels.
  const buckets = axis.map(point => point.sort);
  const series: ActivitySeriesPoint[] = [];
  for (const bucket of buckets) {
    for (const cell of cells) {
      const value = acc.get(cell.key)?.get(bucket) ?? 0;
      series.push(addPresence({
        bucket,
        group: cell.group,
        ...(cell.subgroup ? { subgroup: cell.subgroup } : {}),
        value,
        is_zero: value === 0,
      }, includePresence, present.get(cell.key)?.has(bucket) ?? false));
    }
  }
  const normalized: ActivityResponse = { ...resp, rollup: 'hour', buckets, series };
  const { summary, totals } = recomputeFromSeries(normalized, series, buckets);
  return { ...normalized, summary, totals };
}

// aggregateHourlyResponse converts an hourly response into the selected
// calendar rollup after applying the exact hourly-window share to each row.
// The server's day/week/month/total aggregates have already lost the hourly
// distribution, so their single elapsed-time ratio cannot be correct for
// non-uniform traffic. This helper keeps every hourly cell until after the
// boundary trim, then recomputes the selected metric's summaries and totals.
// When `sourcePrecise` is true, the server has already applied that share and
// this function only re-buckets the corrected values.
// It is intentionally additive-only; blended responses are built from the
// separately normalized spend and token responses below.
export function aggregateHourlyResponse(
  resp: ActivityResponse,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  rangeGranularity: Granularity,
  rollup: ActivityRollup,
  liveExtend = false,
  sourcePrecise = false,
): ActivityResponse {
  if (resp.metric === 'blended' || resp.rollup !== 'hour') return resp;
  if (rollup === 'hour') {
    return rebucketHourlyResponse(resp, since, until, cutoff, liveExtend, sourcePrecise);
  }

  const sourceBuckets = [...resp.buckets];
  for (const p of resp.series) {
    if (!sourceBuckets.includes(p.bucket)) sourceBuckets.push(p.bucket);
  }
  const includedSourceBuckets = sourceBuckets.filter(bucket =>
    sourcePrecise || rollup === 'total' || bucketWindowShare(
      bucket, since, until, cutoff, rangeGranularity, liveExtend,
    ) > 0,
  );
  const buckets = rollup === 'total'
    ? ['Total']
    : [...new Set(includedSourceBuckets.map(b => rollupBucketLabel(b, rollup)))];
  const cells = collectSeriesCells(resp.series);
  const includePresence = hasPresenceMetadata(resp.series);
  const acc = new Map<string, Map<string, number>>();
  const present = new Map<string, Set<string>>();
  for (const p of resp.series) {
    if (!seriesPointHasData(p)) continue;
    const share = sourcePrecise
      ? 1
      : bucketWindowShare(p.bucket, since, until, cutoff, rangeGranularity, liveExtend);
    if (share <= 0) continue;
    const bucket = rollupBucketLabel(p.bucket, rollup);
    const key = seriesCellKey(p.group, p.subgroup);
    let base = acc.get(key);
    if (!base) { base = new Map(); acc.set(key, base); }
    base.set(bucket, (base.get(bucket) ?? 0) + p.value * share);
    let cellPresent = present.get(key);
    if (!cellPresent) { cellPresent = new Set(); present.set(key, cellPresent); }
    cellPresent.add(bucket);
  }
  const series: ActivitySeriesPoint[] = [];
  for (const bucket of buckets) {
    for (const cell of cells) {
      const value = acc.get(cell.key)?.get(bucket) ?? 0;
      series.push(addPresence({
        bucket,
        group: cell.group,
        ...(cell.subgroup ? { subgroup: cell.subgroup } : {}),
        value,
        is_zero: value === 0,
      }, includePresence, present.get(cell.key)?.has(bucket) ?? false));
    }
  }
  const { summary, totals } = recomputeFromSeries(resp, series, buckets);
  return { ...resp, rollup, buckets, series, summary, totals };
}

// normalizeHourlyResponse is the single client-side boundary-normalization
// entry point. The activity endpoint widens every query to complete buckets,
// so callers must retain hourly cells until after the requested window has
// been applied. Sub-hour charts request an explicit minute/min15 output
// rollup; all other rollups aggregate corrected hourly cells directly. The
// range granularity alone must not override an Explore rollup selection. A
// precise server response has already applied the row share; it still needs
// client re-bucketing for short-range output, but must never be prorated a
// second time.
export function normalizeHourlyResponse(
  resp: ActivityResponse,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  rangeGranularity: Granularity,
  rollup: ActivityOutputRollup,
  liveExtend = false,
  sourcePrecise = false,
): ActivityResponse {
  if (rollup === 'minute' || rollup === 'min15') {
    return resampleResponse(resp, since, until, cutoff, rollup, liveExtend, sourcePrecise);
  }
  return aggregateHourlyResponse(resp, since, until, cutoff, rangeGranularity, rollup, liveExtend, sourcePrecise);
}

function seriesPointMap(resp: ActivityResponse): Map<string, ActivitySeriesPoint> {
  const points = new Map<string, ActivitySeriesPoint>();
  for (const point of resp.series) {
    points.set(`${point.bucket}\u0001${seriesCellKey(point.group, point.subgroup)}`, point);
  }
  return points;
}

// combineBlendedResponses derives rates from normalized spend and token cells.
// Keeping the two additive metrics until after hourly boundary correction is
// the only way to make blended rates correct for partial hours and calendar
// buckets with non-uniform usage. Subgroups remain independent series cells,
// while the summary rate is derived from the primary group's combined totals.
export function combineBlendedResponses(
  spend: ActivityResponse,
  tokens: ActivityResponse,
): ActivityResponse {
  const buckets = [...spend.buckets];
  for (const bucket of tokens.buckets) {
    if (!buckets.includes(bucket)) buckets.push(bucket);
  }
  const cells = collectSeriesCells([...spend.series, ...tokens.series]);
  const includePresence = hasPresenceMetadata(spend.series) || hasPresenceMetadata(tokens.series);
  const spendPoints = seriesPointMap(spend);
  const tokenPoints = seriesPointMap(tokens);
  const series: ActivitySeriesPoint[] = [];
  for (const bucket of buckets) {
    for (const cell of cells) {
      const key = `${bucket}\u0001${cell.key}`;
      const spendPoint = spendPoints.get(key);
      const tokenPoint = tokenPoints.get(key);
      const spendValue = seriesPointHasData(spendPoint) ? spendPoint!.value : 0;
      const tokenValue = seriesPointHasData(tokenPoint) ? tokenPoint!.value : 0;
      const value = tokenValue > 0 ? (spendValue / tokenValue) * 1e6 : 0;
      series.push(addPresence({
        bucket,
        group: cell.group,
        ...(cell.subgroup ? { subgroup: cell.subgroup } : {}),
        value,
        is_zero: value === 0,
      }, includePresence, seriesPointHasData(spendPoint) || seriesPointHasData(tokenPoint)));
    }
  }

  const groups = Array.from(new Set(cells.map(c => c.group)));
  const groupSpend = new Map<string, number>();
  const groupTokens = new Map<string, number>();
  const groupPresent = new Map<string, Set<string>>();
  const bucketRates = new Map<string, Map<string, number>>();
  for (const group of groups) {
    const rates = new Map<string, number>();
    for (const bucket of buckets) {
      let spendValue = 0;
      let tokenValue = 0;
      let hasData = false;
      for (const cell of cells) {
        if (cell.group !== group) continue;
        const key = `${bucket}\u0001${cell.key}`;
        const spendPoint = spendPoints.get(key);
        const tokenPoint = tokenPoints.get(key);
        if (seriesPointHasData(spendPoint)) spendValue += spendPoint!.value;
        if (seriesPointHasData(tokenPoint)) tokenValue += tokenPoint!.value;
        hasData = hasData || seriesPointHasData(spendPoint) || seriesPointHasData(tokenPoint);
      }
      groupSpend.set(group, (groupSpend.get(group) ?? 0) + spendValue);
      groupTokens.set(group, (groupTokens.get(group) ?? 0) + tokenValue);
      if (hasData) {
        let groupBuckets = groupPresent.get(group);
        if (!groupBuckets) { groupBuckets = new Set(); groupPresent.set(group, groupBuckets); }
        groupBuckets.add(bucket);
        rates.set(bucket, tokenValue > 0 ? (spendValue / tokenValue) * 1e6 : 0);
      }
    }
    bucketRates.set(group, rates);
  }
  const totalSpend = [...groupSpend.values()].reduce((a, v) => a + v, 0);
  const summary: ActivityGroupSummary[] = groups.map(group => {
    const ratesByBucket = bucketRates.get(group) ?? new Map<string, number>();
    const rates = buckets
      .filter(bucket => groupPresent.get(group)?.has(bucket))
      .map(bucket => ratesByBucket.get(bucket) ?? 0);
    const sum = blendedRate(groupSpend.get(group) ?? 0, groupTokens.get(group) ?? 0);
    return {
      group,
      min: rates.length ? Math.min(...rates) : 0,
      max: rates.length ? Math.max(...rates) : 0,
      avg: rates.length ? rates.reduce((a, v) => a + v, 0) / rates.length : 0,
      sum,
      value: buckets.length ? ratesByBucket.get(buckets[buckets.length - 1]) ?? 0 : 0,
      percent: totalSpend > 0 ? ((groupSpend.get(group) ?? 0) / totalSpend) * 100 : 0,
    };
  });
  const totals = {
    ...spend.totals,
    spend: spend.totals.spend,
    tokens: tokens.totals.tokens,
  };
  return {
    ...spend,
    metric: 'blended',
    rollup: spend.rollup,
    buckets,
    series,
    summary,
    totals,
  };
}

// limitActivityResponse applies Explore/Trends' Top-N fold after the hourly
// response has been normalized. The server cannot choose this set correctly
// from widened boundary rows: a group that is large just outside the window
// can otherwise displace a genuinely top in-window group. `rankResponse`
// carries the normalized values for the requested rank metric; when omitted,
// the chart metric's normalized summary is used. The folded series is kept
// small for the chart, but normalized summaries for the tail are retained so
// Trends can still calculate deltas for groups represented by Other. Both
// the retained series cells and the summary rows are emitted in this
// normalized rank order, even when every group fits within Top-N.
export function limitActivityResponse(
  resp: ActivityResponse,
  topN: number,
  rankResponse: ActivityResponse = resp,
  blendedSources?: { spend: ActivityResponse; tokens: ActivityResponse },
): ActivityResponse {
  if (topN <= 0) return resp;

  if (resp.metric === 'blended' && blendedSources) {
    // Rates cannot be folded by adding rate cells. Fold the normalized spend
    // and token matrices first, then derive the top groups' and Other's rates
    // from their combined values.
    const spend = limitActivityResponse(blendedSources.spend, topN, rankResponse);
    const tokens = limitActivityResponse(blendedSources.tokens, topN, rankResponse);
    return combineBlendedResponses(spend, tokens);
  }

  const groups = Array.from(new Set([
    ...resp.summary.map(s => s.group),
    ...resp.series.map(s => s.group),
  ])).filter(g => g !== 'Other');

  const rankTotals = new Map(rankResponse.summary.map(s => [s.group, s.sum]));
  const ordered = [...groups].sort((a, b) => {
    const av = rankTotals.get(a) ?? 0;
    const bv = rankTotals.get(b) ?? 0;
    if (av !== bv) return bv - av;
    return a.localeCompare(b);
  });
  const topGroups = ordered.slice(0, topN);
  const topSet = new Set(topGroups);
  const tailSet = new Set(ordered.slice(topN));
  const rankIndex = new Map(topGroups.map((group, i) => [group, i]));
  const cells = collectSeriesCells(resp.series)
    .filter(c => topSet.has(c.group))
    .sort((a, b) => (rankIndex.get(a.group)! - rankIndex.get(b.group)!));
  const includePresence = hasPresenceMetadata(resp.series);
  const points = seriesPointMap(resp);
  const buckets = [...resp.buckets];
  const series: ActivitySeriesPoint[] = [];
  for (const bucket of buckets) {
    for (const cell of cells) {
      const point = points.get(`${bucket}\u0001${cell.key}`);
      const value = seriesPointHasData(point) ? point!.value : 0;
      series.push(addPresence({
        bucket,
        group: cell.group,
        ...(cell.subgroup ? { subgroup: cell.subgroup } : {}),
        value,
        is_zero: value === 0,
      }, includePresence, seriesPointHasData(point)));
    }
    if (tailSet.size > 0) {
      let value = 0;
      let hasData = false;
      for (const p of resp.series) {
        if (p.bucket === bucket && tailSet.has(p.group) && seriesPointHasData(p)) {
          value += p.value;
          hasData = true;
        }
      }
      series.push(addPresence(
        { bucket, group: 'Other', value, is_zero: value === 0 },
        includePresence,
        hasData,
      ));
    }
  }
  const folded: ActivityResponse = { ...resp, series };
  const { summary, totals } = resp.metric === 'blended'
    ? recomputeBlendedFromSeries(resp, series, buckets)
    : recomputeFromSeries(resp, series, buckets);
  const summaryByGroup = new Map(summary.map(s => [s.group, s]));
  const tailSummaryByGroup = new Map(resp.summary
    .filter(s => !topSet.has(s.group) && s.group !== 'Other')
    .map(s => [s.group, s]));
  const summaryOrder = [...topGroups, ...(tailSet.size > 0 ? ['Other'] : []), ...ordered.slice(topN)];
  const orderedSummary = summaryOrder
    .map(group => summaryByGroup.get(group) ?? tailSummaryByGroup.get(group))
    .filter((s): s is ActivityGroupSummary => s !== undefined);
  return { ...folded, summary: orderedSummary, totals };
}

// prorateBoundaryBuckets is retained for old callers that already request
// hourly data. It deliberately does not touch day/week/month/total responses:
// those payloads have discarded the hourly distribution, so a single
// aggregate share cannot be correct for non-uniform usage. New callers should
// use normalizeHourlyResponse and choose the final rollup after correction.
export function prorateBoundaryBuckets(
  resp: ActivityResponse,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  untilSent: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
  rollup: string,
  liveExtend = false,
): ActivityResponse {
  // Coarser responses have already discarded the hourly distribution. A
  // single elapsed-time share cannot correct them for non-uniform usage, so
  // callers must fetch hourly cells and use normalizeHourlyResponse instead.
  // Keep this legacy helper hourly-only for callers that still import it.
  if (granularity === 'minute' || granularity === 'min15') return resp;
  if (resp.metric === 'blended') return resp;
  if (resp.buckets.length === 0) return resp;
  if (resp.rollup !== 'hour' || rollup !== 'hour') return resp;

  const series = resp.series.map(point => {
    const share = bucketWindowShare(
      point.bucket, since, until, cutoff, granularity, liveExtend,
    );
    const value = point.value * share;
    return share === 1 ? point : { ...point, value, is_zero: value === 0 };
  });
  const { summary, totals } = recomputeFromSeries(resp, series, resp.buckets);
  const unchanged = series.every((point, i) => {
    const original = resp.series[i];
    return original
      && point.bucket === original.bucket
      && point.group === original.group
      && point.subgroup === original.subgroup
      && point.value === original.value
      && point.is_zero === original.is_zero;
  });
  return unchanged ? resp : { ...resp, series, summary, totals };
}

// aggregateTotalResponse collapses a corrected hourly response into the
// single bucket used by Explore's Total rollup. Additive metrics must take
// this path: the server's total response has already discarded the hourly
// cells, so scaling that one aggregate would also scale interior hours. The
// caller corrects the hourly boundary cells first, then this helper sums the
// remaining values and recomputes the one-bucket summary and metric total.
// Subgroup points are kept as separate series cells; summaries remain at the
// primary-group level, matching the activity endpoint's response shape.
export function aggregateTotalResponse(resp: ActivityResponse): ActivityResponse {
  if (resp.metric === 'blended' || resp.rollup === 'total') return resp;

  const includePresence = hasPresenceMetadata(resp.series);
  const byCell = new Map<string, ActivitySeriesPoint>();
  for (const p of resp.series) {
    const key = `${p.group}\u0000${p.subgroup ?? ''}`;
    const current = byCell.get(key);
    if (current) {
      if (seriesPointHasData(p)) current.value += p.value;
      current.is_zero = current.value === 0;
      if (includePresence) current.has_data = seriesPointHasData(current) || seriesPointHasData(p);
    } else {
      byCell.set(key, addPresence({
        ...p,
        bucket: 'Total',
        value: seriesPointHasData(p) ? p.value : 0,
        is_zero: p.value === 0,
      }, includePresence, seriesPointHasData(p)));
    }
  }
  const series = [...byCell.values()];
  const totalResp: ActivityResponse = {
    ...resp,
    rollup: 'total',
    buckets: ['Total'],
    series,
  };
  const { summary, totals } = recomputeFromSeries(totalResp, series, totalResp.buckets);
  return { ...totalResp, summary, totals };
}

// mergeActivityBoundaryResponses overlays corrected hourly boundary buckets
// onto a coarse response. The coarse response supplies all interior buckets;
// the hourly responses supply only the first/last buckets whose aggregate
// values include rows outside the requested range. Rebuilding a dense grid
// here keeps explicit zero cells and has_data presence intact for summaries.
export function mergeActivityBoundaryResponses(
  coarse: ActivityResponse,
  boundaryResponses: ActivityResponse[],
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  rangeGranularity: Granularity,
  sourceRollup: ActivityRollup,
  liveExtend = false,
): ActivityResponse {
  if (coarse.metric === 'blended' || boundaryResponses.length === 0) return coarse;

  const corrected = boundaryResponses.map(resp => aggregateHourlyResponse(
    resp, since, until, cutoff, rangeGranularity, sourceRollup, liveExtend,
  ));
  const boundaryBuckets = new Set(corrected.flatMap(resp => resp.buckets));
  if (boundaryBuckets.size === 0) return coarse;

  const buckets = [...coarse.buckets];
  for (const bucket of boundaryBuckets) {
    if (buckets.includes(bucket)) continue;
    const index = buckets.findIndex(existing => existing.localeCompare(bucket) > 0);
    if (index < 0) buckets.push(bucket);
    else buckets.splice(index, 0, bucket);
  }

  const cells = collectSeriesCells([
    ...coarse.series,
    ...corrected.flatMap(resp => resp.series),
  ]);
  const coarsePoints = seriesPointMap(coarse);
  const boundaryPoints = new Map<string, ActivitySeriesPoint>();
  for (const resp of corrected) {
    for (const point of resp.series) {
      boundaryPoints.set(`${point.bucket}\u0001${seriesCellKey(point.group, point.subgroup)}`, point);
    }
  }
  const includePresence = hasPresenceMetadata(coarse.series)
    || corrected.some(resp => hasPresenceMetadata(resp.series));
  const series: ActivitySeriesPoint[] = [];
  for (const bucket of buckets) {
    const points = boundaryBuckets.has(bucket) ? boundaryPoints : coarsePoints;
    for (const cell of cells) {
      const point = points.get(`${bucket}\u0001${cell.key}`);
      const hasData = seriesPointHasData(point);
      const value = hasData ? point!.value : 0;
      series.push(addPresence({
        bucket,
        group: cell.group,
        ...(cell.subgroup ? { subgroup: cell.subgroup } : {}),
        value,
        is_zero: value === 0,
      }, includePresence, hasData));
    }
  }
  const merged: ActivityResponse = {
    ...coarse,
    rollup: sourceRollup,
    buckets,
    series,
  };
  const { summary, totals } = recomputeFromSeries(merged, series, buckets);
  return { ...merged, summary, totals };
}

// normalizeActivitySourceResponse handles both bounded coarse sources and
// the existing short-range hourly source. Long ranges keep their selected
// source rollup for interior buckets, overlay corrected boundary responses,
// and collapse to Total only after that merge.
export function normalizeActivitySourceResponse(
  resp: ActivityResponse,
  boundaryResponses: ActivityResponse[],
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  rangeGranularity: Granularity,
  sourceRollup: ActivityRollup,
  outputRollup: ActivityOutputRollup,
  liveExtend = false,
  sourcePrecise = false,
): ActivityResponse {
  if (resp.rollup === 'hour') {
    return normalizeHourlyResponse(
      resp, since, until, cutoff, rangeGranularity, outputRollup, liveExtend, sourcePrecise,
    );
  }
  const merged = mergeActivityBoundaryResponses(
    resp, boundaryResponses, since, until, cutoff,
    rangeGranularity, sourceRollup, liveExtend,
  );
  return outputRollup === 'total' ? aggregateTotalResponse(merged) : merged;
}

// groupTotals sums a metric per group, sorted descending. An optional
// factorFn prorates each row (e.g. bucketWindowShare on a rolling window)
// so ranked lists agree with the prorated charts.
export function groupTotals<T extends BucketedRow>(
  list: T[],
  keyFn: (c: T) => string,
  valFn: (c: T) => number,
  factorFn?: (c: T) => number,
) {
  const acc = new Map<string, number>();
  for (const c of list) {
    acc.set(keyFn(c), (acc.get(keyFn(c)) || 0) + valFn(c) * (factorFn?.(c) ?? 1));
  }
  return [...acc.entries()].sort((a, b) => b[1] - a[1]);
}

// maskKey renders a masked key like "sk-or-v1-063...f48" (first 12 + last 3).
// Masked unconditionally so a full key_value never reaches the UI.
export const maskKey = (raw: string): string => {
  if (!raw) return '';
  return `${raw.slice(0, 12)}...${raw.slice(-3)}`;
};

// toChartData builds stacked-chart rows: bucket -> { label, [group]: value }.
export function toChartData(resp: ActivityResponse): Array<Record<string, string | number>> {
  const groups = Array.from(new Set(resp.series.map(s => s.group)));
  const data: Array<Record<string, string | number>> = resp.buckets.map(b => {
    const row: Record<string, string | number> = { label: b };
    groups.forEach(g => { row[g] = 0; });
    return row;
  });
  const bucketIdx = new Map(resp.buckets.map((b, i) => [b, i]));
  for (const p of resp.series) {
    const i = bucketIdx.get(p.bucket);
    if (i !== undefined) data[i][p.group] = p.value;
  }
  return data;
}

// TrendingRow is one entry of the Trends "Trending" card.
export interface TrendingRow {
  group: string;
  pct: number;   // relative change vs the previous period (-100.., 100 for New)
  isNew: boolean;
  spark: number[]; // previous-period series (flat zeros when no prior usage)
}

// computeTrending builds the "Trending" list: relative change vs the
// previous period per entity. "Other" is an aggregation bucket, never a real
// entity — excluded. Ranked by the absolute drop (prev − cur); entities new
// to this period (prev = 0, cur > 0) rank by their current value. Up to 6
// rows, like the reference page.
export function computeTrending(cur: ActivityResponse, prev: ActivityResponse): TrendingRow[] {
  const prevSum = new Map(prev.summary.map(s => [s.group, s.sum]));
  const names = new Set([...cur.summary.map(s => s.group), ...prevSum.keys()]);
  return [...names]
    .filter(g => g !== 'Other')
    .map(g => {
      const c = cur.summary.find(s => s.group === g)?.sum ?? 0;
      const p = prevSum.get(g) ?? 0;
      if (c === 0 && p === 0) return null;
      const isNew = p === 0 && c > 0;
      const pct = p > 0 ? ((c - p) / p) * 100 : 100;
      let spark = prev.series.filter(s => s.group === g).map(s => s.value);
      if (spark.length === 0) spark = prev.buckets.map(() => 0);
      return { group: g, pct, isNew, spark, sortVal: p > 0 ? p - c : c };
    })
    .filter((r): r is NonNullable<typeof r> => r !== null)
    .sort((a, b) => b.sortVal - a.sortVal)
    .slice(0, 6)
    .map(({ group, pct, isNew, spark }) => ({ group, pct, isNew, spark }));
}
