import dayjs from 'dayjs';

// Shared helpers for the Activity pages. Kept OUT of Activity.tsx to avoid
// the circular import (Activity -> ActivityOverview -> Activity): a
// circular dependency can yield undefined constants during module init and
// crash the page.
export interface DateRange {
  key: string;
  label: string;
  since: dayjs.Dayjs;
  until: dayjs.Dayjs;
}

// makeRanges builds the date presets relative to a reference time (now).
// The Activity page re-runs this on Refresh so the window slides to the
// current moment instead of staying frozen at page-load time.
export function makeRanges(now: dayjs.Dayjs): DateRange[] {
  return [
    { key: 'today', label: 'Today', since: now.startOf('day'), until: now },
    { key: '24h', label: '24h', since: now.subtract(24, 'hour'), until: now },
    { key: '3d', label: '3 days', since: now.subtract(3, 'day'), until: now },
    { key: '7d', label: '7 days', since: now.subtract(7, 'day'), until: now },
    { key: '1mo', label: '1 month', since: now.subtract(1, 'month'), until: now },
  ];
}

export const RANGES = makeRanges(dayjs());

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

// fmtDayLabel renders "MMM D" (Jul 11) for a "MM-DD" bucket string.
export const fmtDayLabel = (mmdd: string): string => {
  const [m, d] = mmdd.split('-').map(Number);
  if (!m || !d) return mmdd;
  const names = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  return `${names[m - 1] || m} ${d}`;
};

// fmtTokensNoSuffix renders bare compact tokens (362M) for KPI values.
export const fmtTokensBare = (v: number): string => fmtCompact(v);

export const fmtTokens = (v: number): string => fmtCompact(v) + ' tok';

export const fmtPercent = (v: number): string => {
  if (v >= 100) return `${Math.round(v)}%`;
  if (v >= 1) return `${v.toFixed(1)}%`;
  return `${v.toFixed(2)}%`;
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
