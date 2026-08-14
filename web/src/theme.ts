// Theme mode preference. A pure UI concern, so it lives in localStorage
// (persisted by WebView2 across restarts) instead of the server config.
// App.tsx resolves the stored mode to a concrete light/dark theme.

export type ThemeMode = 'light' | 'dark' | 'system';

export const THEME_MODE_KEY = 'keyrouter.theme-mode';

// Fired on window whenever the stored mode changes (same tab), carrying the
// new mode in the event detail. The static message/notification root (a
// separate React tree, see StaticThemedHost) subscribes to it to stay in
// sync even when persistence fails.
export const THEME_MODE_CHANGE_EVENT = 'keyrouter:theme-mode-change';

// Default: follow the OS — the app tracks the system theme for users who
// never touch the setting. An explicit light/dark choice still wins.
export const DEFAULT_THEME_MODE: ThemeMode = 'system';

// Maps a raw stored value to a valid mode; anything unknown (corrupt value,
// legacy content, missing key) falls back to the default.
export function parseStoredThemeMode(raw: string | null): ThemeMode {
  return raw === 'light' || raw === 'dark' || raw === 'system' ? raw : DEFAULT_THEME_MODE;
}

// Storage access is guarded: a throwing localStorage (disabled storage,
// quota) must not blank the app. lastMode is the SESSION truth — synced from
// storage by the app's initial read, updated by every mode change — so
// consumers that mount later (the static message root, on the first toast)
// resolve to the user's actual choice even when persistence failed.
let lastMode: ThemeMode = DEFAULT_THEME_MODE;

export function getStoredThemeMode(): ThemeMode {
  try {
    const mode = parseStoredThemeMode(localStorage.getItem(THEME_MODE_KEY));
    lastMode = mode;
    return mode;
  } catch {
    return lastMode;
  }
}

// The mode applying to this session. Use this for reads that happen after
// the app's startup read (e.g. the static message root's first mount):
// storage may hold a stale value when a previous write failed.
export function getSessionThemeMode(): ThemeMode {
  return lastMode;
}

export function setStoredThemeMode(mode: ThemeMode): void {
  try {
    // A true no-op only when BOTH storage and the session mode match: after
    // a failed write the stored value is stale, and skipping the event then
    // would desync the static message root from the session choice.
    const stored = parseStoredThemeMode(localStorage.getItem(THEME_MODE_KEY));
    if (stored === mode && lastMode === mode) return;
    localStorage.setItem(THEME_MODE_KEY, mode);
  } catch {
    // Persistence failed — the mode still applies for this session.
  }
  lastMode = mode;
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent<ThemeMode>(THEME_MODE_CHANGE_EVENT, { detail: mode }));
  }
}

// Pure resolution: which concrete theme a mode maps to for a given OS
// preference. 'system' follows the OS; explicit choices always win.
export function resolveThemeMode(mode: ThemeMode, prefersDark: boolean): 'light' | 'dark' {
  return mode === 'dark' || (mode === 'system' && prefersDark) ? 'dark' : 'light';
}
