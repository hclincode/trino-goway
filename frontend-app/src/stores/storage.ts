import { createJSONStorage, type StateStorage } from 'zustand/middleware';

/**
 * localStorage-backed storage that degrades to an in-memory map when the
 * platform storage is unavailable (e.g. jsdom without a window, privacy mode).
 */
const memory = new Map<string, string>();

const safeStorage: StateStorage = {
  getItem(name) {
    try {
      return globalThis.localStorage?.getItem(name) ?? memory.get(name) ?? null;
    } catch {
      return memory.get(name) ?? null;
    }
  },
  setItem(name, value) {
    try {
      globalThis.localStorage?.setItem(name, value);
    } catch {
      memory.set(name, value);
    }
  },
  removeItem(name) {
    try {
      globalThis.localStorage?.removeItem(name);
    } catch {
      memory.delete(name);
    }
  },
};

export const persistStorage = createJSONStorage(() => safeStorage);
