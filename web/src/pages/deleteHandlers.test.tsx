// @vitest-environment jsdom
// Regression test for the variable-shadowing bug: the local delete handlers
// in Providers.tsx and Models.tsx were declared with the same name as the
// imported API functions (`deleteProvider`, `deleteKey`, `deleteRoute`).
// Inside each handler, `await deleteProvider(id)` then resolved to the local
// binding — the handler recursed into itself instead of the imported
// `api.delete('/providers/:id')`. The HTTP request was never issued, the
// item stayed on screen, and the recursion was caught by a stack-overflow
// toast.
//
// The fix renames the local handlers (`onDeleteProvider`, `onDeleteKey`,
// `onDeleteRoute`) so the imported `api.delete(...)` is the one called.
// This test pins that behavior: clicking each confirm must invoke the
// imported mock exactly once with the right id.
import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, act, waitFor } from '@testing-library/react';
import Providers from './Providers';
import Models from './Models';
import {
  getProviders, updateProvider, deleteProvider,
  getKeys, updateKey, deleteKey, getKeyDetail,
  getRoutes, getModelGroups, updateModelGroup, deleteModelGroup,
  updateRoute, deleteRoute, reorderKeys, reorderRoutes,
} from '../api/client';
import { subscribeEvents } from '../api/events';
import type { Provider, Key, Route, ModelGroup } from '../api/client';

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>();
  return {
    ...actual,
    getProviders: vi.fn(),
    updateProvider: vi.fn(),
    deleteProvider: vi.fn(),
    getKeys: vi.fn(),
    updateKey: vi.fn(),
    deleteKey: vi.fn(),
    getKeyDetail: vi.fn(),
    reorderKeys: vi.fn(),
    getRoutes: vi.fn(),
    getModelGroups: vi.fn(),
    updateModelGroup: vi.fn(),
    deleteModelGroup: vi.fn(),
    updateRoute: vi.fn(),
    deleteRoute: vi.fn(),
    reorderRoutes: vi.fn(),
  };
});

// The page's SSE subscription would try to open a real EventSource in jsdom
// (which is a no-op but the polyfill chatter pollutes logs); swap it for a
// no-op unsubscribe.
vi.mock('../api/events', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/events')>();
  return { ...actual, subscribeEvents: vi.fn(() => () => {}) };
});

if (typeof window.matchMedia !== 'function') {
  (window as unknown as { matchMedia: (q: string) => unknown }).matchMedia = (q: string) => ({
    matches: false, media: q, onchange: null,
    addListener: () => {}, removeListener: () => {},
    addEventListener: () => {}, removeEventListener: () => {},
    dispatchEvent: () => false,
  });
}
if (typeof (window as unknown as { ResizeObserver?: unknown }).ResizeObserver !== 'function') {
  (window as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {} unobserve() {} disconnect() {}
  };
}

const prov1: Provider = { id: 1, name: 'P1', type: 'openai', base_url: 'https://x', extra_headers: '', created_at: '', updated_at: '' };
const key1: Key = {
  id: 10, provider_id: 1, name: 'K1', key_value: 'sk-abc', status: 'active',
  recovery_strategy: 'lazy', sort_order: 0, total_spent: 0, total_spend_limit: 0,
  rpm_limit: 0, tpm_limit: 0, rp5h_limit: 0, rp5h_metric: 'requests',
  rpd_limit: 0, rpd_metric: 'requests', rpw_limit: 0, rpw_metric: 'requests',
  rpm_month_limit: 0, rpm_metric: 'requests',
} as unknown as Key;

const group1: ModelGroup = {
  id: 100, group_id: 'gpt-4o', name: 'GPT-4o', enabled: true, retry_times: 0,
  context_length: 0, max_output_tokens: 0, created_at: 0,
} as unknown as ModelGroup;

const route1: Route = {
  id: 200, model_group_id: 100, provider_id: 1, target_model: '',
  enabled: true, prompt_per_1m: 0, completion_per_1m: 0,
  cache_read_per_1m: 0, cache_write_per_1m: 0, priority: 0,
} as unknown as Route;

beforeEach(() => {
  vi.mocked(getProviders).mockResolvedValue({ data: [] } as any);
  vi.mocked(getKeys).mockResolvedValue({ data: [] } as any);
  vi.mocked(getRoutes).mockResolvedValue({ data: [] } as any);
  vi.mocked(getModelGroups).mockResolvedValue({ data: [] } as any);
  vi.mocked(deleteProvider).mockResolvedValue({ data: undefined } as any);
  vi.mocked(deleteKey).mockResolvedValue({ data: undefined } as any);
  vi.mocked(deleteRoute).mockResolvedValue({ data: undefined } as any);
  vi.mocked(getKeyDetail).mockResolvedValue({ data: null } as any);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// antd Popconfirm renders the confirm button inside a portal after the
// trigger is clicked. We click the trigger, then the popover OK button.
// The page's fetch() is async; we wait for the data row to appear in the
// DOM (findBy) before clicking, so the test isn't racing the initial load.
async function clickPopconfirmOkByTitle(container: HTMLElement, title: string) {
  const trigger = container.querySelector(`button[title="${title}"]`) as HTMLButtonElement | null;
  if (!trigger) throw new Error(`button[title="${title}"] not found in rendered page`);
  await act(async () => { fireEvent.click(trigger); });
  // antd Popconfirm's OK button is a .ant-btn-primary inside the popover
  // (or specifically inside .ant-popover-buttons).
  const ok = document.querySelector(
    '.ant-popover-buttons .ant-btn-primary, .ant-popover:not(.ant-popover-hidden) .ant-btn-primary',
  ) as HTMLButtonElement | null;
  if (!ok) throw new Error('popover OK button not found');
  await act(async () => { fireEvent.click(ok); });
}

describe('Providers — delete handlers must call the imported api functions', () => {
  it('clicking the provider delete confirm invokes api.deleteProvider(id), not a local shadow', async () => {
    vi.mocked(getProviders).mockResolvedValue({ data: [prov1] } as any);
    vi.mocked(getKeys).mockResolvedValue({ data: [key1] } as any);
    vi.mocked(getRoutes).mockResolvedValue({ data: [] } as any);

    const { container, findByText } = render(<Providers />);
    // Wait for the fetch() to settle and the provider label to render.
    await findByText('P1');

    // The bug, if reintroduced, makes the handler recurse into itself; the
    // imported mock is never called. We assert it is called within a tight
    // window so a regression fails fast.
    await clickPopconfirmOkByTitle(container, 'Delete provider');

    await waitFor(() => {
      expect(vi.mocked(deleteProvider)).toHaveBeenCalledTimes(1);
    });
    expect(vi.mocked(deleteProvider)).toHaveBeenCalledWith(1);
  });

  it('clicking the key delete confirm invokes api.deleteKey(id), not a local shadow', async () => {
    vi.mocked(getProviders).mockResolvedValue({ data: [prov1] } as any);
    vi.mocked(getKeys).mockResolvedValue({ data: [key1] } as any);
    vi.mocked(getRoutes).mockResolvedValue({ data: [] } as any);

    const { container, findByText } = render(<Providers />);
    // Expand the provider so the keys table renders. findByText waits for
    // the label, then we click the collapse header to open it.
    await findByText('P1');
    const header = container.querySelector('.ant-collapse-header') as HTMLElement | null;
    if (!header) throw new Error('collapse header not found');
    await act(async () => { fireEvent.click(header); });
    // Wait for the key row to render after the collapse opens.
    await findByText('K1');

    // The keys-table per-row delete button has title="Delete".
    await clickPopconfirmOkByTitle(container, 'Delete');

    await waitFor(() => {
      expect(vi.mocked(deleteKey)).toHaveBeenCalledTimes(1);
    });
    expect(vi.mocked(deleteKey)).toHaveBeenCalledWith(10);
  });
});

describe('Models — delete handler must call the imported api function', () => {
  it('clicking the route delete confirm invokes api.deleteRoute(id), not a local shadow', async () => {
    vi.mocked(getModelGroups).mockResolvedValue({ data: [group1] } as any);
    vi.mocked(getRoutes).mockResolvedValue({ data: [route1] } as any);
    vi.mocked(getProviders).mockResolvedValue({ data: [prov1] } as any);

    const { container, findByText, findByTitle } = render(<Models />);
    await findByText('GPT-4o');
    const header = container.querySelector('.ant-collapse-header') as HTMLElement | null;
    if (!header) throw new Error('collapse header not found');
    await act(async () => { fireEvent.click(header); });
    // Wait for the per-row delete button to appear (title="Delete"), which
    // is unique to the route row — the group-level button is title="Delete
    // group", so this distinguishes them without ambiguity.
    await findByTitle('Delete');

    await clickPopconfirmOkByTitle(container, 'Delete');

    await waitFor(() => {
      expect(vi.mocked(deleteRoute)).toHaveBeenCalledTimes(1);
    });
    expect(vi.mocked(deleteRoute)).toHaveBeenCalledWith(200);
  });
});
