import { useTheme } from '@/hooks/useTheme';

export interface ChartColors {
  text: string;
  axisLine: string;
  /** Used as a key so charts re-render their option on theme change. */
  mode: 'light' | 'dark';
}

/**
 * Theme-aware colors for ECharts. Recomputed whenever the resolved light/dark
 * mode changes so chart legends/axes follow the app theme.
 */
export function useChartColors(): ChartColors {
  const { mode } = useTheme();
  return mode === 'dark'
    ? { text: 'rgba(255,255,255,0.85)', axisLine: 'rgba(255,255,255,0.25)', mode }
    : { text: 'rgba(0,0,0,0.85)', axisLine: 'rgba(0,0,0,0.25)', mode };
}
