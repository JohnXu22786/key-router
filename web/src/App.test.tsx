// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { getAutoCheckState, getHealth } from './api/client';
import App from './App';

vi.mock('./pages/Activity', () => ({
  default: () => { throw new Error('activity page failed'); },
}));
vi.mock('./pages/Providers', () => ({
  default: () => <div>Healthy providers page</div>,
}));
vi.mock('./pages/Models', () => ({ default: () => <div>Models page</div> }));
vi.mock('./pages/Settings', () => ({ default: () => <div>Settings page</div> }));
vi.mock('./pages/Help', () => ({ default: () => <div>Help page</div> }));

vi.mock('./api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api/client')>();
  return {
    ...actual,
    getAutoCheckState: vi.fn(),
    getHealth: vi.fn(),
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
if (typeof (globalThis as { ResizeObserver?: unknown }).ResizeObserver !== 'function') {
  (globalThis as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

beforeEach(() => {
  window.history.replaceState({}, '', '/');
  vi.mocked(getAutoCheckState).mockResolvedValue({ data: { update_available: false } } as any);
  vi.mocked(getHealth).mockResolvedValue({ data: { version: 'test' } } as any);
  vi.spyOn(console, 'error').mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('App route error boundaries', () => {
  it('resets a failed page boundary when navigating to a healthy route', async () => {
    render(<App />);

    expect(await screen.findByText('Something went wrong on this page')).not.toBeNull();
    const providersLink = screen.getByRole('link', { name: 'Providers' });
    expect(providersLink).not.toBeNull();

    fireEvent.click(providersLink);

    expect(await screen.findByText('Healthy providers page')).not.toBeNull();
    expect(screen.queryByText('Something went wrong on this page')).toBeNull();
  });
});
