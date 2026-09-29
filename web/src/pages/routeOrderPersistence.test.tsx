// @vitest-environment jsdom
import React from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Models from './Models';
import RoutesPage from './RoutesPage';
import { getModelGroups, getProviders, getRoutes, observeRouteOrderVersion, reorderRoutes, updateRoute } from '../api/client';
import type { ModelGroup, Provider, Route } from '../api/client';
import { persistRouteOrder, routeOrderPersistence } from '../api/routeOrder';

vi.mock('../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return {
    ...actual,
    getModelGroups: vi.fn(),
    getProviders: vi.fn(),
    getRoutes: vi.fn(),
    reorderRoutes: vi.fn(),
    updateRoute: vi.fn(),
  };
});

if (typeof window.matchMedia !== 'function') {
  window.matchMedia = () => ({
    matches: false,
    media: '',
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
const group: ModelGroup = {
  id: 100,
  group_id: 'group-one',
  name: 'Group One',
  enabled: true,
  retry_times: 0,
  context_length: 0,
  max_output_tokens: 0,
  created_at: '',
  updated_at: '',
};
const routes: Route[] = [301, 302, 303].map((id, index) => ({
  id,
  model_group_id: group.id,
  provider_id: provider.id,
  target_model: `model-${id}`,
  priority: index,
  weight: 0,
  enabled: true,
  prompt_per_1m: 0,
  completion_per_1m: 0,
  cache_read_per_1m: 0,
  cache_write_per_1m: 0,
  extra_params: '',
  created_at: '',
  updated_at: '',
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function routeRows(container: HTMLElement) {
  return Array.from(container.querySelectorAll('tr[data-row-key]'))
    .filter(row => [301, 302, 303].includes(Number(row.getAttribute('data-row-key'))));
}

function pointerEvent(type: string, pointerId: number, clientY: number) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperties(event, {
    pointerId: { value: pointerId },
    clientY: { value: clientY },
  });
  return event;
}

function setRect(element: Element, top: number, height: number) {
  const rect = {
    x: 0,
    y: top,
    top,
    bottom: top + height,
    left: 0,
    right: 600,
    width: 600,
    height,
    toJSON: () => ({}),
  };
  Object.defineProperty(element, 'getBoundingClientRect', {
    configurable: true,
    value: () => rect,
  });
}

function setDragGeometry(rows: Element[]) {
  rows.forEach((row, index) => setRect(row, 100 + index * 40, 40));
  if (rows[0]?.parentElement) setRect(rows[0].parentElement, 100, rows.length * 40);
}

function startModelsRouteDrop(container: HTMLElement) {
  const rows = routeRows(container);
  const firstRow = rows[0] as HTMLElement | undefined;
  const handle = firstRow?.querySelector('[data-drag-handle]');
  if (!firstRow || !handle) throw new Error('model route drag handle not found');
  setDragGeometry(rows);
  fireEvent(handle, pointerEvent('pointerdown', 1, 110));
  fireEvent(firstRow, pointerEvent('pointermove', 1, 190));
  fireEvent(firstRow, pointerEvent('pointerup', 1, 190));
}

async function dropModelsRoute(container: HTMLElement) {
  startModelsRouteDrop(container);
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });
}

function nativeDragEvent(type: string) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, 'dataTransfer', {
    value: { effectAllowed: '', dropEffect: '', setData: vi.fn() },
  });
  return event;
}

async function dropRoutesPageRoute(container: HTMLElement) {
  const rows = routeRows(container);
  if (rows.length !== 3) throw new Error(`expected 3 route rows, got ${rows.length}`);
  fireEvent(rows[0], nativeDragEvent('dragstart'));
  fireEvent(rows[2], nativeDragEvent('dragover'));
  fireEvent(rows[2], nativeDragEvent('drop'));
}

function newerVersion(candidate: { timestamp: number; client_id: string; sequence: number }, current: { timestamp: number; client_id: string; sequence: number }) {
  if (candidate.client_id === current.client_id) return candidate.sequence > current.sequence;
  if (candidate.timestamp !== current.timestamp) return candidate.timestamp > current.timestamp;
  return candidate.client_id > current.client_id;
}

async function verifyImmediateDrops(drop: () => Promise<void>, container: HTMLElement) {
  let serverOrder = routes.map(route => route.id);
  let serverVersion: { timestamp: number; client_id: string; sequence: number } | undefined;
  const firstWrite = deferred<void>();
  const secondWrite = deferred<void>();
  const applyPayload = (
    payload: { id: number; priority: number }[],
    version?: { timestamp: number; client_id: string; sequence: number },
  ) => {
    if (!version) throw new Error('route order version was not sent');
    if (serverVersion && !newerVersion(version, serverVersion)) return;
    serverVersion = version;
    serverOrder = [...payload].sort((a, b) => a.priority - b.priority).map(route => route.id);
  };
  vi.mocked(reorderRoutes)
    .mockImplementationOnce(async (payload, version) => {
      await firstWrite.promise;
      applyPayload(payload, version);
      return { data: undefined } as any;
    })
    .mockImplementation(async (payload, version) => {
      applyPayload(payload, version);
      await secondWrite.promise;
      return { data: undefined } as any;
    });

  await drop();
  await waitFor(() => expect(reorderRoutes).toHaveBeenCalledTimes(1));
  await drop();
  expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
  await waitFor(() => expect(reorderRoutes).toHaveBeenCalledTimes(2));
  expect(serverOrder).toEqual([303, 301, 302]);
  const versions = vi.mocked(reorderRoutes).mock.calls.map(([, version]) => version);
  if (!versions[0] || !versions[1]) throw new Error('route order versions were not sent');
  expect(versions[0].client_id).toBe(versions[1].client_id);
  expect(versions[1].sequence).toBeGreaterThan(versions[0].sequence);
  expect(sendBeaconMock).not.toHaveBeenCalled();

  window.dispatchEvent(new Event('pagehide'));
  expect(sendBeaconMock).toHaveBeenCalledTimes(1);
  const [beaconURL, beaconBody] = sendBeaconMock.mock.calls[0];
  expect(beaconURL).toBe('/api/routes/reorder');
  const beaconPayload = JSON.parse(await (beaconBody as Blob).text());
  expect(beaconPayload.routes.map((route: { id: number }) => route.id)).toEqual([303, 301, 302]);
  expect(beaconPayload.order_version).toEqual(versions[1]);

  firstWrite.resolve();
  secondWrite.resolve();
  await waitFor(() => expect(routeOrderPersistence.pending).toBe(0));
  expect(vi.mocked(reorderRoutes).mock.calls.map(([payload]) => payload.map(route => route.id))).toEqual([
    [302, 303, 301],
    [303, 301, 302],
  ]);
  expect(serverOrder).toEqual([303, 301, 302]);
}

let originalSendBeacon: PropertyDescriptor | undefined;
let sendBeaconMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  originalSendBeacon = Object.getOwnPropertyDescriptor(navigator, 'sendBeacon');
  sendBeaconMock = vi.fn(() => true);
  Object.defineProperty(navigator, 'sendBeacon', { configurable: true, value: sendBeaconMock });
  vi.mocked(getModelGroups).mockResolvedValue({ data: [group] } as any);
  vi.mocked(getProviders).mockResolvedValue({ data: [provider] } as any);
  vi.mocked(getRoutes).mockResolvedValue({ data: routes.map(route => ({ ...route })) } as any);
  vi.mocked(updateRoute).mockReset().mockResolvedValue({ data: routes[0] } as any);
  vi.mocked(reorderRoutes).mockReset();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
  if (originalSendBeacon) Object.defineProperty(navigator, 'sendBeacon', originalSendBeacon);
  else Reflect.deleteProperty(navigator, 'sendBeacon');
});

describe('route reorder persistence', () => {
  it('submits later Models drops immediately and preserves them over stale completion', async () => {
    const { container } = render(<Models />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('model group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    await verifyImmediateDrops(() => dropModelsRoute(container), container);
  }, 15000);

  it('submits later Routes drops immediately and preserves them over stale completion', async () => {
    const { container } = render(<RoutesPage />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('route group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    await verifyImmediateDrops(() => dropRoutesPageRoute(container), container);
  }, 15000);

  it('persists a Models drop before the settle timer can be cleared on unmount', async () => {
    const pendingWrite = deferred<void>();
    vi.mocked(reorderRoutes).mockImplementation(async () => {
      await pendingWrite.promise;
      return { data: { status: 'ok' } } as any;
    });
    const { container, unmount } = render(<Models />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('model group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    startModelsRouteDrop(container);
    expect(reorderRoutes).toHaveBeenCalledTimes(1);
    unmount();
    window.dispatchEvent(new Event('pagehide'));

    expect(sendBeaconMock).toHaveBeenCalledTimes(1);
    const [, beaconBody] = sendBeaconMock.mock.calls[0];
    const beaconPayload = JSON.parse(await (beaconBody as Blob).text());
    expect(beaconPayload.routes.map((route: { id: number }) => route.id)).toEqual([302, 303, 301]);
    expect(beaconPayload.order_version).toEqual(vi.mocked(reorderRoutes).mock.calls[0][1]);

    pendingWrite.resolve();
    await waitFor(() => expect(routeOrderPersistence.pending).toBe(0));
  }, 15000);

  it('does not beacon a payload after its write failed', async () => {
    vi.mocked(reorderRoutes).mockRejectedValueOnce({ response: { status: 500 } });

    await expect(persistRouteOrder([{ id: 301, priority: 0 }])).rejects.toMatchObject({ response: { status: 500 } });
    window.dispatchEvent(new Event('pagehide'));

    expect(sendBeaconMock).not.toHaveBeenCalled();
    expect(routeOrderPersistence.pending).toBe(0);
  });

  it('falls back to an earlier active order when the latest write fails', async () => {
    const earlierWrite = deferred<any>();
    vi.mocked(reorderRoutes)
      .mockImplementationOnce(() => earlierWrite.promise)
      .mockRejectedValueOnce({ response: { status: 500 } });

    const earlier = persistRouteOrder([{ id: 301, priority: 0 }]);
    await expect(persistRouteOrder([{ id: 302, priority: 0 }])).rejects.toMatchObject({ response: { status: 500 } });
    expect(routeOrderPersistence.pending).toBe(1);
    window.dispatchEvent(new Event('pagehide'));

    expect(sendBeaconMock).toHaveBeenCalledTimes(1);
    const [, beaconBody] = sendBeaconMock.mock.calls[0];
    expect(JSON.parse(await (beaconBody as Blob).text()).routes).toEqual([{ id: 301, priority: 0 }]);

    earlierWrite.resolve({ data: { status: 'ok' } });
    await expect(earlier).resolves.toBe(true);
    expect(routeOrderPersistence.pending).toBe(0);
  });

  it('keeps the server order when a fast conflict refetch precedes Models settle', async () => {
    const latestRoutes = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockResolvedValueOnce({ data: latestRoutes } as any);
    vi.mocked(reorderRoutes).mockRejectedValueOnce({ response: { status: 409 } });

    const { container } = render(<Models />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('model group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    startModelsRouteDrop(container);
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
    });
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });

    expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
  }, 15000);

  it('does not restore a successful Models drop over a newer fetched order during settle', async () => {
    const newerRoutes = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockResolvedValueOnce({ data: newerRoutes } as any);
    vi.mocked(reorderRoutes).mockResolvedValue({ data: { status: 'ok' } } as any);
    const intervalSpy = vi.spyOn(globalThis, 'setInterval');

    const { container } = render(<Models />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('model group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    const poll = intervalSpy.mock.calls.find(([, interval]) => interval === 10000)?.[0];
    if (typeof poll !== 'function') throw new Error('Models polling callback not found');
    startModelsRouteDrop(container);
    await waitFor(() => expect(routeOrderPersistence.pending).toBe(0));
    await act(async () => { poll(); await Promise.resolve(); });
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
    });
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });

    expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
  }, 15000);

  it.each(['Models', 'RoutesPage'])('reloads %s routes when a pending write settles after mount', async page => {
    const pendingWrite = deferred<any>();
    const latestRoutes = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(reorderRoutes).mockImplementationOnce(() => pendingWrite.promise);
    const write = persistRouteOrder([{ id: 303, priority: 0 }]);
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockResolvedValueOnce({ data: latestRoutes } as any);

    const { container } = page === 'Models' ? render(<Models />) : render(<RoutesPage />);
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(1));
    pendingWrite.resolve({ data: { status: 'ok' } });
    await expect(write).resolves.toBe(true);

    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('route group header not found');
    fireEvent.click(header);
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
    });
  }, 15000);

  it('refreshes Models after a newer failed drop while the earlier drop is pending', async () => {
    const earlierWrite = deferred<any>();
    const laterWrite = deferred<any>();
    const earlierOrder = [routes[1], routes[2], routes[0]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockResolvedValueOnce({ data: earlierOrder } as any);
    vi.mocked(reorderRoutes)
      .mockImplementationOnce(() => earlierWrite.promise)
      .mockImplementationOnce(() => laterWrite.promise);

    const { container } = render(<Models />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('model group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    await dropModelsRoute(container);
    startModelsRouteDrop(container);
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });
    expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);

    laterWrite.reject({ response: { status: 500 } });
    await waitFor(() => expect(routeOrderPersistence.pending).toBe(1));
    expect(getRoutes).toHaveBeenCalledTimes(1);
    earlierWrite.resolve({ data: { status: 'ok' } });

    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([302, 303, 301]);
    });
  }, 15000);

  it.each(['Models', 'RoutesPage'])('refreshes %s after a cross-tab stale conflict', async page => {
    const latestRoutes = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockRejectedValueOnce(new Error('temporary route read failure'))
      .mockResolvedValueOnce({ data: latestRoutes } as any);
    vi.mocked(reorderRoutes).mockRejectedValueOnce({
      response: { status: 409, headers: { 'x-route-order-version': JSON.stringify({ timestamp: 999, client_id: 'other-tab', sequence: 1 }) } },
    });

    const { container } = page === 'Models' ? render(<Models />) : render(<RoutesPage />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('route group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    if (page === 'Models') await dropModelsRoute(container);
    else await dropRoutesPageRoute(container);

    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(3));
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
    });
    expect(routeOrderPersistence.pending).toBe(0);
  }, 15000);

  it('keeps retrying RoutesPage reconciliation after both conflict reads fail', async () => {
    const latestRoutes = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockRejectedValueOnce(new Error('route read unavailable'))
      .mockRejectedValueOnce(new Error('route read still unavailable'))
      .mockResolvedValueOnce({ data: latestRoutes } as any);
    vi.mocked(reorderRoutes).mockRejectedValueOnce({ response: { status: 409 } });

    const { container } = render(<RoutesPage />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('route group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    await dropRoutesPageRoute(container);
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(4), { timeout: 7000 });
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
    });
  }, 15000);

  it.each(['Models', 'RoutesPage'])('does not apply a pre-drop CRUD fetch over a newer %s order', async page => {
    const staleFetch = deferred<any>();
    const latestRoutes = [routes[1], routes[2], routes[0]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockReturnValueOnce(staleFetch.promise)
      .mockResolvedValueOnce({ data: latestRoutes } as any);
    vi.mocked(updateRoute).mockResolvedValue({ data: routes[0] } as any);
    vi.mocked(reorderRoutes).mockResolvedValue({ data: { status: 'ok' } } as any);

    const { container } = page === 'Models' ? render(<Models />) : render(<RoutesPage />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('route group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');
    const editButton = container.querySelector('tr[data-row-key="301"] button[title="Edit"]');
    if (!editButton) throw new Error('route edit button not found');
    fireEvent.click(editButton);
    await screen.findByText('Edit Route');
    fireEvent.click(screen.getByRole('button', { name: 'OK' }));
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));

    if (page === 'Models') await dropModelsRoute(container);
    else await dropRoutesPageRoute(container);
    await waitFor(() => expect(reorderRoutes).toHaveBeenCalledTimes(1));
    staleFetch.resolve({ data: routes.map(route => ({ ...route })) });

    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(3));
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([302, 303, 301]);
    });
  }, 15000);

  it('does not let a delayed conflict refresh overwrite a newer RoutesPage fetch', async () => {
    const conflictRefresh = deferred<any>();
    const conflictOrder = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    const latestOrder = [routes[1], routes[2], routes[0]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockReturnValueOnce(conflictRefresh.promise)
      .mockResolvedValueOnce({ data: latestOrder } as any);
    vi.mocked(updateRoute).mockResolvedValue({ data: routes[0] } as any);
    vi.mocked(reorderRoutes).mockRejectedValueOnce({ response: { status: 409 } });

    const { container } = render(<RoutesPage />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('route group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');

    await dropRoutesPageRoute(container);
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));
    const editButton = container.querySelector('tr[data-row-key="301"] button[title="Edit"]');
    if (!editButton) throw new Error('route edit button not found');
    fireEvent.click(editButton);
    await screen.findByText('Edit Route');
    fireEvent.click(screen.getByRole('button', { name: 'OK' }));

    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(3));
    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([302, 303, 301]);
    });
    conflictRefresh.resolve({ data: conflictOrder });
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });

    expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([302, 303, 301]);
  }, 15000);

  it('applies a successful Models CRUD fetch after a later poll fails', async () => {
    const crudFetch = deferred<any>();
    const crudOrder = [routes[2], routes[0], routes[1]].map((route, priority) => ({ ...route, priority }));
    vi.mocked(getRoutes).mockReset()
      .mockResolvedValueOnce({ data: routes.map(route => ({ ...route })) } as any)
      .mockReturnValueOnce(crudFetch.promise)
      .mockRejectedValueOnce(new Error('poll route read failed'));
    vi.mocked(updateRoute).mockResolvedValue({ data: routes[0] } as any);
    const intervalSpy = vi.spyOn(globalThis, 'setInterval');

    const { container } = render(<Models />);
    await screen.findByText('Group One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('model group header not found');
    fireEvent.click(header);
    await screen.findByText('model-301');
    const editButton = container.querySelector('tr[data-row-key="301"] button[title="Edit"]');
    if (!editButton) throw new Error('route edit button not found');
    fireEvent.click(editButton);
    await screen.findByText('Edit Route');
    fireEvent.click(screen.getByRole('button', { name: 'OK' }));
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(2));

    const poll = intervalSpy.mock.calls.find(([, interval]) => interval === 10000)?.[0];
    if (typeof poll !== 'function') throw new Error('Models polling callback not found');
    await act(async () => { poll(); await Promise.resolve(); });
    await waitFor(() => expect(getRoutes).toHaveBeenCalledTimes(3));
    crudFetch.resolve({ data: crudOrder });

    await waitFor(() => {
      expect(routeRows(container).map(row => Number(row.getAttribute('data-row-key')))).toEqual([303, 301, 302]);
    });
  }, 15000);

  it('starts a new client above a persisted version after local clock rollback', async () => {
    const serverFloor = Date.now() + 60000;
    observeRouteOrderVersion({ timestamp: serverFloor, client_id: 'before-clock-change', sequence: 8 });
    vi.mocked(reorderRoutes).mockResolvedValue({ data: { status: 'ok' } } as any);

    await persistRouteOrder([{ id: 301, priority: 0 }]);

    const version = vi.mocked(reorderRoutes).mock.calls[0][1];
    if (!version) throw new Error('route order version was not sent');
    expect(version.timestamp).toBeGreaterThan(serverFloor);
  });
});
