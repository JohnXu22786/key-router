import dayjs from 'dayjs';
import { ActivityResponse } from '../api/client';

// Shared helpers for the Activity pages. Kept OUT of Activity.tsx so no page
// ever imports from it (Activity -> child -> activityShared would be a
// circular import, which can hand the children undefined constants during
// module init and crash the page). activityShared itself only imports
// dayjs and the API client — a leaf module.

// Bucket granularity for a range. Mirrors OpenRouter's rollup ladder
// (Hourly / Daily / Weekly / Monthly): sub-3-day windows bucket by hour,
// up to two months by day, anything longer by month.
export type Granularity = 'hour' | 'day' | 'month';

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
    // Rolling windows (badge = the preset's own compact duration).
    { key: '15m', label: 'Past 15 Minutes', badge: '15m', since: now.subtract(15, 'minute'), until: now, granularity: 'hour' },
    { key: '30m', label: 'Past 30 Minutes', badge: '30m', since: now.subtract(30, 'minute'), until: now, granularity: 'hour' },
    { key: '1h', label: 'Past 1 Hour', badge: '1h', since: now.subtract(1, 'hour'), until: now, granularity: 'hour' },
    { key: '3h', label: 'Past 3 Hours', badge: '3h', since: now.subtract(3, 'hour'), until: now, granularity: 'hour' },
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
// sub-3-day windows stay hourly, up to two months daily, longer monthly.
export function granularityFor(since: dayjs.Dayjs, until: dayjs.Dayjs): Granularity {
  const days = until.diff(since, 'hour', true) / 24;
  if (days < 3) return 'hour';
  if (days < 60) return 'day';
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

// fmtTick renders a bucket label for chart axes: hour -> "15:00",
// day ("MM-DD" or "YYYY-MM-DD") -> "Jul 13", week ("YYYY-MM-DD") -> "Aug 13",
// month ("YYYY-MM") -> "Aug 26".
export function fmtTick(granularity: Granularity, label: string): string {
  if (granularity === 'hour') {
    // "YYYY-MM-DD HH:00" -> slice(11) = "HH:00"; client-side "MM-DD HH:00" -> slice(6).
    return label.indexOf('-') === 4 ? label.slice(11) : label.slice(6);
  }
  if (label.length >= 7 && label.indexOf('-') === 4) {
    const [y, m, d] = label.split('-');
    return d ? `${MONTHS[Number(m) - 1] || m} ${Number(d)}` : `${MONTHS[Number(m) - 1] || m} ${y.slice(2)}`;
  }
  return fmtDayLabel(label);
}

// fmtBucket renders a bucket label for tooltips: "Jul 13, 15:00" /
// "Jul 13" / "Aug 13, 2026" / "Aug 2026".
export function fmtBucket(granularity: Granularity, label: string): string {
  if (granularity === 'hour') {
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

// A row needs only hour_bucket for bucketing; callers pass their own value
// extractor.
export interface BucketedRow { hour_bucket: string; }

export interface SeriesPoint {
  label: string; // human label: "MM-DD HH:00" / "MM-DD" / "YYYY-MM"
  sort: string;  // sortable key
  value: number;
}

// bucketAxis builds a CONTINUOUS bucket axis over since..until at the given
// granularity (empty buckets included, like OR's full-range charts).
export function bucketAxis(since: dayjs.Dayjs, until: dayjs.Dayjs, granularity: Granularity): SeriesPoint[] {
  const fmt = (t: dayjs.Dayjs): { label: string; sort: string } => {
    switch (granularity) {
      case 'hour': return { label: t.format('MM-DD HH:00'), sort: t.format('YYYY-MM-DD HH:00') };
      case 'day': return { label: t.format('MM-DD'), sort: t.format('YYYY-MM-DD') };
      case 'month': return { label: t.format('YYYY-MM'), sort: t.format('YYYY-MM') };
    }
  };
  const step = granularity === 'hour' ? 'hour' : granularity === 'day' ? 'day' : 'month';
  const out: SeriesPoint[] = [];
  let cur = granularity === 'hour' ? since.startOf('hour') : granularity === 'day' ? since.startOf('day') : since.startOf('month');
  while (!cur.isAfter(until)) {
    const f = fmt(cur);
    // DST fall-back repeats a wall-clock hour (dayjs 'hour' steps in elapsed
    // time); keep one bucket — the duplicate would double-print the values.
    if (out.length === 0 || out[out.length - 1].label !== f.label) {
      out.push({ label: f.label, sort: f.sort, value: 0 });
    }
    cur = cur.add(1, step);
  }
  return out;
}

// series buckets rows by the range's granularity onto a continuous axis.
export function series<T extends BucketedRow>(
  list: T[],
  valFn: (c: T) => number,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  granularity: Granularity,
): SeriesPoint[] {
  const axis = bucketAxis(since, until, granularity);
  const keyOf = (t: dayjs.Dayjs): string =>
    granularity === 'hour' ? t.format('YYYY-MM-DD HH:00') : granularity === 'day' ? t.format('YYYY-MM-DD') : t.format('YYYY-MM');
  const idx = new Map(axis.map((p, i) => [p.sort, i]));
  for (const r of list) {
    const i = idx.get(keyOf(dayjs(r.hour_bucket)));
    if (i !== undefined) axis[i].value += valFn(r);
  }
  return axis;
}

// stackedData buckets rows per group onto a continuous axis; every group
// gets a column in each bucket row (zero-filled), like OR's stacked charts.
export function stackedData<T extends BucketedRow>(
  list: T[],
  groups: string[],
  keyFn: (c: T) => string,
  valFn: (c: T) => number,
  since: dayjs.Dayjs,
  until: dayjs.Dayjs,
  granularity: Granularity,
): Array<Record<string, any>> {
  const axis = bucketAxis(since, until, granularity);
  const rows = axis.map(p => {
    const row: Record<string, any> = { label: p.label, sort: p.sort };
    groups.forEach(g => { row[g] = 0; });
    return row;
  });
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
      rows[i][g] = (rows[i][g] ?? 0) + valFn(r);
    }
  }
  return rows;
}

// groupTotals sums a metric per group, sorted descending.
export function groupTotals<T extends BucketedRow>(list: T[], keyFn: (c: T) => string, valFn: (c: T) => number) {
  const acc = new Map<string, number>();
  for (const c of list) {
    acc.set(keyFn(c), (acc.get(keyFn(c)) || 0) + valFn(c));
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
