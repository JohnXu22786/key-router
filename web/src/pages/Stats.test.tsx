// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import Stats from './Stats';
import { getConsumptions, getKeys, getOverview, getProviders } from '../api/client';
import type { Consumption, Key, OverviewStats, Provider } from '../api/client';

type ConsumptionResponse = Awaited<ReturnType<typeof getConsumptions>>;
type KeysResponse = Awaited<ReturnType<typeof getKeys>>;
type OverviewResponse = Awaited<ReturnType<typeof getOverview>>;
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
  const nul = () => null;
  return {
    ResponsiveContainer: pass,
    AreaChart: pass,
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
};

const deferred = <T,>(): Deferred<T> => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(res => { resolve = res; });
  return { promise, resolve };
};

const response = <T,>(data: T) => ({ data }) as unknown as { data: T };

const makeConsumption = (keyId: number, cost: number): Consumption => ({
  id: keyId,
  key_id: keyId,
  hour_bucket: '2026-08-13T10:00:00Z',
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
  overview: Array<Deferred<OverviewResponse>>;
  providers: Array<Deferred<ProvidersResponse>>;
};

let requests: Requests;

beforeEach(() => {
  requests = { consumptions: [], keys: [], overview: [], providers: [] };
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
  vi.mocked(getOverview).mockReset().mockImplementation(() => {
    const request = deferred<OverviewResponse>();
    requests.overview.push(request);
    return request.promise;
  });
  vi.mocked(getProviders).mockReset().mockImplementation(() => {
    const request = deferred<ProvidersResponse>();
    requests.providers.push(request);
    return request.promise;
  });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('Stats range request races', () => {
  it('keeps an older refresh from overwriting the newly selected range', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-08-13T12:00:00Z'));
    render(<Stats />);
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(2);
      expect(requests.keys).toHaveLength(1);
      expect(requests.overview).toHaveLength(1);
      expect(requests.providers).toHaveLength(1);
    });

    await act(async () => {
      requests.consumptions[0].resolve(response([makeConsumption(1, 1)]) as ConsumptionResponse);
      requests.consumptions[1].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[0].resolve(response([makeKey(1, 'initial-key', 1)]) as KeysResponse);
      requests.overview[0].resolve(response({} as OverviewStats) as OverviewResponse);
      requests.providers[0].resolve(response([makeProvider(1, 'initial-provider')]) as ProvidersResponse);
    });
    expect(await screen.findByText('initial-key')).not.toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /refresh/i }));
    });
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(4);
      expect(requests.keys).toHaveLength(2);
      expect(requests.overview).toHaveLength(2);
      expect(requests.providers).toHaveLength(2);
    });

    await act(async () => {
      fireEvent.click(screen.getByText('30 days'));
    });
    await waitFor(() => {
      expect(requests.consumptions).toHaveLength(6);
      expect(requests.keys).toHaveLength(3);
      expect(requests.overview).toHaveLength(3);
      expect(requests.providers).toHaveLength(3);
    });

    // The refresh for the old range completes while the new range is still loading.
    await act(async () => {
      requests.consumptions[2].resolve(response([makeConsumption(2, 99)]) as ConsumptionResponse);
      requests.consumptions[3].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[1].resolve(response([makeKey(99, 'stale-key', 99)]) as KeysResponse);
      requests.overview[1].resolve(response({} as OverviewStats) as OverviewResponse);
      requests.providers[1].resolve(response([makeProvider(99, 'stale-provider')]) as ProvidersResponse);
    });

    expect(document.querySelector('.ant-spin-spinning')).not.toBeNull();
    expect(screen.queryByText('stale-key')).toBeNull();
    expect(screen.queryByText('stale-provider')).toBeNull();

    await act(async () => {
      requests.consumptions[4].resolve(response([makeConsumption(3, 3)]) as ConsumptionResponse);
      requests.consumptions[5].resolve(response([] as Consumption[]) as ConsumptionResponse);
      requests.keys[2].resolve(response([makeKey(3, 'new-key', 3)]) as KeysResponse);
      requests.overview[2].resolve(response({} as OverviewStats) as OverviewResponse);
      requests.providers[2].resolve(response([makeProvider(3, 'new-provider')]) as ProvidersResponse);
    });

    expect(await screen.findByText('new-key')).not.toBeNull();
    expect(screen.getByText('new-provider')).not.toBeNull();
    expect(screen.getAllByText('$3.00').length).toBeGreaterThan(0);
    expect(screen.queryByText('stale-key')).toBeNull();
    expect(screen.queryByText('stale-provider')).toBeNull();
    expect(document.querySelector('.ant-spin-spinning')).toBeNull();
  });
});
