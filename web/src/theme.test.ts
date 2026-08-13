import { describe, it, expect } from 'vitest';
import {
  THEME_MODE_KEY, THEME_MODE_CHANGE_EVENT, parseStoredThemeMode, resolveThemeMode,
  getStoredThemeMode, getSessionThemeMode, setStoredThemeMode,
} from './theme';

// Node test env has no localStorage — stub a minimal one backed by a Map.
function stubLocalStorage() {
  const store = new Map<string, string>();
  (globalThis as any).localStorage = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => { store.set(k, v); },
  };
  return store;
}

describe('parseStoredThemeMode — missing/corrupt values fall back', () => {
  it('defaults to light when nothing is stored (existing users keep the light UI)', () => {
    expect(parseStoredThemeMode(null)).toBe('light');
  });

  it('falls back to light for unknown values', () => {
    expect(parseStoredThemeMode('')).toBe('light');
    expect(parseStoredThemeMode('neon')).toBe('light');
    expect(parseStoredThemeMode('auto')).toBe('light');
  });

  it('accepts the three valid modes', () => {
    expect(parseStoredThemeMode('light')).toBe('light');
    expect(parseStoredThemeMode('dark')).toBe('dark');
    expect(parseStoredThemeMode('system')).toBe('system');
  });
});

describe('resolveThemeMode — mode × OS preference', () => {
  it('light always resolves to light, regardless of the OS preference', () => {
    expect(resolveThemeMode('light', false)).toBe('light');
    expect(resolveThemeMode('light', true)).toBe('light');
  });

  it('dark always resolves to dark, regardless of the OS preference', () => {
    expect(resolveThemeMode('dark', false)).toBe('dark');
    expect(resolveThemeMode('dark', true)).toBe('dark');
  });

  it('system follows the OS preference', () => {
    expect(resolveThemeMode('system', false)).toBe('light');
    expect(resolveThemeMode('system', true)).toBe('dark');
  });
});

describe('storage roundtrip', () => {
  it('get/set share the same storage key (guard against key drift)', () => {
    const store = stubLocalStorage();
    setStoredThemeMode('dark');
    expect(store.get(THEME_MODE_KEY)).toBe('dark');
    expect(getStoredThemeMode()).toBe('dark');
    setStoredThemeMode('system');
    expect(getStoredThemeMode()).toBe('system');
    store.clear();
    expect(getStoredThemeMode()).toBe('light');
  });
});

describe('storage failures degrade gracefully', () => {
  it('a throwing storage falls back to the session mode instead of crashing', () => {
    stubLocalStorage();
    setStoredThemeMode('light'); // establish a known in-session mode
    (globalThis as any).localStorage = {
      getItem: () => { throw new Error('storage disabled'); },
      setItem: () => { throw new Error('storage disabled'); },
    };
    // Nothing written this session yet → the default.
    expect(getStoredThemeMode()).toBe('light');
    // setItem must not throw — the mode still applies in-session...
    expect(() => setStoredThemeMode('dark')).not.toThrow();
    // ...and a broken read still resolves to the remembered session mode.
    expect(getStoredThemeMode()).toBe('dark');
  });
});

describe('change event', () => {
  it('carries the new mode in its detail and skips no-op re-selections', () => {
    stubLocalStorage();
    const events: any[] = [];
    (globalThis as any).window = { dispatchEvent: (e: any) => events.push(e) };

    setStoredThemeMode('dark');
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe(THEME_MODE_CHANGE_EVENT);
    expect(events[0].detail).toBe('dark');

    // Re-selecting the current mode must not write or dispatch again.
    setStoredThemeMode('dark');
    expect(events).toHaveLength(1);
  });

  it('dispatches the in-session mode even when persistence fails', () => {
    (globalThis as any).localStorage = {
      getItem: () => null,
      setItem: () => { throw new Error('quota exceeded'); },
    };
    const events: any[] = [];
    (globalThis as any).window = { dispatchEvent: (e: any) => events.push(e) };

    setStoredThemeMode('dark');
    expect(events).toHaveLength(1);
    expect(events[0].detail).toBe('dark');
  });

  it('getSessionThemeMode follows the session choice over stale storage', () => {
    stubLocalStorage();
    // Stale storage: the last successful write left 'light', but the
    // session moved to 'dark' (the write below fails).
    (globalThis as any).localStorage = {
      getItem: () => 'light',
      setItem: () => { throw new Error('quota exceeded'); },
    };
    const events: any[] = [];
    (globalThis as any).window = { dispatchEvent: (e: any) => events.push(e) };

    setStoredThemeMode('dark');
    expect(getSessionThemeMode()).toBe('dark');
    expect(getStoredThemeMode()).toBe('light'); // storage truth is unchanged
  });
});
