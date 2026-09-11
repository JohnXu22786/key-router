// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup, act } from '@testing-library/react';
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

// A 1y rolling range whose bounds are aligned to the response's day buckets
// remains unchanged by boundary normalization.
const range: DateRange = {
  key: '1y',
  label: 'Past 1 Year',
  badge: '1y',
  since: dayjs('2025-08-13T00:00:00'),
  until: dayjs('2026-08-13T00:00:00'),
  granularity: 'month',
};

const shortRange: DateRange = {
  key: '15m',
  label: 'Past 15 Minutes',
  badge: '15m',
  since: dayjs('2026-08-13T15:50:00'),
  until: dayjs('2026-08-13T16:05:00'),
  granularity: 'minute',
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

const makeHourlyResponse = (): ActivityResponse => ({
  metric: 'spend',
  group_by: 'model',
  rollup: 'hour',
  buckets: ['2026-08-13 15:00', '2026-08-13 16:00'],
  series: [
    { bucket: '2026-08-13 15:00', group: 'model-1', value: 120, is_zero: false },
    { bucket: '2026-08-13 16:00', group: 'model-1', value: 10, is_zero: false },
  ],
  summary: [{ group: 'model-1', min: 10, max: 120, avg: 65, sum: 130, value: 10, percent: 100 }],
  totals: { spend: 130, tokens: 0, requests: 0, cache: 0 },
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
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

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
  it('uses the response-time cutoff for a slow custom-range response', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    const requestStartedAt = dayjs('2026-08-13T10:00:00');
    const responseAt = dayjs('2026-08-13T10:30:00');
    vi.setSystemTime(requestStartedAt.toDate());

    let resolveActivity!: (value: Awaited<ReturnType<typeof getActivity>>) => void;
    const pending = new Promise<Awaited<ReturnType<typeof getActivity>>>((resolve) => {
      resolveActivity = resolve;
    });
    vi.mocked(getActivity).mockReturnValue(pending);

    const slowRange: DateRange = {
      key: 'custom',
      label: 'Custom',
      badge: '',
      since: dayjs('2026-08-09T00:00:00'),
      until: requestStartedAt,
      granularity: 'day',
    };
    const response: ActivityResponse = {
      metric: 'spend',
      group_by: 'model',
      rollup: 'day',
      buckets: ['2026-08-09', '2026-08-10', '2026-08-11', '2026-08-12', '2026-08-13'],
      series: [
        ...['2026-08-09', '2026-08-10', '2026-08-11', '2026-08-12'].map(bucket => ({
          bucket, group: 'model-1', value: 1, is_zero: false,
        })),
        { bucket: '2026-08-13', group: 'model-1', value: 100, is_zero: false },
      ],
      summary: [{ group: 'model-1', min: 1, max: 100, avg: 20.8, sum: 104, value: 100, percent: 100 }],
      totals: { spend: 104, tokens: 0, requests: 0, cache: 0 },
    };

    render(<ActivityExplore range={slowRange} />);
    expect(getActivity).toHaveBeenCalledTimes(1);

    vi.setSystemTime(responseAt.toDate());
    await act(async () => {
      resolveActivity({ data: response } as Awaited<ReturnType<typeof getActivity>>);
      await pending;
    });

    // At request start the live day had 10h of recorded coverage, so using
    // that stale cutoff would leave the full $100 row untouched. At response
    // time it has 10.5h, and the selected 10h slice is $95.2.
    expect(screen.getAllByText('$95.2')).toHaveLength(2);
    expect(screen.queryByText('$100')).toBeNull();
  });

  it('uses hourly data and re-samples short ranges instead of showing a full daily bucket', async () => {
    vi.mocked(getActivity).mockResolvedValue({
      data: makeHourlyResponse(),
    } as Awaited<ReturnType<typeof getActivity>>);
    const { container } = render(<ActivityExplore range={shortRange} />);

    const footer = await screen.findByText(/rows ·/);
    expect(footer).toBeTruthy();
    expect(vi.mocked(getActivity)).toHaveBeenCalledWith(expect.objectContaining({ rollup: 'hour' }));
    // The hourly response sums to $130, while the 15-minute window contains
    // only the corresponding slice ($21 with the fixed fixture and the
    // current-range live-minute behavior).
    expect(container.textContent).toContain('$21');
    expect(container.textContent).not.toContain('$130');
});
});
