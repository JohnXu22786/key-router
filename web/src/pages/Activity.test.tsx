// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/react';
import dayjs from 'dayjs';
import Activity from './Activity';
import { getConsumptions, getKeys } from '../api/client';
import type { Consumption } from '../api/client';

vi.mock('./ActivityOverview', () => ({ default: () => <div /> }));
vi.mock('./ActivityTrends', () => ({ default: () => <div /> }));
vi.mock('./ActivityExplore', () => ({ default: () => <div /> }));

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return { ...actual, getConsumptions: vi.fn(), getKeys: vi.fn() };
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
if (typeof (globalThis as { ResizeObserver?: unknown }).ResizeObserver !== 'function') {
  (globalThis as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

const makeConsumption = (model_name: string, hour_bucket: string): Consumption => ({
  id: 1,
  key_id: 1,
  hour_bucket,
  model_name,
  app_name: 'app',
  request_count: 1,
  input_tokens: 100,
  output_tokens: 100,
  cache_hit_tokens: 0,
  cache_write_tokens: 0,
  cost_usd: 1,
});

const response = (rows: Consumption[], headers: Record<string, string> = {}) => ({
  data: rows,
  headers: {
    ...headers,
    get: (name: string) => headers[name.toLowerCase()],
  },
} as unknown as Awaited<ReturnType<typeof getConsumptions>>);

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(dayjs('2026-08-13T16:00:00').toDate());
  vi.mocked(getConsumptions).mockResolvedValue(response([
    makeConsumption('in-range-model', '2026-08-13T15:00:00'),
    makeConsumption('boundary-model', '2026-08-13T16:00:00'),
  ]));
  vi.mocked(getKeys).mockResolvedValue({ data: [] } as unknown as Awaited<ReturnType<typeof getKeys>>);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('Activity filter candidates', () => {
  it('excludes a model found only in the exclusive end bucket', async () => {
    render(<Activity />);

    fireEvent.click(screen.getByRole('button', { name: /filter filter/i }));
    await waitFor(() => expect(getConsumptions).toHaveBeenCalledTimes(1));

    fireEvent.mouseDown(screen.getByRole('combobox'));
    expect((await screen.findAllByText('in-range-model')).length).toBeGreaterThan(0);
    expect(screen.queryByText('boundary-model')).toBeNull();
  });

  it('warns when truncated consumption rows may omit filter candidates', async () => {
    vi.mocked(getConsumptions).mockResolvedValueOnce(response(
      [makeConsumption('returned-model', '2026-08-13T15:00:00')],
      { 'x-consumptions-truncated': 'true' },
    ));

    render(<Activity />);

    fireEvent.click(screen.getByRole('button', { name: /filter filter/i }));
    await waitFor(() => expect(getConsumptions).toHaveBeenCalledTimes(1));

    expect(await screen.findByText(/filter options may be incomplete/i)).not.toBeNull();
    expect(screen.getByText(/narrow the time range/i)).not.toBeNull();

    fireEvent.mouseDown(screen.getByRole('combobox'));
    expect((await screen.findAllByText('returned-model')).length).toBeGreaterThan(0);
  });

  it('refreshes candidates when the active time window changes', async () => {
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption('old-window-model', '2026-08-13T15:00:00')]))
      .mockResolvedValueOnce(response([makeConsumption('new-window-model', '2026-08-13T14:00:00')]));

    render(<Activity />);

    fireEvent.click(screen.getByRole('button', { name: /filter filter/i }));
    await waitFor(() => expect(getConsumptions).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole('button', { name: /^1d/ }));
    fireEvent.click(screen.getByRole('button', { name: /Past 48 Hours/i }));
    await waitFor(() => expect(getConsumptions).toHaveBeenCalledTimes(2));

    fireEvent.mouseDown(screen.getByRole('combobox'));
    expect((await screen.findAllByText('new-window-model')).length).toBeGreaterThan(0);
    expect(screen.queryByText('old-window-model')).toBeNull();
  });

  it('clears stale candidates when a refresh fails', async () => {
    vi.mocked(getConsumptions)
      .mockResolvedValueOnce(response([makeConsumption('stale-model', '2026-08-13T15:00:00')]))
      .mockRejectedValueOnce(new Error('filter options unavailable'));

    render(<Activity />);

    fireEvent.click(screen.getByRole('button', { name: /filter filter/i }));
    await waitFor(() => expect(getConsumptions).toHaveBeenCalledTimes(1));
    fireEvent.mouseDown(screen.getByRole('combobox'));
    expect((await screen.findAllByText('stale-model')).length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole('button', { name: /filter filter/i }));
    fireEvent.click(screen.getByRole('button', { name: /filter filter/i }));
    await waitFor(() => expect(getConsumptions).toHaveBeenCalledTimes(2));

    fireEvent.mouseDown(screen.getByRole('combobox'));
    await waitFor(() => expect(screen.queryAllByText('stale-model')).toHaveLength(0));
  });
});
