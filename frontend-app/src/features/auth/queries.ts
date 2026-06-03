import { useEffect } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import Cookies from 'js-cookie';
import {
  fetchLoginType,
  fetchUserInfo,
  login,
  logout,
  ssoRedirectUrl,
} from '@/api/endpoints/auth';
import { useAccessStore } from '@/stores/access';

/** POST /loginType — drives which login variant renders. */
export function useLoginType() {
  return useQuery({
    queryKey: ['loginType'],
    queryFn: fetchLoginType,
    staleTime: Infinity,
  });
}

/**
 * Hydrate the access store with the user profile whenever a token is present
 * but the profile has not been loaded yet. Runs once per token.
 */
export function useHydrateUserInfo() {
  const token = useAccessStore((s) => s.token);
  const userName = useAccessStore((s) => s.userName);
  const setUserInfo = useAccessStore((s) => s.setUserInfo);

  const enabled = !!token && !userName;
  const query = useQuery({
    queryKey: ['userinfo', token],
    queryFn: fetchUserInfo,
    enabled,
    staleTime: Infinity,
    retry: false,
  });

  useEffect(() => {
    if (query.data) {
      setUserInfo(query.data);
    }
  }, [query.data, setUserInfo]);

  return query;
}

/**
 * Consume a `token` cookie set by the OIDC callback on mount, store it, then
 * remove the cookie. Runs once.
 */
export function useConsumeOidcCookie() {
  const setToken = useAccessStore((s) => s.setToken);
  useEffect(() => {
    const cookieToken = Cookies.get('token');
    if (cookieToken) {
      setToken(cookieToken);
      Cookies.remove('token');
    }
  }, [setToken]);
}

export function useLoginMutation() {
  const setToken = useAccessStore((s) => s.setToken);
  return useMutation({
    mutationFn: (vars: { username: string; password: string }) =>
      login(vars.username, vars.password),
    onSuccess: (data) => {
      setToken(data.token);
    },
  });
}

export function useSsoMutation() {
  return useMutation({
    mutationFn: () => ssoRedirectUrl(),
    onSuccess: (url) => {
      window.location.href = url;
    },
  });
}

export function useLogoutMutation() {
  const clear = useAccessStore((s) => s.clear);
  return useMutation({
    mutationFn: () => logout(),
    // Clear locally regardless of server outcome.
    onSettled: () => {
      clear();
    },
  });
}
