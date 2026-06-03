import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { defaultTimezone } from '@/utils/time';

interface TimezoneContextValue {
  timezone: string;
  changeTimezone: (tz: string) => void;
}

const TimezoneContext = createContext<TimezoneContextValue>({
  timezone: 'UTC',
  changeTimezone: () => {},
});

/**
 * Provides the selected display timezone. In-memory only (matches the
 * original: resets to the browser timezone on refresh).
 */
export function TimezoneProvider({ children }: { children: ReactNode }) {
  const [timezone, setTimezone] = useState<string>(() => defaultTimezone());
  const changeTimezone = useCallback((tz: string) => setTimezone(tz), []);
  const value = useMemo(
    () => ({ timezone, changeTimezone }),
    [timezone, changeTimezone],
  );
  return (
    <TimezoneContext.Provider value={value}>
      {children}
    </TimezoneContext.Provider>
  );
}

export function useTimezone(): TimezoneContextValue {
  return useContext(TimezoneContext);
}
