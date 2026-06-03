import { apiClient } from '@/api/client';
import type { LoginResponse, LoginType, UserInfo } from '@/types/api';

/** POST /loginType — which login UI to render. */
export function fetchLoginType(): Promise<LoginType> {
  return apiClient.post<LoginType>('/loginType', {});
}

/** POST /login — form / no-auth login. */
export function login(
  username: string,
  password: string,
): Promise<LoginResponse> {
  return apiClient.post<LoginResponse>('/login', { username, password });
}

/** POST /sso — returns the IdP redirect URL. */
export function ssoRedirectUrl(): Promise<string> {
  return apiClient.post<string>('/sso', {});
}

/** POST /userinfo — authenticated user profile. */
export function fetchUserInfo(): Promise<UserInfo> {
  return apiClient.post<UserInfo>('/userinfo', {});
}

/** POST /logout. */
export function logout(): Promise<unknown> {
  return apiClient.post<unknown>('/logout', {});
}
