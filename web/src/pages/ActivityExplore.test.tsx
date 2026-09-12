// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { ReactNode } from 'react';
import { render, screen, cleanup, act, waitFor } from '@testing-library/react';
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
  const pass = ({ children }: { children?: ReactNode }) => children ?? null;
  const chart = ({ children }: { children?: ReactNode }) => <div data-testid="explore-chart">{children}</div>;
  const nul = () => null;
  return {
    ResponsiveContainer: pass,
    BarChart: chart,
    Bar: nul,
    AreaChart: chart,
    Area: nul,
    LineChart: chart,
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

// Explore requests the hourly source with precise=true, so the fixture's
// boundary rows already contain their [15:50, 16:05) shares: 120 * 10/60
// and 10 * 5/60. The client must re-bucket these values without prorating
// them a second time.
const makeHourlyResponse = (): ActivityResponse => ({
  metric: 'spend',
  group_by: 'model',
  rollup: 'hour',
  buckets: ['2026-08-13 15:00', '2026-08-13 16:00'],
  series: [
    { bucket: '2026-08-13 15:00', group: 'model-1', value: 20, is_zero: false },
    { bucket: '2026-08-13 16:00', group: 'model-1', value: 5 / 6, is_zero: false },
  ],
  summary: [{ group: 'model-1', min: 5 / 6, max: 20, avg: 125 / 12, sum: 125 / 6, value: 5 / 6, percent: 100 }],
  totals: { spend: 125 / 6, tokens: 0, requests: 0, cache: 0 },
});

const emptyResponse: ActivityResponse = {
  metric: 'spend',
  group_by: 'model',
  rollup: 'day',
  buckets: [],
  series: [],
  summary: [],
  totals: { spend: 0, tokens: 0, requests: 0, cache: 0 },
};

const mockSummary = (n: number) => {
  vi.mocked(getActivity).mockResolvedValue({
    data: makeResponse(n),
  } as Awaited<ReturnType<typeof getActivity>>);
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
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
  it('shows the empty state instead of an empty chart and legend', async () => {
    vi.mocked(getActivity).mockResolvedValue({
      data: emptyResponse,
    } as Awaited<ReturnType<typeof getActivity>>);
    const { container } = render(<ActivityExplore range={range} />);

    expect(await screen.findAllByText('No usage in this period.')).toHaveLength(2);
    expect(screen.queryByTestId('explore-chart')).toBeNull();
    expect(container.querySelectorAll('.explore-legend-item')).toHaveLength(0);
  }, 10000);

  it('counts the rows actually rendered when the server summary exceeds Top-N (default 10)', async () => {
    mockSummary(25);
    const { container } = render(<ActivityExplore range={range} />);

    const footer = await screen.findByText(/rows ·/);
    expect(footer.textContent).toMatch(/^10 rows · \d+ms$/);
    expect(container.querySelectorAll('.ant-table-tbody .ant-table-row')).toHaveLength(10);
    expect(vi.mocked(getActivity)).toHaveBeenCalledWith(expect.objectContaining({ rollup: 'day' }));
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
      until: responseAt,
      granularity: 'day',
    };
    const response: ActivityResponse = {
      metric: 'spend',
      group_by: 'model',
      rollup: 'hour',
      buckets: ['2026-08-13 10:00'],
      series: [{ bucket: '2026-08-13 10:00', group: 'model-1', value: 100, is_zero: false }],
      summary: [{ group: 'model-1', min: 100, max: 100, avg: 100, sum: 100, value: 100, percent: 100 }],
      totals: { spend: 100, tokens: 0, requests: 0, cache: 0 },
    };

    render(<ActivityExplore range={slowRange} />);
    expect(getActivity).toHaveBeenCalledTimes(1);

    vi.setSystemTime(responseAt.toDate());
    await act(async () => {
      resolveActivity({ data: response } as Awaited<ReturnType<typeof getActivity>>);
      await pending;
    });

    // The hourly source row is only partially recorded at request start. The
    // response-time cutoff sees its 30 recorded minutes and keeps the full
    // selected slice instead of dropping it as empty.
    expect(screen.getAllByText('$100').length).toBeGreaterThan(0);
  });

  it('uses hourly data and re-samples short ranges instead of showing a full daily bucket', async () => {
    vi.mocked(getActivity).mockResolvedValue({
      data: makeHourlyResponse(),
    } as Awaited<ReturnType<typeof getActivity>>);
    const { container } = render(<ActivityExplore range={shortRange} />);

    const footer = await screen.findByText(/rows ·/);
    expect(footer).toBeTruthy();
    expect(vi.mocked(getActivity)).toHaveBeenCalledWith(expect.objectContaining({ rollup: 'hour', precise: true }));
    // The precise hourly response sums to $20.833..., matching only the
    // corresponding slice of the original $130 widened-hour fixture.
    expect(container.textContent).toContain('$20.8');
    expect(container.textContent).not.toContain('$130');
  });

  it('requests blended source metrics with blended ranking for the current metric', async () => {
    vi.mocked(getActivity).mockImplementation(async (params) => ({
      data: {
        ...makeHourlyResponse(),
        metric: params.metric!,
      },
    } as Awaited<ReturnType<typeof getActivity>>));
    render(<ActivityExplore range={shortRange} initialMetric="blended" />);

    await screen.findByText(/rows ·/);
    const calls = vi.mocked(getActivity).mock.calls;
    expect(calls.map(([params]) => params.metric).sort()).toEqual(['spend', 'tokens']);
    expect(calls.every(([params]) => params.rank_by === 'blended')).toBe(true);
  });

  it('clears stale data when custom bounds change and the replacement request fails', async () => {
    type ActivityResult = Awaited<ReturnType<typeof getActivity>>;
    const requests: Array<ReturnType<typeof deferred<ActivityResult>>> = [];
    vi.mocked(getActivity).mockImplementation(() => {
      const request = deferred<ActivityResult>();
      requests.push(request);
      return request.promise;
    });

    const firstRange: DateRange = {
      key: 'custom',
      label: 'Custom',
      badge: '',
      since: dayjs('2026-08-12T00:00:00'),
      until: dayjs('2026-08-14T00:00:00'),
      granularity: 'day',
    };
    const nextRange: DateRange = {
      ...firstRange,
      until: dayjs('2026-08-15T00:00:00'),
    };

    const { rerender } = render(<ActivityExplore range={firstRange} />);
    await waitFor(() => expect(requests).toHaveLength(1));

    await act(async () => {
      requests[0].resolve({ data: makeResponse(1) } as ActivityResult);
      await requests[0].promise;
    });
    expect(screen.getAllByText('model-1').length).toBeGreaterThan(0);

    rerender(<ActivityExplore range={nextRange} />);
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(screen.queryAllByText('model-1')).toHaveLength(0);

    await act(async () => {
      requests[1].reject(new Error('offline'));
      await requests[1].promise.catch(() => undefined);
    });
    expect(screen.getAllByText(/Failed to load explore/).length).toBeGreaterThan(0);
    expect(screen.queryAllByText('model-1')).toHaveLength(0);
  });
});
