import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { StoreKey } from './access';
import { persistStorage } from './storage';

export enum Theme {
  Auto = 'auto',
  Dark = 'dark',
  Light = 'light',
}

export const DEFAULT_CONFIG = {
  avatar: '/trino-gateway/logo.svg',
  theme: Theme.Auto as Theme,
  fontSize: 14,
  sidebarWidth: 240,
};

export type AppConfig = typeof DEFAULT_CONFIG;

export interface ConfigStore extends AppConfig {
  setTheme: (theme: Theme) => void;
  /** Advance the theme cycle: auto -> light -> dark -> auto. */
  cycleTheme: () => void;
  reset: () => void;
}

const THEME_CYCLE: Record<Theme, Theme> = {
  [Theme.Auto]: Theme.Light,
  [Theme.Light]: Theme.Dark,
  [Theme.Dark]: Theme.Auto,
};

export const useConfigStore = create<ConfigStore>()(
  persist(
    (set, get) => ({
      ...DEFAULT_CONFIG,

      setTheme(theme: Theme) {
        set({ theme });
      },

      cycleTheme() {
        set({ theme: THEME_CYCLE[get().theme] });
      },

      reset() {
        set({ ...DEFAULT_CONFIG });
      },
    }),
    {
      name: StoreKey.Config,
      version: 1,
      storage: persistStorage,
    },
  ),
);
