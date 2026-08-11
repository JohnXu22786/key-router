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

export const RANGES: DateRange[] = [
  { key: 'today', label: 'Today', since: dayjs().startOf('day'), until: dayjs() },
  { key: '24h', label: '24h', since: dayjs().subtract(24, 'hour'), until: dayjs() },
  { key: '3d', label: '3 days', since: dayjs().subtract(3, 'day'), until: dayjs() },
  { key: '7d', label: '7 days', since: dayjs().subtract(7, 'day'), until: dayjs() },
  { key: '1mo', label: '1 month', since: dayjs().subtract(1, 'month'), until: dayjs() },
];

export const fmtCompact = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e12) return (v / 1e12).toFixed(2).replace(/\.?0+$/, '') + 'T';
  if (abs >= 1e9) return (v / 1e9).toFixed(2).replace(/\.?0+$/, '') + 'B';
  if (abs >= 1e6) return (v / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M';
  if (abs >= 1e3) return (v / 1e3).toFixed(1).replace(/\.?0$/, '') + 'k';
  return String(Math.round(v));
};

export const fmtUSD = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return `$${(v / 1e3).toFixed(1)}k`;
  return `$${v.toFixed(2)}`;
};

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
