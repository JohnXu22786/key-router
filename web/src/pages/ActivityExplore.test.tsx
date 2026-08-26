// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/react';
import dayjs from 'dayjs';
import ActivityExplore from './ActivityExplore';
import { getActivity } from '../api/client';
import type { ActivityResponse, ActivityGroupSummary } from '../api/client';
import type { DateRange } from './activityShared';

// The page slices the server summary to Top-N client-side, but the endpoint
// returns a summary row for EVERY group in the window. Regression: the
// footer used to print data.summary.length — the uncapped count — while the
// table shows only the sliced rows (25 groups with Top 10: "25 rows" over a
// 10-row table).
vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return { ...actual, getActivity: vi.fn() };
});

// The chart needs a layout engine jsdom lacks (ResizeObserver-backed
// ResponsiveContainer); the table/footer are what this test pins, so render
// the chart as a no-op.
vi.mock('recharts', () => {
  const pass = ({ children }: { children?: unknown }) => children ?? null;
  const nul = () => null;
  return {
    ResponsiveContainer: pass,
    BarChart: pass,
    Bar: nul,
    AreaChart: pass,
    Area: nul,
    LineChart: pass,
    Line: nul,
    XAxis: nul,
    YAxis: nul,
    CartesianGrid: nul,
    Tooltip: nul,
  };
});

// antd's Table touches matchMedia/ResizeObserver on mount; jsdom has neither.
if (typeof window.matchMedia !== 'function') {
  (window as unknown as { matchMedia: (q: string) => unknown }).matchMedia = (q: string) => ({
    matches: false,
    media: q,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  });
}
if (typeof (window as unknown as { ResizeObserver?: unknown }).ResizeObserver !== 'function') {
  (window as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

// A 1y rolling range: granularity 'month' vs the page's default 'day' rollup
// makes prorateBoundaryBuckets pass the response through untouched.
const range: DateRange = {
  key: '1y',
  label: 'Past 1 Year',
  badge: '1y',
  since: dayjs('2025-08-13T00:00:00'),
  until: dayjs('2026-08-13T00:00:00'),
  granularity: 'month',
};

const makeResponse = (n: number): ActivityResponse => ({
  metric: 'spend',
  group_by: 'model',
  rollup: 'day',
  buckets: ['2026-08-13'],
  series: Array.from({ length: n }, (_, i) => ({
    bucket: '2026-08-13', group: `model-${i + 1}`, value: (i + 1) * 10, is_zero: false,
  })),
  summary: Array.from({ length: n }, (_, i): ActivityGroupSummary => ({
    group: `model-${i + 1}`,
    min: 1, max: 2, avg: 1.5, sum: i + 1, value: i + 1, percent: 100 / n,
  })),
  totals: { spend: n * 10, tokens: 0, requests: 0, cache: 0 },
});

const mockSummary = (n: number) => {
  vi.mocked(getActivity).mockResolvedValue({
    data: makeResponse(n),
  } as Awaited<ReturnType<typeof getActivity>>);
};

beforeEach(() => {
  vi.mocked(getActivity).mockReset();
});

// vitest runs without globals, so RTL's auto-cleanup never registers; without
// this the previous test's rendered tree stays in document.body.
afterEach(() => cleanup());

describe('ActivityExplore summary footer', () => {
  it('counts the rows actually rendered when the server summary exceeds Top-N (default 10)', async () => {
    mockSummary(25);
    const { container } = render(<ActivityExplore range={range} />);

    const footer = await screen.findByText(/rows ·/);
    expect(footer.textContent).toMatch(/^10 rows · \d+ms$/);
    expect(container.querySelectorAll('.ant-table-tbody .ant-table-row')).toHaveLength(10);
  });

  it('keeps matching when the summary fits inside Top-N', async () => {
    mockSummary(5);
    const { container } = render(<ActivityExplore range={range} />);

    const footer = await screen.findByText(/rows ·/);
    expect(footer.textContent).toMatch(/^5 rows · \d+ms$/);
    expect(container.querySelectorAll('.ant-table-tbody .ant-table-row')).toHaveLength(5);
  });
});