import { useCallback, useEffect, useState } from 'react';
import { Theme, useConfigStore } from '@/stores/config';

export type ResolvedMode = 'light' | 'dark';

function systemPrefersDark(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: dark)').matches
  );
}

function resolve(theme: Theme, prefersDark: boolean): ResolvedMode {
  if (theme === Theme.Dark) return 'dark';
  if (theme === Theme.Light) return 'light';
  return prefersDark ? 'dark' : 'light';
}

/**
 * Theme controller: exposes the persisted theme setting, the resolved
 * light/dark mode (auto follows the OS), a cycle action, and keeps the
 * document's `theme-mode` attribute + `<meta theme-color>` in sync.
 */
export function useTheme() {
  const theme = useConfigStore((s) => s.theme);
  const cycleTheme = useConfigStore((s) => s.cycleTheme);
  const setTheme = useConfigStore((s) => s.setTheme);

  const [prefersDark, setPrefersDark] = useState<boolean>(systemPrefersDark);

  // Track the OS preference so `auto` reacts live.
  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return;
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = (e: MediaQueryListEvent) => setPrefersDark(e.matches);
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);

  const mode = resolve(theme, prefersDark);

  // Reflect the resolved mode on the document for CSS + chart token reads.
  useEffect(() => {
    const root = document.documentElement;
    if (mode === 'dark') {
      root.setAttribute('theme-mode', 'dark');
    } else {
      root.removeAttribute('theme-mode');
    }
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) {
      meta.setAttribute('content', mode === 'dark' ? '#1f1f1f' : '#ffffff');
    }
  }, [mode]);

  const cycle = useCallback(() => cycleTheme(), [cycleTheme]);

  return { theme, mode, cycle, setTheme };
}
