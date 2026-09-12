// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import dayjs from 'dayjs';
import ActivityOverview from './ActivityOverview';
import { getConsumptions, getKeys } from '../api/client';
import { customRange } from './activityShared';
import type { Consumption } from '../api/client';

type ConsumptionResponse = Awaited<ReturnType<typeof getConsumptions>>;

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return { ...actual, getConsumptions: vi.fn(), getKeys: vi.fn() };
});

vi.mock('recharts', () => {
  const pass = ({ children }: { children?: unknown }) => children ?? null;
  const nul = () => null;
  return {
    ResponsiveContainer: pass,
    BarChart: pass,
    Bar: nul,
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

const makeConsumption = (cost: number, hourBucket: string): Consumption => ({
  id: 1,
  key_id: 1,
  hour_bucket: hourBucket,
  model_name: 'model',
  app_name: 'app',
  request_count: 1,
  input_tokens: 100,
  output_tokens: 100,
  cache_hit_tokens: 0,
  cache_write_tokens: 0,
  cost_usd: cost,
});

const response = (rows: Consumption[], headers: Record<string, string> = {}) => ({
  data: rows,
  headers: {
    ...headers,
    get: (name: string) => headers[name.toLowerCase()],
  },
}) as unknown as ConsumptionResponse;

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(res => { resolve = res; });
  return { promise, resolve };
};

beforeEach(() => {
  vi.mocked(getConsumptions).mockReset();
  vi.mocked(getKeys).mockReset();
  vi.mocked(getKeys).mockResolvedValue({ data: [] } as unknown as Awaited<ReturnType<typeof getKeys>>);
});

afterEach(() => cleanup());

describe('ActivityOverview custom range refresh', () => {
  it('clears the previous snapshot when a custom range bound changes', async () => {
    const requests: Array<ReturnType<typeof deferred<ConsumptionResponse>>> = [];
    vi.mocked(getConsumptions).mockImplementation(() => {
      const request = deferred<ConsumptionResponse>();
      requests.push(request);
      return request.promise;
    });

    const firstRange = customRange(
      dayjs('2026-08-01T00:00:00'),
      dayjs('2026-08-02T00:00:00'),
    );
    const nextRange = customRange(
      dayjs('2026-08-01T00:00:00'),
      dayjs('2026-08-03T00:00:00'),
    );

    const { rerender } = render(<ActivityOverview range={firstRange} />);
    await waitFor(() => expect(requests).toHaveLength(2));

    await act(async () => {
      requests[0].resolve(response([makeConsumption(100, '2026-08-01T12:00:00')]));
      requests[1].resolve(response([]));
    });
    expect(await screen.findByText('$100.00')).not.toBeNull();

    rerender(<ActivityOverview range={nextRange} />);
    await waitFor(() => expect(requests).toHaveLength(4));
    expect(screen.queryByText('$100.00')).toBeNull();

    await act(async () => {
      requests[2].resolve(response([makeConsumption(200, '2026-08-02T12:00:00')]));
      requests[3].resolve(response([]));
    });
    expect(await screen.findByText('$200.00')).not.toBeNull();
  });
});

describe('ActivityOverview truncated responses', () => {
  it('warns when the server truncates consumption rows', async () => {
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption(1, '2026-08-01T12:00:00')], { 'x-consumptions-truncated': 'true' }))
      .mockResolvedValueOnce(response([]));

    render(<ActivityOverview range={customRange(
      dayjs('2026-08-01T00:00:00'),
      dayjs('2026-08-02T00:00:00'),
    )} />);

    expect(await screen.findByText(/activity data is incomplete/i)).not.toBeNull();
    expect(screen.getByText(/narrow the time range or add a filter/i)).not.toBeNull();
  });
});
