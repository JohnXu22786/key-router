// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import dayjs from 'dayjs';
import ActivityTrends from './ActivityTrends';
import { getActivity, getKeys } from '../api/client';
import type { ActivityResponse, ActivityGroupSummary } from '../api/client';
import type { DateRange } from './activityShared';

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return { ...actual, getActivity: vi.fn(), getKeys: vi.fn() };
});

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

const response: ActivityResponse = {
  metric: 'spend',
  group_by: 'model',
  rollup: 'day',
  buckets: ['2026-08-13'],
  series: [{ bucket: '2026-08-13', group: 'model-1', value: 10, is_zero: false }],
  summary: [{
    group: 'model-1', min: 10, max: 10, avg: 10, sum: 10, value: 10, percent: 100,
  } as ActivityGroupSummary],
  totals: { spend: 10, tokens: 0, requests: 0, cache: 0 },
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

const range = (until: string): DateRange => ({
  key: 'custom',
  label: 'Custom',
  badge: '',
  since: dayjs('2026-08-12T00:00:00'),
  until: dayjs(until),
  granularity: 'day',
});

const presetRange = (since: string, until: string): DateRange => ({
  key: 'today',
  label: 'Today',
  badge: '24h',
  since: dayjs(since),
  until: dayjs(until),
  granularity: 'hour',
});

beforeEach(() => {
  vi.mocked(getActivity).mockReset();
  vi.mocked(getKeys).mockReset();
  vi.mocked(getKeys).mockResolvedValue({ data: [] } as unknown as Awaited<ReturnType<typeof getKeys>>);
});

afterEach(() => cleanup());

describe('ActivityTrends custom range refresh', () => {
  it('clears stale data when custom bounds change and the replacement requests fail', async () => {
    type ActivityResult = Awaited<ReturnType<typeof getActivity>>;
    const requests: Array<ReturnType<typeof deferred<ActivityResult>>> = [];
    vi.mocked(getActivity).mockImplementation(() => {
      const request = deferred<ActivityResult>();
      requests.push(request);
      return request.promise;
    });

    const { rerender } = render(<ActivityTrends range={range('2026-08-14T00:00:00')} />);
    await waitFor(() => expect(requests).toHaveLength(6));

    await act(async () => {
      requests.forEach(request => request.resolve({ data: response } as ActivityResult));
      await Promise.all(requests.map(request => request.promise));
    });
    expect(screen.getAllByText('model-1').length).toBeGreaterThan(0);

    rerender(<ActivityTrends range={range('2026-08-15T00:00:00')} />);
    await waitFor(() => expect(requests).toHaveLength(12));
    expect(screen.queryAllByText('model-1')).toHaveLength(0);

    await act(async () => {
      requests.slice(6).forEach(request => request.reject(new Error('offline')));
      await Promise.all(requests.slice(6).map(request => request.promise.catch(() => undefined)));
    });
    expect(screen.getAllByText(/Failed to load Models trends/).length).toBeGreaterThan(0);
    expect(screen.queryAllByText('model-1')).toHaveLength(0);
  });
});

describe('ActivityTrends preset range refresh', () => {
  it('clears stale data when preset bounds advance and the replacement requests fail', async () => {
    type ActivityResult = Awaited<ReturnType<typeof getActivity>>;
    const requests: Array<ReturnType<typeof deferred<ActivityResult>>> = [];
    vi.mocked(getActivity).mockImplementation(() => {
      const request = deferred<ActivityResult>();
      requests.push(request);
      return request.promise;
    });

    const { rerender } = render(
      <ActivityTrends range={presetRange('2026-08-14T00:00:00', '2026-08-15T00:00:00')} />,
    );
    await waitFor(() => expect(requests).toHaveLength(6));

    await act(async () => {
      requests.forEach(request => request.resolve({ data: response } as ActivityResult));
      await Promise.all(requests.map(request => request.promise));
    });
    expect(screen.getAllByText('model-1').length).toBeGreaterThan(0);

    rerender(
      <ActivityTrends range={presetRange('2026-08-15T00:00:00', '2026-08-16T00:00:00')} />,
    );
    await waitFor(() => expect(requests).toHaveLength(12));

    await act(async () => {
      requests.slice(6).forEach(request => request.reject(new Error('offline')));
      await Promise.all(requests.slice(6).map(request => request.promise.catch(() => undefined)));
    });
    expect(screen.getAllByText(/Failed to load Models trends/).length).toBeGreaterThan(0);
    expect(screen.queryAllByText('model-1')).toHaveLength(0);
  });
});
