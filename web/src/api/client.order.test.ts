// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { post } = vi.hoisted(() => ({ post: vi.fn() }));

vi.mock('axios', () => ({
  default: {
    create: vi.fn(() => ({ post })),
  },
}));

import { nextKeyOrderRevision, reorderKeys } from './client';
import type { KeyOrderRevision } from './client';

if (!navigator.locks) {
  let lockQueue = Promise.resolve();
  Object.defineProperty(navigator, 'locks', {
    configurable: true,
    value: {
      request: (_name: string, _options: unknown, callback: () => unknown) => {
        const result = lockQueue.then(callback);
        lockQueue = result.then(() => undefined, () => undefined);
        return result;
      },
    },
  });
}

describe('key reorder API contract', () => {
  beforeEach(() => post.mockReset());

  it('serializes the provider, revision, and keys in the server request body', async () => {
    const revision = { sequence: 42, client_id: 'test-tab' };
    const keys = [{ id: 17, sort_order: 0 }, { id: 18, sort_order: 1 }];

    await reorderKeys(7, keys, revision);

    expect(post).toHaveBeenCalledWith('/keys/reorder', {
      provider_id: 7,
      revision,
      keys,
    });
  });

  it('allocates unique increasing revisions for concurrent drops in one tab', async () => {
    const revisions = await Promise.all(
      Array.from({ length: 8 }, () => nextKeyOrderRevision()),
    );
    const sequences = revisions.map(revision => revision.sequence);

    expect(new Set(sequences).size).toBe(sequences.length);
    expect(sequences).toEqual([...sequences].sort((a, b) => a - b));
    expect(new Set(revisions.map(revision => revision.client_id)).size).toBe(1);
  });

  it('allocates the shared revision while holding an exclusive browser lock', async () => {
    const previous = Object.getOwnPropertyDescriptor(navigator, 'locks');
    const request = vi.fn((
      _name: string,
      _options: { mode: 'exclusive' },
      callback: () => KeyOrderRevision,
    ) => Promise.resolve(callback()));
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: { request },
    });

    try {
      await nextKeyOrderRevision();
      expect(request).toHaveBeenCalledWith(
        'key-router:key-order-revision',
        { mode: 'exclusive' },
        expect.any(Function),
      );
    } finally {
      if (previous) Object.defineProperty(navigator, 'locks', previous);
      else Reflect.deleteProperty(navigator, 'locks');
    }
  });
});
