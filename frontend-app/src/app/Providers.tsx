import { useEffect, type ReactNode } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { App as AntApp, ConfigProvider, theme as antTheme } from 'antd';
import enUS from 'antd/locale/en_US';
import { I18nextProvider, useTranslation } from 'react-i18next';
import { onSessionExpired } from '@/api/client';
import { useTheme } from '@/hooks/useTheme';
import { TimezoneProvider } from '@/context/timezone';
import i18n from '@/locales/i18n';
import { queryClient } from './queryClient';
import { ErrorBoundary } from './ErrorBoundary';
import { notify, setMessageApi } from './notify';

/** Wires antd's message instance into the notify bridge + session expiry toast. */
function MessageBridge({ children }: { children: ReactNode }) {
  const { message } = AntApp.useApp();
  const { t } = useTranslation();

  useEffect(() => {
    setMessageApi(message);
  }, [message]);

  // Surface a toast whenever the API client signals session expiry.
  useEffect(() => {
    return onSessionExpired(() => {
      notify.error(t('auth.expiration'));
    });
  }, [t]);

  return <>{children}</>;
}

/** Applies the resolved light/dark mode to antd's algorithm. */
function ThemedConfig({ children }: { children: ReactNode }) {
  const { mode } = useTheme();
  return (
    <ConfigProvider
      locale={enUS}
      theme={{
        algorithm:
          mode === 'dark' ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
        cssVar: true,
      }}
    >
      <AntApp style={{ height: '100%' }}>
        <MessageBridge>{children}</MessageBridge>
      </AntApp>
    </ConfigProvider>
  );
}

export function Providers({ children }: { children: ReactNode }) {
  return (
    <ErrorBoundary>
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <TimezoneProvider>
            <ThemedConfig>{children}</ThemedConfig>
          </TimezoneProvider>
        </QueryClientProvider>
      </I18nextProvider>
    </ErrorBoundary>
  );
}
