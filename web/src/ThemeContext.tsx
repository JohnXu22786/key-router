import React, { createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import { ConfigProvider, theme as antdTheme } from 'antd';
import {
  ThemeMode, THEME_MODE_CHANGE_EVENT, getStoredThemeMode, getSessionThemeMode,
  resolveThemeMode, setStoredThemeMode,
} from './theme';

interface ThemeContextValue {
  mode: ThemeMode;
  // The concrete theme actually rendered (resolves 'system' via the OS).
  isDark: boolean;
  setMode: (mode: ThemeMode) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

// The OS light/dark preference, live. useSyncExternalStore resolves against
// the CURRENT preference during render (no stale mount-time snapshot) and
// re-renders only while the mode is 'system' (subscribe no-ops otherwise).
function useSystemPrefs(mode: ThemeMode): boolean {
  return useSyncExternalStore(
    (onChange) => {
      if (mode !== 'system') return () => {};
      const mq = window.matchMedia('(prefers-color-scheme: dark)');
      mq.addEventListener('change', onChange);
      return () => mq.removeEventListener('change', onChange);
    },
    () => window.matchMedia('(prefers-color-scheme: dark)').matches,
  );
}

// Holds the theme mode, persists it to localStorage, and — while the mode is
// 'system' — tracks the OS preference live so the app flips when the OS does.
const ThemeProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [mode, setModeState] = useState<ThemeMode>(getStoredThemeMode);
  const prefersDark = useSystemPrefs(mode);

  const value = useMemo<ThemeContextValue>(() => ({
    mode,
    isDark: resolveThemeMode(mode, prefersDark) === 'dark',
    setMode: (m: ThemeMode) => {
      setStoredThemeMode(m);
      setModeState(m);
    },
  }), [mode, prefersDark]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
};

const useThemeMode = (): ThemeContextValue => {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useThemeMode must be used within a ThemeProvider');
  return ctx;
};

// StaticThemedHost themes the app's static message/notification/modal
// containers. antd renders those in a SEPARATE React root on first call, so
// the main tree's context never reaches them — this host resolves the theme
// itself and re-subscribes when the mode changes (via the custom event fired
// by setStoredThemeMode, which carries the new mode in its detail) and when
// the OS preference flips. It is injected through ConfigProvider.config's
// holderRender.
const StaticThemedHost: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  // Init from the SESSION mode: storage may hold a stale value when a
  // previous write failed (quota), and the session choice still applies.
  const [mode, setModeState] = useState<ThemeMode>(getSessionThemeMode);
  const prefersDark = useSystemPrefs(mode);

  useEffect(() => {
    const onModeChange = (e: Event) => setModeState((e as CustomEvent<ThemeMode>).detail);
    window.addEventListener(THEME_MODE_CHANGE_EVENT, onModeChange);
    return () => window.removeEventListener(THEME_MODE_CHANGE_EVENT, onModeChange);
  }, []);

  const algorithm = resolveThemeMode(mode, prefersDark) === 'dark'
    ? antdTheme.darkAlgorithm
    : antdTheme.defaultAlgorithm;
  return <ConfigProvider theme={{ algorithm }}>{children}</ConfigProvider>;
};

// Module-level, before any message/notification can be shown (the first
// static call captures this render function).
ConfigProvider.config({
  holderRender: (dom) => <StaticThemedHost>{dom}</StaticThemedHost>,
});

export { ThemeProvider, useThemeMode };
