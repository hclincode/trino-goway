import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { UserInfo } from '@/types/api';
import { persistStorage } from './storage';

export const StoreKey = {
  Access: 'access-control',
  Config: 'app-config',
} as const;

export enum Role {
  ADMIN = 'ADMIN',
  API = 'API',
  USER = 'USER',
}

export interface AccessState extends UserInfo {
  token: string;
}

export interface AccessStore extends AccessState {
  /** Set (or clear) the JWT. Clearing also wipes the cached user profile. */
  setToken: (token: string) => void;
  /** Merge a fetched /userinfo payload into the store. */
  setUserInfo: (info: UserInfo) => void;
  /** Clear token + profile (logout / session expiry). */
  clear: () => void;
  isAuthorized: () => boolean;
  hasRole: (role: Role) => boolean;
  hasPermission: (permission?: string) => boolean;
}

const EMPTY_USER: UserInfo = {
  userId: '',
  userName: '',
  nickName: '',
  userType: '',
  email: '',
  phonenumber: '',
  sex: '',
  avatar: '',
  permissions: [],
  roles: [],
};

export const useAccessStore = create<AccessStore>()(
  persist(
    (set, get) => ({
      token: '',
      ...EMPTY_USER,

      setToken(token: string) {
        const trimmed = token?.trim() ?? '';
        if (!trimmed) {
          set({ token: '', ...EMPTY_USER });
          return;
        }
        set({ token: trimmed });
      },

      setUserInfo(info: UserInfo) {
        set({ ...info });
      },

      clear() {
        set({ token: '', ...EMPTY_USER });
      },

      isAuthorized() {
        return !!get().token;
      },

      hasRole(role: Role) {
        return get().roles.includes(role);
      },

      hasPermission(permission?: string) {
        const { permissions } = get();
        return (
          permission === undefined ||
          permissions == null ||
          permissions.length === 0 ||
          permissions.includes(permission)
        );
      },
    }),
    {
      name: StoreKey.Access,
      version: 1,
      storage: persistStorage,
    },
  ),
);

/** Non-reactive token read for the API client (outside React). */
export function getToken(): string {
  return useAccessStore.getState().token;
}
