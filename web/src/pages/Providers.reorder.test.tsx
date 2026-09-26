// @vitest-environment jsdom
import React from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Providers from './Providers';
import { getKeys, getProviders, getRoutes, reorderKeys, updateProvider } from '../api/client';
import type { Key, Provider } from '../api/client';

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return {
    ...actual,
    getProviders: vi.fn(),
    getKeys: vi.fn(),
    getRoutes: vi.fn(),
    reorderKeys: vi.fn(),
    updateProvider: vi.fn(),
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

let webLockQueue = Promise.resolve();
let webLockRequestCount = 0;
const provider: Provider = {
  id: 1,
  name: 'Provider One',
  type: 'openai',
  base_url: 'https://example.test',
  extra_headers: '',
  created_at: '',
  updated_at: '',
};

const keys: Key[] = [10, 11, 12].map((id, index) => ({
  id,
  provider_id: 1,
  name: `Key ${id}`,
  key_value: `sk-${id}`,
  status: 'active',
  recovery_strategy: 'lazy',
  sort_order: index,
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
} as Key));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
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

function setDragGeometry(tbody: Element) {
  const rows = Array.from(tbody.querySelectorAll('tr[data-row-key]'));
  rows.forEach((row, index) => setRect(row, 100 + index * 40, 40));
  setRect(tbody, 100, rows.length * 40);
}

function orderKeys(payload: { id: number; sort_order: number }[], current: Key[]) {
  const byId = new Map(current.map(key => [key.id, key]));
  return payload.map(({ id, sort_order }) => ({
    ...byId.get(id)!,
    sort_order,
  }));
}

function rowIds(container: HTMLElement) {
  return Array.from(container.querySelectorAll('tr[data-row-key]'))
    .map(row => Number(row.getAttribute('data-row-key')));
}

function startFirstKeyDragToLast(container: HTMLElement, firstKeyId?: number) {
  const firstRow = firstKeyId == null
    ? container.querySelector('tr[data-row-key]')
    : container.querySelector(`tr[data-row-key="${firstKeyId}"]`);
  const tbody = firstRow?.parentElement;
  if (!firstRow || !tbody) throw new Error('first key row not found');
  setDragGeometry(tbody);
  const handle = firstRow?.querySelector('[data-drag-handle]');
  if (!firstRow || !handle) throw new Error('first key drag handle not found');

  fireEvent(handle, pointerEvent('pointerdown', 1, 110));
  fireEvent(firstRow, pointerEvent('pointermove', 1, 190));
  return firstRow;
}

function dropFirstKeyToLast(container: HTMLElement, firstKeyId?: number) {
  const firstRow = startFirstKeyDragToLast(container, firstKeyId);
  fireEvent(firstRow, pointerEvent('pointerup', 1, 190));
}

async function dragFirstKeyToLast(container: HTMLElement, firstKeyId?: number) {
  dropFirstKeyToLast(container, firstKeyId);
  await act(async () => {
    await new Promise(resolve => setTimeout(resolve, 180));
  });
}

describe('Providers key reorder persistence', () => {
  beforeEach(() => {
    webLockQueue = Promise.resolve();
    webLockRequestCount = 0;
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: {
        request: (_name: string, callback: () => unknown) => {
          webLockRequestCount++;
          const result = webLockQueue.then(callback);
          webLockQueue = result.then(() => undefined, () => undefined);
          return result;
        },
      } as unknown as LockManager,
    });
    vi.mocked(getProviders).mockResolvedValue({ data: [provider] } as any);
    vi.mocked(getKeys).mockResolvedValue({ data: keys } as any);
    vi.mocked(getRoutes).mockResolvedValue({ data: [] } as any);
    vi.mocked(reorderKeys).mockReset();
    vi.mocked(updateProvider).mockReset();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it('queues the newer drop until the first write settles, then persists the latest order', async () => {
    let serverKeys = keys.map(key => ({ ...key }));
    const firstWrite = deferred<any>();
    vi.mocked(reorderKeys)
      .mockImplementationOnce(async payload => {
        await firstWrite.promise;
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      })
      .mockImplementationOnce(async payload => {
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      });
    vi.mocked(getKeys).mockImplementation(async () => ({ data: serverKeys.map(key => ({ ...key })) }) as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const collapseHeader = container.querySelector('.ant-collapse-header');
    if (!collapseHeader) throw new Error('provider collapse header not found');
    fireEvent.click(collapseHeader);
    await screen.findByText('Key 10');
    await screen.findByText('Key 11');
    await screen.findByText('Key 12');

    await dragFirstKeyToLast(container);
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    expect(vi.mocked(reorderKeys).mock.calls[0][0]).toEqual([
      { id: 11, sort_order: 0 },
      { id: 12, sort_order: 1 },
      { id: 10, sort_order: 2 },
    ]);

    await dragFirstKeyToLast(container);
    expect(reorderKeys).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstWrite.resolve({ data: undefined });
      await firstWrite.promise;
    });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(2));
    expect(vi.mocked(reorderKeys).mock.calls[1][0]).toEqual([
      { id: 12, sort_order: 0 },
      { id: 10, sort_order: 1 },
      { id: 11, sort_order: 2 },
    ]);
    await waitFor(async () => {
      const persisted = await getKeys();
      expect(persisted.data.map(key => key.id)).toEqual([12, 10, 11]);
    });
  }, 15000);

  it('registers each same-origin tab drop with Web Locks at drop time', async () => {
    let serverKeys = keys.map(key => ({ ...key }));
    const firstWrite = deferred<any>();
    vi.mocked(getKeys).mockImplementation(async () => ({ data: serverKeys.map(key => ({ ...key })) }) as any);
    vi.mocked(reorderKeys)
      .mockImplementationOnce(async payload => {
        await firstWrite.promise;
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      })
      .mockImplementation(async payload => {
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      });

    const tabA = render(<Providers />);
    await waitFor(() => expect(tabA.container.textContent).toContain('Provider One'));
    const headerA = tabA.container.querySelector('.ant-collapse-header');
    if (!headerA) throw new Error('first tab provider header not found');
    fireEvent.click(headerA);
    await waitFor(() => expect(tabA.container.textContent).toContain('Key 10'));

    const tabB = render(<Providers />);
    await waitFor(() => expect(tabB.container.textContent).toContain('Provider One'));
    const headerB = tabB.container.querySelector('.ant-collapse-header');
    if (!headerB) throw new Error('second tab provider header not found');
    fireEvent.click(headerB);
    await waitFor(() => expect(tabB.container.textContent).toContain('Key 10'));

    await dragFirstKeyToLast(tabA.container);
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    dropFirstKeyToLast(tabA.container);
    expect(webLockRequestCount).toBe(2);
    expect(reorderKeys).toHaveBeenCalledTimes(1);

    dropFirstKeyToLast(tabB.container);
    expect(webLockRequestCount).toBe(3);
    expect(reorderKeys).toHaveBeenCalledTimes(1);
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 180));
    });

    await act(async () => {
      firstWrite.resolve({ data: undefined });
      await firstWrite.promise;
    });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(3));
    expect(vi.mocked(reorderKeys).mock.calls.map(([payload]) => payload.map(key => key.id))).toEqual([
      [11, 12, 10],
      [12, 10, 11],
      [11, 12, 10],
    ]);
    expect(serverKeys.map(key => key.id)).toEqual([11, 12, 10]);
  }, 15000);

  it('continues with a queued drop after an earlier reorder request fails', async () => {
    let serverKeys = keys.map(key => ({ ...key }));
    const firstWrite = deferred<any>();
    vi.mocked(reorderKeys)
      .mockImplementationOnce(async () => {
        await firstWrite.promise;
        throw new Error('temporary server failure');
      })
      .mockImplementationOnce(async payload => {
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      });
    vi.mocked(getKeys).mockImplementation(async () => ({ data: serverKeys.map(key => ({ ...key })) }) as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('provider collapse header not found');
    fireEvent.click(header);
    await screen.findByText('Key 10');

    await dragFirstKeyToLast(container);
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    await dragFirstKeyToLast(container);
    expect(reorderKeys).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstWrite.resolve({ data: undefined });
      await firstWrite.promise;
    });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(2));
    expect(vi.mocked(reorderKeys).mock.calls[1][0]).toEqual([
      { id: 12, sort_order: 0 },
      { id: 10, sort_order: 1 },
      { id: 11, sort_order: 2 },
    ]);
    await waitFor(async () => {
      const persisted = await getKeys();
      expect(persisted.data.map(key => key.id)).toEqual([12, 10, 11]);
    });
  }, 15000);

  it('keeps a pending write ordered and refreshes keys after the Providers route remounts', async () => {
    let serverKeys = keys.map(key => ({ ...key }));
    const firstWrite = deferred<any>();
    vi.mocked(getKeys).mockImplementation(async () => ({ data: serverKeys.map(key => ({ ...key })) }) as any);
    vi.mocked(reorderKeys)
      .mockImplementationOnce(async payload => {
        await firstWrite.promise;
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      })
      .mockImplementation(async payload => {
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      });

    const firstMount = render(<Providers />);
    await screen.findByText('Provider One');
    const firstHeader = firstMount.container.querySelector('.ant-collapse-header');
    if (!firstHeader) throw new Error('provider collapse header not found');
    fireEvent.click(firstHeader);
    await screen.findByText('Key 10');
    await dragFirstKeyToLast(firstMount.container);
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    await dragFirstKeyToLast(firstMount.container);
    expect(reorderKeys).toHaveBeenCalledTimes(1);
    firstMount.unmount();

    const secondMount = render(<Providers />);
    await screen.findByText('Provider One');
    const secondHeader = secondMount.container.querySelector('.ant-collapse-header');
    if (!secondHeader) throw new Error('provider collapse header not found after remount');
    fireEvent.click(secondHeader);

    expect(rowIds(secondMount.container)).toEqual([]);
    expect(reorderKeys).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstWrite.resolve({ data: undefined });
      await firstWrite.promise;
    });
    await screen.findByText('Key 10');
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(rowIds(secondMount.container)).toEqual([12, 10, 11]));
    expect(vi.mocked(reorderKeys).mock.calls[1][0]).toEqual([
      { id: 12, sort_order: 0 },
      { id: 10, sort_order: 1 },
      { id: 11, sort_order: 2 },
    ]);
    await waitFor(async () => {
      const persisted = await getKeys();
      expect(persisted.data.map(key => key.id)).toEqual([12, 10, 11]);
    });
  }, 15000);

  it('serializes writes across providers with provider-scoped payloads', async () => {
    const providerTwo: Provider = { ...provider, id: 2, name: 'Provider Two' };
    const secondProviderKeys = [20, 21, 22].map((id, index) => ({
      ...keys[index],
      id,
      provider_id: 2,
      name: `Key ${id}`,
      key_value: `sk-${id}`,
      sort_order: index,
    }));
    const firstWrite = deferred<any>();
    vi.mocked(getProviders).mockResolvedValue({ data: [provider, providerTwo] } as any);
    vi.mocked(getKeys).mockResolvedValue({ data: [...keys, ...secondProviderKeys] } as any);
    vi.mocked(reorderKeys)
      .mockImplementationOnce(async () => {
        await firstWrite.promise;
        return { data: undefined } as any;
      })
      .mockResolvedValueOnce({ data: undefined } as any);

    const { container } = render(<Providers />);
    await screen.findByText('Provider Two');
    const headers = container.querySelectorAll('.ant-collapse-header');
    if (headers.length !== 2) throw new Error('both provider headers not found');
    fireEvent.click(headers[0]);
    await screen.findByText('Key 10');
    await dragFirstKeyToLast(container);
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    expect(vi.mocked(reorderKeys).mock.calls[0][0]).toEqual([
      { id: 11, sort_order: 0 },
      { id: 12, sort_order: 1 },
      { id: 10, sort_order: 2 },
    ]);

    fireEvent.click(headers[1]);
    await screen.findByText('Key 20');
    await dragFirstKeyToLast(container, 20);
    expect(reorderKeys).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstWrite.resolve({ data: undefined });
      await firstWrite.promise;
    });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(2));
    expect(vi.mocked(reorderKeys).mock.calls[1][0]).toEqual([
      { id: 21, sort_order: 0 },
      { id: 22, sort_order: 1 },
      { id: 20, sort_order: 2 },
    ]);
  }, 15000);

  it('defers an ordinary fetch that resolves during a drag, then applies the persisted order', async () => {
    let serverKeys = keys.map(key => ({ ...key }));
    const staleFetch = deferred<any>();
    const postDragSnapshot = deferred<any>();
    const reorderWrite = deferred<any>();
    const staleOrder = [...keys].reverse();
    vi.spyOn(globalThis, 'setInterval').mockImplementation(() => 0 as unknown as ReturnType<typeof setInterval>);
    vi.mocked(getKeys)
      .mockResolvedValueOnce({ data: keys.map(key => ({ ...key })) } as any)
      .mockReturnValueOnce(staleFetch.promise)
      .mockReturnValueOnce(postDragSnapshot.promise)
      .mockImplementation(async () => ({
        data: serverKeys.map(key => ({
          ...key,
          name: key.id === 11 ? 'Key 11 refreshed' : key.name,
        })),
      }) as any);
    vi.mocked(updateProvider).mockResolvedValue({ data: provider } as any);
    vi.mocked(reorderKeys).mockImplementation(async payload => {
      await reorderWrite.promise;
      serverKeys = orderKeys(payload, serverKeys);
      return { data: undefined } as any;
    });

    const { container } = render(<Providers />);
    await screen.findByText('Provider One');
    const header = container.querySelector('.ant-collapse-header');
    if (!header) throw new Error('provider header not found');
    fireEvent.click(header);
    await screen.findByText('Key 10');

    const editProvider = container.querySelector('button[title="Edit provider"]');
    if (!editProvider) throw new Error('edit provider button not found');
    fireEvent.click(editProvider);
    await screen.findByText('Edit Provider');
    fireEvent.click(screen.getByRole('button', { name: 'OK' }));
    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(2));

    const draggedRow = startFirstKeyDragToLast(container, 10);
    await act(async () => { staleFetch.resolve({ data: staleOrder }); });
    expect(rowIds(container)).toEqual([10, 11, 12]);

    fireEvent(draggedRow, pointerEvent('pointerup', 1, 190));
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    expect(vi.mocked(reorderKeys).mock.calls[0][0]).toEqual([
      { id: 11, sort_order: 0 },
      { id: 12, sort_order: 1 },
      { id: 10, sort_order: 2 },
    ]);
    expect(rowIds(container)).toEqual([11, 12, 10]);

    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(3));
    await act(async () => {
      postDragSnapshot.resolve({ data: keys.map(key => ({ ...key })) });
      await postDragSnapshot.promise;
    });
    expect(rowIds(container)).toEqual([11, 12, 10]);
    expect(getKeys).toHaveBeenCalledTimes(3);

    await act(async () => {
      reorderWrite.resolve(undefined);
      await reorderWrite.promise;
    });
    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(4));
    await waitFor(() => expect(rowIds(container)).toEqual([11, 12, 10]));
    await screen.findByText('Key 11 refreshed');
  }, 15000);

  it('retries the recovery GET when another Providers instance advances the shared generation', async () => {
    let serverKeys = keys.map(key => ({ ...key }));
    let getKeysCall = 0;
    const ordinaryFetch = deferred<any>();
    const postDropSnapshot = deferred<any>();
    const recoverySnapshot = deferred<any>();
    const latestSnapshot = deferred<any>();
    const firstWrite = deferred<any>();
    vi.spyOn(globalThis, 'setInterval').mockImplementation(() => 0 as unknown as ReturnType<typeof setInterval>);
    vi.mocked(getKeys).mockImplementation(() => {
      getKeysCall++;
      if (getKeysCall <= 2) return Promise.resolve({ data: serverKeys.map(key => ({ ...key })) }) as any;
      if (getKeysCall === 3) return ordinaryFetch.promise;
      if (getKeysCall === 4) return postDropSnapshot.promise;
      if (getKeysCall === 5) return recoverySnapshot.promise;
      return latestSnapshot.promise;
    });
    vi.mocked(updateProvider).mockResolvedValue({ data: provider } as any);
    vi.mocked(reorderKeys)
      .mockImplementationOnce(async payload => {
        await firstWrite.promise;
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      })
      .mockImplementation(async payload => {
        serverKeys = orderKeys(payload, serverKeys);
        return { data: undefined } as any;
      });

    const target = render(<Providers />);
    await waitFor(() => expect(target.container.textContent).toContain('Provider One'));
    const targetHeader = target.container.querySelector('.ant-collapse-header');
    if (!targetHeader) throw new Error('target provider header not found');
    fireEvent.click(targetHeader);
    await waitFor(() => expect(target.container.textContent).toContain('Key 10'));

    const peer = render(<Providers />);
    await waitFor(() => expect(peer.container.textContent).toContain('Provider One'));
    const peerHeader = peer.container.querySelector('.ant-collapse-header');
    if (!peerHeader) throw new Error('peer provider header not found');
    fireEvent.click(peerHeader);
    await waitFor(() => expect(peer.container.textContent).toContain('Key 10'));

    const editProvider = target.container.querySelector('button[title="Edit provider"]');
    if (!editProvider) throw new Error('edit provider button not found');
    fireEvent.click(editProvider);
    await screen.findByText('Edit Provider');
    fireEvent.click(screen.getByRole('button', { name: 'OK' }));
    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(3));

    const targetDraggedRow = startFirstKeyDragToLast(target.container, 10);
    await act(async () => { ordinaryFetch.resolve({ data: [...keys].reverse() }); });
    expect(rowIds(target.container)).toEqual([10, 11, 12]);
    fireEvent(targetDraggedRow, pointerEvent('pointerup', 1, 190));
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(1));
    expect(rowIds(target.container)).toEqual([11, 12, 10]);

    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(4));
    await act(async () => {
      postDropSnapshot.resolve({ data: keys.map(key => ({ ...key })) });
      await postDropSnapshot.promise;
    });
    await act(async () => {
      firstWrite.resolve(undefined);
      await firstWrite.promise;
    });
    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(5));
    const firstDropSnapshot = serverKeys.map(key => ({ ...key }));

    dropFirstKeyToLast(peer.container, 11);
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 180)); });
    await waitFor(() => expect(reorderKeys).toHaveBeenCalledTimes(2));
    expect(serverKeys.map(key => key.id)).toEqual([10, 12, 11]);

    await act(async () => {
      recoverySnapshot.resolve({
        data: firstDropSnapshot.map(key => ({
          ...key,
          name: key.id === 11 ? 'Key 11 from stale drop' : key.name,
        })),
      });
      await recoverySnapshot.promise;
    });
    await waitFor(() => expect(getKeys).toHaveBeenCalledTimes(6));
    expect(rowIds(target.container)).toEqual([11, 12, 10]);
    expect(target.container.textContent).not.toContain('Key 11 from stale drop');
    await act(async () => {
      latestSnapshot.resolve({
        data: serverKeys.map(key => ({
          ...key,
          name: key.id === 11 ? 'Key 11 refreshed' : key.name,
        })),
      });
      await latestSnapshot.promise;
    });
    await waitFor(() => expect(rowIds(target.container)).toEqual([10, 12, 11]));
    await screen.findByText('Key 11 refreshed');
  }, 15000);
});
