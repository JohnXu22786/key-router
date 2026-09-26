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
  let currentChartData: Record<string, any>[] = [];
  const pass = ({ children }: { children?: unknown }) => children ?? null;
  const barChart = ({ children, data }: { children?: unknown; data?: Record<string, any>[] }) => {
    currentChartData = data ?? [];
    return children ?? null;
  };
  const bar = ({ dataKey, name }: { dataKey: string | ((row: Record<string, any>) => unknown); name: string }) => {
    const values = currentChartData.map(row => typeof dataKey === 'function' ? dataKey(row) : row[dataKey]);
    return <span data-testid="activity-overview-series" data-name={name} data-values={JSON.stringify(values)} />;
  };
  const nul = () => null;
  return {
    ResponsiveContainer: pass,
    BarChart: barChart,
    Bar: bar,
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

const makeConsumption = (cost: number, hourBucket: string, inputTokens = 100, outputTokens = 100): Consumption => ({
  id: 1,
  key_id: 1,
  hour_bucket: hourBucket,
  model_name: 'model',
  app_name: 'app',
  request_count: 1,
  input_tokens: inputTokens,
  output_tokens: outputTokens,
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

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('ActivityOverview response-time cutoff', () => {
  it('uses the response-time cutoff for a slow response', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    const requestStartedAt = dayjs('2026-08-13T10:00:00');
    const responseAt = dayjs('2026-08-13T10:30:00');
    vi.setSystemTime(requestStartedAt.toDate());

    const requests: Array<ReturnType<typeof deferred<ConsumptionResponse>>> = [];
    vi.mocked(getConsumptions).mockImplementation(() => {
      const request = deferred<ConsumptionResponse>();
      requests.push(request);
      return request.promise;
    });

    render(<ActivityOverview range={customRange(
      dayjs('2026-08-09T00:00:00'),
      responseAt,
    )} />);
    await waitFor(() => expect(requests).toHaveLength(2));

    vi.setSystemTime(responseAt.toDate());
    await act(async () => {
      requests[0].resolve(response([makeConsumption(100, '2026-08-13T10:00:00')]));
      requests[1].resolve(response([]));
    });

    expect(await screen.findByText('$100.00')).not.toBeNull();
  });

  it('captures the cutoff when current consumption resolves before the other requests', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    const requestStartedAt = dayjs('2026-08-13T10:00:00');
    const currentResponseAt = dayjs('2026-08-13T10:30:00');
    const delayedResponseAt = dayjs('2026-08-13T11:30:00');
    vi.setSystemTime(requestStartedAt.toDate());

    const requests: Array<ReturnType<typeof deferred<ConsumptionResponse>>> = [];
    const keysResponse = deferred<Awaited<ReturnType<typeof getKeys>>>();
    vi.mocked(getConsumptions).mockImplementation(() => {
      const request = deferred<ConsumptionResponse>();
      requests.push(request);
      return request.promise;
    });
    vi.mocked(getKeys).mockImplementation(() => keysResponse.promise);

    render(<ActivityOverview range={customRange(
      dayjs('2026-08-13T09:00:00'),
      currentResponseAt,
    )} />);
    await waitFor(() => expect(requests).toHaveLength(2));

    vi.setSystemTime(currentResponseAt.toDate());
    await act(async () => {
      requests[0].resolve(response([makeConsumption(100, '2026-08-13T10:00:00')]));
    });

    vi.setSystemTime(delayedResponseAt.toDate());
    await act(async () => {
      requests[1].resolve(response([]));
      keysResponse.resolve({ data: [] } as unknown as Awaited<ReturnType<typeof getKeys>>);
    });

    expect(await screen.findByText('$100.00')).not.toBeNull();
  });
});

describe('ActivityOverview optional key metadata', () => {
  it('renders activity data with Key #id fallbacks when keys fail', async () => {
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption(100, '2026-08-01T12:00:00')]))
      .mockResolvedValueOnce(response([]));
    vi.mocked(getKeys).mockRejectedValueOnce(new Error('keys unavailable'));

    render(<ActivityOverview range={customRange(
      dayjs('2026-08-01T00:00:00'),
      dayjs('2026-08-02T00:00:00'),
    )} />);

    expect(await screen.findByText('$100.00')).not.toBeNull();
    expect(screen.getByText('Key #1')).not.toBeNull();
    expect(screen.queryByText('Failed to load activity — check the log file.')).toBeNull();
  });

  it('keeps duplicate key names separate and masks each key by ID', async () => {
    const first = { ...makeConsumption(1, '2026-08-01T12:00:00', 100, 100), id: 1, key_id: 1 };
    const second = { ...makeConsumption(1, '2026-08-01T12:00:00', 300, 400), id: 2, key_id: 2 };
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([first, second]))
      .mockResolvedValueOnce(response([]));
    vi.mocked(getKeys).mockResolvedValueOnce({ data: [
      { id: 2, name: 'Production', key_value: 'sk-production-two' },
      { id: 1, name: 'Production', key_value: 'sk-production-one' },
    ] } as unknown as Awaited<ReturnType<typeof getKeys>>);

    render(<ActivityOverview range={customRange(
      dayjs('2026-08-01T00:00:00'),
      dayjs('2026-08-02T00:00:00'),
    )} />);

    expect(await screen.findByText('Production (#1)')).not.toBeNull();
    expect(screen.getByText('Production (#2)')).not.toBeNull();
    expect(screen.getByText('sk-productio...one')).not.toBeNull();
    expect(screen.getByText('sk-productio...two')).not.toBeNull();
    expect(screen.getByText('200 tok')).not.toBeNull();
    expect(screen.getByText('700 tok')).not.toBeNull();
  });
});

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

  it('warns when the previous-period response is truncated', async () => {
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption(1, '2026-08-01T12:00:00')]))
      .mockResolvedValueOnce(response([], { 'x-consumptions-truncated': 'true' }));

    render(<ActivityOverview range={customRange(
      dayjs('2026-08-01T00:00:00'),
      dayjs('2026-08-02T00:00:00'),
    )} />);

    expect(await screen.findByText(/activity data is incomplete/i)).not.toBeNull();
  });
});

describe('ActivityOverview empty state', () => {
  it('shows the empty state for charts when the successful response has no usage', async () => {
    vi.mocked(getConsumptions).mockResolvedValue(response([]));

    render(
      <ActivityOverview
        range={customRange(
          dayjs('2026-08-01T00:00:00'),
          dayjs('2026-08-02T00:00:00'),
        )}
      />,
    );

    await waitFor(() => expect(screen.getAllByText('No usage in this period.')).toHaveLength(6));
    expect(screen.queryAllByText('Other')).toHaveLength(0);
    expect(screen.queryAllByText('Prompt')).toHaveLength(0);
    expect(screen.queryAllByText('Completion')).toHaveLength(0);
    expect(screen.queryAllByText('Cached')).toHaveLength(0);
    expect(screen.queryAllByText('Uncached')).toHaveLength(0);
  });

  it('shows the empty state consistently when the only response row starts at the exclusive end', async () => {
    const until = dayjs('2026-08-02T00:00:00');
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption(100, until.format('YYYY-MM-DDTHH:mm:ss'))]))
      .mockResolvedValueOnce(response([]));

    render(
      <ActivityOverview
        range={customRange(dayjs('2026-08-01T00:00:00'), until)}
      />,
    );

    await waitFor(() => expect(screen.getAllByText('No usage in this period.')).toHaveLength(6));
    expect(screen.queryByText('Key #1')).toBeNull();
    expect(screen.queryByText('app')).toBeNull();
    expect(screen.queryAllByText('Other')).toHaveLength(0);
    expect(screen.queryAllByText('Prompt')).toHaveLength(0);
    expect(screen.queryAllByText('Completion')).toHaveLength(0);
    expect(screen.queryAllByText('Cached')).toHaveLength(0);
    expect(screen.queryAllByText('Uncached')).toHaveLength(0);
  });

  it('uses a token-specific empty state when in-range usage has no tokens', async () => {
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption(100, '2026-08-01T12:00:00', 0, 0)]))
      .mockResolvedValueOnce(response([]));

    render(
      <ActivityOverview
        range={customRange(
          dayjs('2026-08-01T00:00:00'),
          dayjs('2026-08-02T00:00:00'),
        )}
      />,
    );

    await waitFor(() => expect(screen.getAllByText('No usage in this period.')).toHaveLength(4));
    expect(screen.queryAllByText('Prompt')).toHaveLength(0);
    expect(screen.queryAllByText('Completion')).toHaveLength(0);
    expect(screen.queryAllByText('Cached')).toHaveLength(0);
    expect(screen.queryAllByText('Uncached')).toHaveLength(0);
  });

  it('omits zero-share model groups before selecting chart legends', async () => {
    const until = dayjs('2026-08-02T00:00:00');
    const inRange = { ...makeConsumption(100, '2026-08-01T12:00:00'), model_name: 'in-range-model' };
    const atExclusiveEnd = { ...makeConsumption(100, until.format('YYYY-MM-DDTHH:mm:ss')), model_name: 'boundary-model' };
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([inRange, atExclusiveEnd]))
      .mockResolvedValueOnce(response([]));

    render(
      <ActivityOverview
        range={customRange(dayjs('2026-08-01T00:00:00'), until)}
      />,
    );

    await waitFor(() => expect(screen.getAllByRole('group', { name: 'in-range-model' })).toHaveLength(2));
    expect(screen.queryAllByRole('group', { name: 'boundary-model' })).toHaveLength(0);
  });

  it('reads reserved model keys and folds chart tails into Other', async () => {
    const names = ['label', 'sort', 'top-3', 'top-4', 'top-5', '__proto__', 'constructor', 'dotted.name'];
    const rows = names.map((model_name, i) => ({
      ...makeConsumption(100 - i * 10, '2026-08-01T12:00:00'),
      model_name,
      request_count: i + 1,
    }));
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response(rows))
      .mockResolvedValueOnce(response([]));

    render(<ActivityOverview range={customRange(dayjs('2026-08-01T00:00:00'), dayjs('2026-08-02T00:00:00'))} />);
    await waitFor(() => expect(screen.getAllByTestId('activity-overview-series')).toHaveLength(16));

    const valuesFor = (name: string) => screen.getAllByTestId('activity-overview-series')
      .filter(element => element.getAttribute('data-name') === name)
      .map(element => (JSON.parse(element.getAttribute('data-values')!) as number[])
        .reduce((sum, value) => sum + value, 0));
    expect(valuesFor('label')).toEqual([100]);
    expect(valuesFor('sort')).toEqual([90]);
    expect(valuesFor('Other')).toEqual([120, 6]);
  });
});
