import { QueryClient } from '@tanstack/react-query';
import { SessionExpiredError } from '@/api/client';

/**
 * Shared QueryClient. Session-expiry errors are never retried (the user must
 * re-authenticate); other failures get a single retry.
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) => {
        if (error instanceof SessionExpiredError) return false;
        return failureCount < 1;
      },
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
    mutations: {
      retry: false,
    },
  },
});
