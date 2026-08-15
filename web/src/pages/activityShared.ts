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

// bucketStarts lists the bucket START times covering since..until at the
// granularity's step (sub-hour granularities advance by STEP_MIN minutes
// and the min15 axis is floored to the 15-minute clock cell; the others
// advance by their calendar unit). Buckets are kept only while their sort
// key strictly increases: DST fall-back repeats wall-clock times (dayjs
// steps in elapsed time) — for hour granularity the repeat is consecutive
// ("01:00" twice), for minute steps the whole repeated hour re-appears
// non-consecutively ("01:59" then "01:00" again) — dropping the repeats
// keeps the axis monotonic and the repeated hour's usage lands on its
// first occurrence. (Rows serialized with the second occurrence's offset
// are re-anchored by dayjs to the first occurrence's hour — the chart and
// the KPI proration shift identically, so the totals stay consistent.)
function bucketStarts(since: dayjs.Dayjs, until: dayjs.Dayjs, granularity: Granularity): dayjs.Dayjs[] {
  const stepMin = STEP_MIN[granularity];
  const unit = granularity === 'hour' ? 'hour' : granularity === 'day' ? 'day' : 'month';
  const out: dayjs.Dayjs[] = [];
  let cur = stepMin != null ? since.startOf('minute')
    : granularity === 'hour' ? since.startOf('hour')
    : granularity === 'day' ? since.startOf('day')
    : since.startOf('month');
  if (granularity === 'min15') cur = cur.subtract(cur.minute() % 15, 'minute');
  let prevSort = '';
  while (!cur.isAfter(until)) {
    const sort = bucketLabel(granularity, cur).sort;
    if (sort > prevSort) { out.push(cur); prevSort = sort; }
    cur = stepMin != null ? cur.add(stepMin, 'minute') : cur.add(1, unit);
  }
  return out;
}

// bucketAxis builds a CONTINUOUS bucket axis over since..until at the given
// granularity (empty buckets included, like OR's full-range charts).
export function bucketAxis(since: dayjs.Dayjs, until: dayjs.Dayjs, granularity: Granularity): SeriesPoint[] {
  return bucketStarts(since, until, granularity).map(t => {
    const f = bucketLabel(granularity, t);
    return { label: f.label, sort: f.sort, value: 0 };
  });
}

// overlapFractions splits ONE hourly row across the sub-hour axis buckets
// overlapping the WINDOW [since, until), returning [axisIndex, fraction]
// pairs. A row covers [hour, hour+1h) but only up to `cutoff` — the time
// its value was recorded: the current hour accumulates live usage, so a
// PAST window (previous period, calendar preset) shares its boundary hour
// with the live window, and that hour's value must be divided by its real
// coverage (cutoff - hour), not by (until - hour). Each bucket gets the
// fraction of the row proportional to its overlap with the row's data and
// the window — the uniform-within-hour assumption needed because the
// stored data is hourly while the axis is finer. Values outside the window
// are never counted, so the window total is preserved.
function overlapFractions(
  hourBucket: string,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  starts: dayjs.Dayjs[],
  stepMin: number,
): Array<[number, number]> {
  const h = dayjs(hourBucket).startOf('hour');
  let covEnd = h.add(1, 'hour');
  if (covEnd.isAfter(cutoff)) covEnd = cutoff;
  const coverage = covEnd.diff(h, 'minute', true);
  if (coverage <= 0) return [];
  const perMin = 1 / coverage;
  const out: Array<[number, number]> = [];
  for (let i = 0; i < starts.length; i++) {
    const s = starts[i];
    const e = s.add(stepMin, 'minute');
    const overlap = Math.min(e.valueOf(), covEnd.valueOf(), until.valueOf())
      - Math.max(s.valueOf(), h.valueOf(), since.valueOf());
    if (overlap > 0) out.push([i, perMin * (overlap / 60000)]);
  }
  return out;
}

// bucketWindowShare returns the fraction of a bucket's recorded value that
// lies inside [since, until]. hour_bucket rows are truncated to the LOCAL
// hour; the "bucket" is the containing hour/day/month for the corresponding
// granularity — sub-hour windows (minute/min15) still have HOURLY rows, so
// they share the 'hour' math (rowWindowShare's coverage, generalized).
//
// A bucket covers [start, start+unit); its recorded value only covers up to
// `cutoff` (the fetch time) — the bucket containing cutoff accumulates live
// usage, so its value is divided by the real coverage, never by a window
// boundary (a past window sharing that hour must not inflate it).
//
// Interior buckets lie fully inside the window and keep share 1; boundary
// buckets are prorated to the overlap, so a rolling window shows exactly
// the data inside its span and repeated auto-refreshes never accumulate
// pre-window (or post-window) usage into the totals.
export function bucketWindowShare(
  hourBucket: string,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: Granularity,
): number {
  const h = dayjs(hourBucket);
  // Rows are always hourly; day/month granularity aggregates the containing
  // calendar unit, sub-hour (minute/min15) stays on the hour.
  const rowUnit = granularity === 'day' ? 'day' : granularity === 'month' ? 'month' : 'hour';
  const start = rowUnit === 'hour' ? h.startOf('hour') : rowUnit === 'day' ? h.startOf('day') : h.startOf('month');
  let covEnd = start.add(1, rowUnit);
  // DST fall-back repeats a wall-clock hour (01:00 EDT then 01:00 EST) and
  // both occurrences truncate to the SAME hourly row, so its recorded value
  // covers two elapsed hours. The elapsed +1h step lands on the same
  // wall-clock hour exactly then (spring-forward skips an hour and lands
  // two ahead; half-hour zones like Lord Howe land on :30) — walk minute by
  // minute to the first instant whose wall-clock hour differs, which is the
  // second occurrence's end in every transition shape. A window crossing
  // the repeat hour must divide by this real coverage, never by 60 minutes
  // (that would inflate the share up to 2x).
  if (rowUnit === 'hour' && covEnd.hour() === start.hour()) {
    while (covEnd.hour() === start.hour() && covEnd.diff(start, 'minute', true) < 180) {
      covEnd = covEnd.add(1, 'minute');
    }
  }
  if (covEnd.isAfter(cutoff)) covEnd = cutoff;
  const coverage = covEnd.diff(start, 'minute', true);
  if (coverage <= 0) return 0;
  const overlap = Math.min(covEnd.valueOf(), until.valueOf()) - Math.max(start.valueOf(), since.valueOf());
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
): SeriesPoint[] {
  const axis = bucketAxis(since, until, granularity);
  const stepMin = STEP_MIN[granularity];
  if (stepMin != null) {
    // Sub-hour axis: the rows are hourly, so distribute each row's value
    // over the minute buckets overlapping its hour.
    const starts = bucketStarts(since, until, granularity);
    for (const r of list) {
      for (const [i, f] of overlapFractions(r.hour_bucket, since, until, cutoff, starts, stepMin)) {
        axis[i].value += valFn(r) * f;
      }
    }
    return axis;
  }
  const keyOf = (t: dayjs.Dayjs): string =>
    granularity === 'hour' ? t.format('YYYY-MM-DD HH:00') : granularity === 'day' ? t.format('YYYY-MM-DD') : t.format('YYYY-MM');
  const idx = new Map(axis.map((p, i) => [p.sort, i]));
  for (const r of list) {
    const i = idx.get(keyOf(dayjs(r.hour_bucket)));
    if (i !== undefined) {
      axis[i].value += valFn(r) * bucketWindowShare(r.hour_bucket, since, until, cutoff, granularity);
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
): Array<Record<string, any>> {
  const axis = bucketAxis(since, until, granularity);
  const rows = axis.map(p => {
    const row: Record<string, any> = { label: p.label, sort: p.sort };
    groups.forEach(g => { row[g] = 0; });
    return row;
  });
  const stepMin = STEP_MIN[granularity];
  if (stepMin != null) {
    // Sub-hour axis: distribute each hourly row over its overlapping
    // minute buckets (see series).
    const starts = bucketStarts(since, until, granularity);
    for (const r of list) {
      const g = keyFn(r);
      const v = valFn(r);
      for (const [i, f] of overlapFractions(r.hour_bucket, since, until, cutoff, starts, stepMin)) {
        rows[i][g] = (rows[i][g] ?? 0) + v * f;
      }
    }
    return rows;
  }
  const keyOf = (t: dayjs.Dayjs): string =>
    granularity === 'hour' ? t.format('YYYY-MM-DD HH:00') : granularity === 'day' ? t.format('YYYY-MM-DD') : t.format('YYYY-MM');
  const idx = new Map(axis.map((p, i) => [p.sort, i]));
  for (const r of list) {
    const i = idx.get(keyOf(dayjs(r.hour_bucket)));
    if (i !== undefined) {
      const g = keyFn(r);
      // Rows whose group is outside `groups` still accumulate under their
      // own key — callers fold those keys into "Other". Guarding against
      // an uninitialized key keeps the fold from summing NaN into Other.
      rows[i][g] = (rows[i][g] ?? 0) + valFn(r) * bucketWindowShare(r.hour_bucket, since, until, cutoff, granularity);
    }
  }
  return rows;
}

// resampleResponse re-samples an HOURLY-rolled ActivityResponse onto a
// sub-hour client axis (the Trends/Explore API rolls up at most hourly —
// see activityWindow in admin.go). Every hourly series point is distributed
// over the minute buckets overlapping the window exactly like series() does
// for raw rows (cutoff = the fetch time, see overlapFractions); every group
// stays zero-filled per bucket (the server emits full bucket x group grids
// with is_zero flags). Summary and totals are RECOMPUTED from the resampled
// buckets (server semantics: min/max/avg over non-empty buckets, value =
// last bucket, percent of the total) so the Trends "Trending" deltas agree
// with the re-bucketed chart.
export function resampleResponse(
  resp: ActivityResponse,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  cutoff: dayjs.Dayjs,
  granularity: 'minute' | 'min15',
): ActivityResponse {
  const stepMin = STEP_MIN[granularity]!;
  const starts = bucketStarts(since, until, granularity);
  const buckets = bucketAxis(since, until, granularity).map(p => p.label);
  const groups = Array.from(new Set(resp.series.map(s => s.group)));
  // group -> bucket label -> value
  const acc = new Map<string, Map<string, number>>();
  for (const p of resp.series) {
    if (p.value === 0) continue;
    let base = acc.get(p.group);
    if (!base) { base = new Map(); acc.set(p.group, base); }
    for (const [i, f] of overlapFractions(p.bucket, since, until, cutoff, starts, stepMin)) {
      const b = buckets[i];
      base.set(b, (base.get(b) ?? 0) + p.value * f);
    }
  }
  const series: ActivitySeriesPoint[] = [];
  for (const b of buckets) {
    for (const g of groups) {
      const v = acc.get(g)?.get(b) ?? 0;
      series.push({ bucket: b, group: g, value: v, is_zero: v === 0 });
    }
  }
  const groupSum = new Map<string, number>();
  let totalSum = 0;
  for (const g of groups) {
    const s = buckets.reduce((a, b) => a + (acc.get(g)?.get(b) ?? 0), 0);
    groupSum.set(g, s);
    totalSum += s;
  }
  const summary: ActivityGroupSummary[] = groups.map(g => {
    const vals = buckets.map(b => acc.get(g)?.get(b) ?? 0);
    const nonZero = vals.filter(v => v > 0);
    const sum = groupSum.get(g) ?? 0;
    return {
      group: g,
      min: nonZero.length ? Math.min(...nonZero) : 0,
      max: nonZero.length ? Math.max(...nonZero) : 0,
      avg: nonZero.length ? sum / nonZero.length : 0,
      sum,
      value: vals[vals.length - 1] ?? 0,
      percent: totalSum > 0 ? (sum / totalSum) * 100 : 0,
    };
  });
  // Groups folded away server-side (beyond top-N) keep their ORIGINAL
  // summary rows: the response only carries their totals, so they cannot be
  // re-bucketed — Trends' deltas for them stay on the server's raw sums,
  // exactly as before this change (the chart itself only draws top-N + Other).
  const resampledGroups = new Set(groups);
  summary.push(...resp.summary.filter(s => !resampledGroups.has(s.group)));
  const totals = { ...resp.totals };
  const metricTotal = series.reduce((a, p) => a + p.value, 0);
  if (resp.metric === 'spend') totals.spend = metricTotal;
  else if (resp.metric === 'tokens') totals.tokens = metricTotal;
  else if (resp.metric === 'requests') totals.requests = metricTotal;
  else if (resp.metric === 'cache') totals.cache = metricTotal;
  return { ...resp, rollup: granularity, buckets, series, summary, totals };
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
