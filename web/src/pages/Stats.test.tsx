// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import Stats from './Stats';
import { getConsumptions, getKeys, getOverview, getProviders } from '../api/client';
import type { Consumption, Key, Provider } from '../api/client';

type ConsumptionResponse = Awaited<ReturnType<typeof getConsumptions>>;
type KeysResponse = Awaited<ReturnType<typeof getKeys>>;
type ProvidersResponse = Awaited<ReturnType<typeof getProviders>>;

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return {
    ...actual,
    getConsumptions: vi.fn(),
    getKeys: vi.fn(),
    getOverview: vi.fn(),
    getProviders: vi.fn(),
  };
});

vi.mock('recharts', () => {
  const pass = ({ children }: { children?: ReactNode }) => children ?? null;
  const areaChart = ({ children, data }: { children?: ReactNode; data?: unknown }) => (
    <div data-testid="area-chart" data-series={JSON.stringify(data ?? [])}>{children}</div>
  );
  const nul = () => null;
  return {
    ResponsiveContainer: pass,
    AreaChart: areaChart,
    Area: nul,
    XAxis: nul,
    YAxis: nul,
    CartesianGrid: nul,
    Tooltip: nul,
    BarChart: pass,
    Bar: nul,
  };
});

if (typeof window.matchMedia !== 'function') {
  (window as unknown as { matchMedia: (query: string) => unknown }).matchMedia = (query: string) => ({
    matches: false,
    media: query,
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

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
};

const deferred = <T,>(): Deferred<T> => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};

const response = <T,>(data: T, headers: Record<string, string> = {}) => ({
  data,
  headers: {
    ...headers,
    get: (name: string) => headers[name.toLowerCase()],
  },
}) as unknown as { data: T };

const makeConsumption = (keyId: number, cost: number, hourBucket = '2026-08-13T10:00:00Z'): Consumption => ({
  id: keyId,
  key_id: keyId,
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

const makeKey = (id: number, name: string, providerId: number) => ({
  id,
  name,
  provider_id: providerId,
} as Key);

const makeProvider = (id: number, name: string) => ({ id, name } as Provider);

type Requests = {
  consumptions: Array<Deferred<ConsumptionResponse>>;
  keys: Array<Deferred<KeysResponse>>;
  providers: Array<Deferred<ProvidersResponse>>;
};

let requests: Requests;

beforeEach(() => {
  requests = { consumptions: [], keys: [], providers: [] };
  vi.mocked(getConsumptions).mockReset().mockImplementation(() => {
    const request = deferred<ConsumptionResponse>();
    requests.consumptions.push(request);
    return request.promise;
  });
  vi.mocked(getKeys).mockReset().mockImplementation(() => {
    const request = deferred<KeysResponse>();
    requests.keys.push(request);
    return request.promise;
  });
  vi.mocked(getOverview).mockReset();
  vi.mocked(getProviders).mockReset().mockImplementation(() => {
    const request = deferred<ProvidersResponse>();
    requests.providers.push(request);
    return request.promise;
  });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllEnvs();
});

describe('Stats range request races', () => {
  it('keeps successful stats when the unused overview endpoint fails', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-08-13T12:00:00Z'));
    vi.mocked(getOverview).mockRejectedValue(new Error('overview unavailable'));

    render(<Stats />);
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(2);
      expect(requests.keys).toHaveLength(1);
      expect(requests.providers).toHaveLength(1);
    });

    await act(async () => {
      requests.consumptions[0].resolve(response([makeConsumption(1, 1)]) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[0].resolve(response([makeKey(1, 'stats-key', 1)]) as KeysResponse);
      requests.providers[0].resolve(response([makeProvider(1, 'stats-provider')]) as ProvidersResponse);
    });

    expect(await screen.findByText('stats-key')).not.toBeNull();
    expect(screen.getByText('stats-provider')).not.toBeNull();
    expect(screen.queryByText('Failed to load stats — check the log file.')).toBeNull();
    expect(getOverview).not.toHaveBeenCalled();
  });

  it('keeps an older refresh from overwriting the newly selected range', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-08-13T12:00:00Z'));
    render(<Stats />);
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(2);
      expect(requests.keys).toHaveLength(1);
      expect(requests.providers).toHaveLength(1);
    });

    await act(async () => {
      requests.consumptions[0].resolve(response([makeConsumption(1, 1)]) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[0].resolve(response([makeKey(1, 'initial-key', 1)]) as KeysResponse);
      requests.providers[0].resolve(response([makeProvider(1, 'initial-provider')]) as ProvidersResponse);
    });
    expect(await screen.findByText('initial-key')).not.toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /refresh/i }));
    });
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(4);
      expect(requests.keys).toHaveLength(2);
      expect(requests.providers).toHaveLength(2);
    });

    await act(async () => {
      fireEvent.click(screen.getByText('30 days'));
    });
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(6);
      expect(requests.keys).toHaveLength(3);
      expect(requests.providers).toHaveLength(3);
    });

    // The refresh for the old range completes while the new range is still loading.
    await act(async () => {
      requests.consumptions[2].resolve(response([makeConsumption(2, 99)]) as ConsumptionResponse);
      requests.consumptions[3].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[1].resolve(response([makeKey(99, 'stale-key', 99)]) as KeysResponse);
      requests.providers[1].resolve(response([makeProvider(99, 'stale-provider')]) as ProvidersResponse);
    });

    expect(document.querySelector('.ant-spin-spinning')).not.toBeNull();
    expect(screen.queryByText('stale-key')).toBeNull();
    expect(screen.queryByText('stale-provider')).toBeNull();

    await act(async () => {
      requests.consumptions[4].resolve(response([makeConsumption(3, 3)]) as ConsumptionResponse);
      requests.consumptions[5].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[2].resolve(response([makeKey(3, 'new-key', 3)]) as KeysResponse);
      requests.providers[2].resolve(response([makeProvider(3, 'new-provider')]) as ProvidersResponse);
    });

    expect(await screen.findByText('new-key')).not.toBeNull();
    expect(screen.getByText('new-provider')).not.toBeNull();
    expect(screen.getAllByText('$3.00').length).toBeGreaterThan(0);
    expect(screen.queryByText('stale-key')).toBeNull();
    expect(screen.queryByText('stale-provider')).toBeNull();
    expect(document.querySelector('.ant-spin-spinning')).toBeNull();
  });

  it('clears current and previous data when the newly selected range fails', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-08-13T12:00:00Z'));
    render(<Stats />);
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(2);
      expect(requests.keys).toHaveLength(1);
      expect(requests.providers).toHaveLength(1);
    });

    await act(async () => {
      requests.consumptions[0].resolve(response([makeConsumption(1, 1)]) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([makeConsumption(1, 2, '2026-08-05T10:00:00Z')]) as ConsumptionResponse);
      requests.keys[0].resolve(response([makeKey(1, 'initial-key', 1)]) as KeysResponse);
      requests.providers[0].resolve(response([makeProvider(1, 'initial-provider')]) as ProvidersResponse);
    });
    expect(await screen.findByText('initial-key')).not.toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByText('30 days'));
    });
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(4);
      expect(requests.keys).toHaveLength(2);
      expect(requests.providers).toHaveLength(2);
    });

    await act(async () => {
      requests.consumptions[2].reject(new Error('latest range failed'));
      requests.consumptions[3].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[1].resolve(response([makeKey(1, 'new-range-key', 1)]) as KeysResponse);
      requests.providers[1].resolve(response([makeProvider(1, 'new-range-provider')]) as ProvidersResponse);
    });

    expect(await screen.findByText('Failed to load stats — check the log file.')).not.toBeNull();
    expect(screen.queryByText('initial-key')).toBeNull();
    expect(screen.queryByText('initial-provider')).toBeNull();
    expect(screen.queryByText(/50\.0%/)).toBeNull();
    expect(screen.queryByText(/100\.0%/)).toBeNull();
    expect(screen.getAllByText('$0.00').length).toBeGreaterThan(0);
  });
});

describe('Stats truncated responses', () => {
  const resolveStatsRequests = () => {
    requests.keys[0].resolve(response([makeKey(1, 'stats-key', 1)]) as KeysResponse);
    requests.providers[0].resolve(response([makeProvider(1, 'stats-provider')]) as ProvidersResponse);
  };

  it('warns when the current-period response is truncated', async () => {
    render(<Stats />);
    await waitFor(() => expect(requests.consumptions).toHaveLength(2));

    await act(async () => {
      requests.consumptions[0].resolve(response([makeConsumption(1, 1)], { 'x-consumptions-truncated': 'true' }) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([] as Consumption[]) as ConsumptionResponse);
      resolveStatsRequests();
    });

    expect(await screen.findByText(/activity data is incomplete/i)).not.toBeNull();
    expect(screen.getByText(/narrow the time range or add a filter/i)).not.toBeNull();
  });

  it('warns when the previous-period response is truncated', async () => {
    render(<Stats />);
    await waitFor(() => expect(requests.consumptions).toHaveLength(2));

    await act(async () => {
      requests.consumptions[0].resolve(response([makeConsumption(1, 1)]) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([] as Consumption[], { 'x-consumptions-truncated': 'true' }) as ConsumptionResponse);
      resolveStatsRequests();
    });

    expect(await screen.findByText(/activity data is incomplete/i)).not.toBeNull();
  });
});

describe('Stats Chatham spring-forward aggregation', () => {
  it('keeps clipped 03:45-03:50 usage under the pre-normalization 03:00 bucket', async () => {
    vi.stubEnv('TZ', 'Pacific/Chatham');
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-09-26T14:05:00.000Z')); // 03:50 +13:45

    render(<Stats />);
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(2);
      expect(requests.keys).toHaveLength(1);
      expect(requests.providers).toHaveLength(1);
    });

    await act(async () => {
      requests.consumptions[0].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[0].resolve(response([makeKey(1, 'chatham-key', 1)]) as KeysResponse);
      requests.providers[0].resolve(response([makeProvider(1, 'chatham-provider')]) as ProvidersResponse);
    });

    await act(async () => {
      fireEvent.click(screen.getByText('3 days'));
    });
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(4);
      expect(requests.keys).toHaveLength(2);
      expect(requests.providers).toHaveLength(2);
    });

    await act(async () => {
      requests.consumptions[2].resolve(response([makeConsumption(1, 60, '2026-09-27T04:00:00+13:45')]) as ConsumptionResponse);
      requests.consumptions[3].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[1].resolve(response([makeKey(1, 'chatham-key', 1)]) as KeysResponse);
      requests.providers[1].resolve(response([makeProvider(1, 'chatham-provider')]) as ProvidersResponse);
    });

    const series = JSON.parse((await screen.findByTestId('area-chart')).getAttribute('data-series')!);
    expect(series).toEqual([{
      t: '2026-09-27 03:00',
      cost: 60,
      tokens: 200,
      requests: 1,
      cache: 0,
      label: '09-27 03:00',
    }]);
  });
});
