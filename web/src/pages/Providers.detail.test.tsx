// @vitest-environment jsdom
import React from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Providers from './Providers';
import { getKeyDetail, getKeys, getProviders, getRoutes } from '../api/client';
import { subscribeEvents } from '../api/events';
import type { Key, Provider } from '../api/client';

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return {
    ...actual,
    getKeyDetail: vi.fn(),
    getKeys: vi.fn(),
    getProviders: vi.fn(),
    getRoutes: vi.fn(),
  };
});

vi.mock('../api/events', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/events')>();
  return { ...actual, subscribeEvents: vi.fn(() => () => {}) };
});

if (typeof window.matchMedia !== 'function') {
  window.matchMedia = (query: string) => ({
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
if (typeof globalThis.ResizeObserver !== 'function') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

const provider: Provider = {
  id: 1,
  name: 'Provider One',
  type: 'openai',
  base_url: 'https://example.test',
  extra_headers: '',
  created_at: '',
  updated_at: '',
};

const key: Key = {
  id: 10,
  provider_id: provider.id,
  name: 'Key One',
  key_value: 'sk-one',
  status: 'active',
  recovery_strategy: 'lazy',
  sort_order: 0,
  total_spent: 0,
  total_spend_limit: 0,
  rpm_limit: 0,
  tpm_limit: 0,
  rp5h_limit: 0,
  rp5h_metric: 'requests',
  rpd_limit: 0,
  rpd_metric: 'requests',
  rpw_limit: 0,
  rpw_metric: 'requests',
  rpm_month_limit: 0,
  rpm_metric: 'requests',
} as Key;
const otherKey: Key = { ...key, id: 11, name: 'Key Two', key_value: 'sk-two', sort_order: 1 };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function detail(totalCost: number, detailKey: Key = key) {
  return {
    key: { ...detailKey, provider },
    counts: {},
    consumptions: [],
    total_cost: totalCost,
  };
}

beforeEach(() => {
  vi.mocked(subscribeEvents).mockClear();
  vi.mocked(getProviders).mockResolvedValue({ data: [provider] } as any);
  vi.mocked(getKeys).mockResolvedValue({ data: [key, otherKey] } as any);
  vi.mocked(getRoutes).mockResolvedValue({ data: [] } as any);
  vi.mocked(getKeyDetail).mockReset();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('Providers key detail refresh', () => {
  it('applies a completed response before one queued refresh resolves', async () => {
    const inFlight = deferred<{ data: ReturnType<typeof detail> }>();
    const trailing = deferred<{ data: ReturnType<typeof detail> }>();
    vi.mocked(getKeyDetail)
      .mockResolvedValueOnce({ data: detail(1) } as any)
      .mockImplementationOnce(() => inFlight.promise as any)
      .mockImplementationOnce(() => trailing.promise as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const collapseHeader = container.querySelector('.ant-collapse-header');
    if (!collapseHeader) throw new Error('provider header not found');
    fireEvent.click(collapseHeader);
    await screen.findByText('Key One');

    fireEvent.click(container.querySelector('button[title="Detail"]')!);
    await screen.findByText('$1.000000');

    const listener = vi.mocked(subscribeEvents).mock.calls[0]?.[0];
    if (!listener) throw new Error('SSE listener was not registered');
    act(() => listener({ type: 'key_status_changed', key_id: key.id }));
    expect(getKeyDetail).toHaveBeenCalledTimes(2);
    act(() => {
      listener({ type: 'key_status_changed', key_id: key.id });
      listener({ type: 'key_status_changed', key_id: key.id });
    });
    expect(getKeyDetail).toHaveBeenCalledTimes(2);

    await act(async () => {
      inFlight.resolve({ data: detail(2) });
      await inFlight.promise;
    });

    expect(await screen.findByText('$2.000000')).toBeTruthy();
    await waitFor(() => expect(getKeyDetail).toHaveBeenCalledTimes(3));
    expect(screen.queryByText('$1.000000')).toBeNull();

    await act(async () => {
      trailing.resolve({ data: detail(3) });
      await trailing.promise;
    });
    expect(await screen.findByText('$3.000000')).toBeTruthy();
  }, 15000);

  it('discards an old key response after switching detail keys', async () => {
    const oldKeyRefresh = deferred<{ data: ReturnType<typeof detail> }>();
    vi.mocked(getKeyDetail)
      .mockResolvedValueOnce({ data: detail(1) } as any)
      .mockImplementationOnce(() => oldKeyRefresh.promise as any)
      .mockResolvedValueOnce({ data: detail(2, otherKey) } as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const collapseHeader = container.querySelector('.ant-collapse-header');
    if (!collapseHeader) throw new Error('provider header not found');
    fireEvent.click(collapseHeader);
    await screen.findByText('Key One');
    await screen.findByText('Key Two');

    const detailButtons = container.querySelectorAll('button[title="Detail"]');
    fireEvent.click(detailButtons[0]);
    await screen.findByText('$1.000000');

    const listener = vi.mocked(subscribeEvents).mock.calls[0]?.[0];
    if (!listener) throw new Error('SSE listener was not registered');
    act(() => {
      listener({ type: 'key_status_changed', key_id: key.id });
      listener({ type: 'key_status_changed', key_id: key.id });
    });
    expect(getKeyDetail).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    fireEvent.click(container.querySelectorAll('button[title="Detail"]')[1]);
    expect(await screen.findByText('$2.000000')).toBeTruthy();

    await act(async () => {
      oldKeyRefresh.resolve({ data: detail(9) });
      await oldKeyRefresh.promise;
    });

    expect(getKeyDetail).toHaveBeenCalledTimes(3);
    expect(screen.getByText('$2.000000')).toBeTruthy();
    expect(screen.queryByText('$9.000000')).toBeNull();
  }, 15000);

  it('discards a prior modal session when reopening the same key', async () => {
    const oldRefresh = deferred<{ data: ReturnType<typeof detail> }>();
    const currentRefresh = deferred<{ data: ReturnType<typeof detail> }>();
    const trailing = deferred<{ data: ReturnType<typeof detail> }>();
    vi.mocked(getKeyDetail)
      .mockResolvedValueOnce({ data: detail(1) } as any)
      .mockImplementationOnce(() => oldRefresh.promise as any)
      .mockResolvedValueOnce({ data: detail(3) } as any)
      .mockImplementationOnce(() => currentRefresh.promise as any)
      .mockImplementationOnce(() => trailing.promise as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const collapseHeader = container.querySelector('.ant-collapse-header');
    if (!collapseHeader) throw new Error('provider header not found');
    fireEvent.click(collapseHeader);
    await screen.findByText('Key One');

    const detailButton = container.querySelector('button[title="Detail"]')!;
    fireEvent.click(detailButton);
    await screen.findByText('$1.000000');
    const listener = vi.mocked(subscribeEvents).mock.calls[0]?.[0];
    if (!listener) throw new Error('SSE listener was not registered');
    act(() => {
      listener({ type: 'key_status_changed', key_id: key.id });
      listener({ type: 'key_status_changed', key_id: key.id });
    });
    expect(getKeyDetail).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    fireEvent.click(detailButton);
    expect(await screen.findByText('$3.000000')).toBeTruthy();

    act(() => listener({ type: 'key_status_changed', key_id: key.id }));
    expect(getKeyDetail).toHaveBeenCalledTimes(4);
    act(() => listener({ type: 'key_status_changed', key_id: key.id }));

    await act(async () => {
      oldRefresh.reject(new Error('stale request failed'));
      await oldRefresh.promise.catch(() => {});
    });
    expect(getKeyDetail).toHaveBeenCalledTimes(4);
    expect(screen.getByText('$3.000000')).toBeTruthy();

    await act(async () => {
      currentRefresh.resolve({ data: detail(4) });
      await currentRefresh.promise;
    });
    expect(getKeyDetail).toHaveBeenCalledTimes(5);
    expect(screen.getByText('$4.000000')).toBeTruthy();

    await act(async () => {
      trailing.resolve({ data: detail(5) });
      await trailing.promise;
    });
    expect(screen.queryByText('$9.000000')).toBeNull();
    expect(await screen.findByText('$5.000000')).toBeTruthy();
    expect(screen.queryByText('$3.000000')).toBeNull();
  }, 15000);

  it('does not apply a response or run queued work after the detail modal closes', async () => {
    const inFlight = deferred<{ data: ReturnType<typeof detail> }>();
    vi.mocked(getKeyDetail)
      .mockResolvedValueOnce({ data: detail(1) } as any)
      .mockImplementationOnce(() => inFlight.promise as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const collapseHeader = container.querySelector('.ant-collapse-header');
    if (!collapseHeader) throw new Error('provider header not found');
    fireEvent.click(collapseHeader);
    await screen.findByText('Key One');

    fireEvent.click(container.querySelector('button[title="Detail"]')!);
    await screen.findByText('$1.000000');
    const listener = vi.mocked(subscribeEvents).mock.calls[0]?.[0];
    if (!listener) throw new Error('SSE listener was not registered');
    act(() => {
      listener({ type: 'key_status_changed', key_id: key.id });
      listener({ type: 'key_status_changed', key_id: key.id });
    });
    expect(getKeyDetail).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    await act(async () => {
      inFlight.resolve({ data: detail(9) });
      await inFlight.promise;
    });

    expect(getKeyDetail).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('$9.000000')).toBeNull();
  }, 15000);
});
