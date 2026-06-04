import { createContext, useContext } from 'react';

export interface TimezoneContextValue {
  timezone: string;
  changeTimezone: (tz: string) => void;
}

export const TimezoneContext = createContext<TimezoneContextValue>({
  timezone: 'UTC',
  changeTimezone: () => {},
});

export function useTimezone(): TimezoneContextValue {
  return useContext(TimezoneContext);
}
