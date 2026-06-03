import type { ReactElement, ReactNode } from 'react';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App as AntApp } from 'antd';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';
import { TimezoneProvider } from '@/context/timezone';
import i18n from '@/locales/i18n';

/**
 * Render a component inside the app's providers (Query, antd App, i18n,
 * timezone, router) with an isolated QueryClient per test.
 */
export function renderWithProviders(
  ui: ReactElement,
  { route = '/' }: { route?: string } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });

  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={[route]}>
            <TimezoneProvider>
              <AntApp>{children}</AntApp>
            </TimezoneProvider>
          </MemoryRouter>
        </QueryClientProvider>
      </I18nextProvider>
    );
  }

  return { queryClient, ...render(ui, { wrapper: Wrapper }) };
}
