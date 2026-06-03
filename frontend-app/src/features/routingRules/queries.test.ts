import { describe, expect, it } from 'vitest';
import { EXTERNAL_ROUTING } from '@/api/client';
import { isExternalRouting } from './queries';
import type { RoutingRulesData } from '@/types/api';

describe('isExternalRouting', () => {
  it('is true for the EXTERNAL_ROUTING sentinel', () => {
    expect(isExternalRouting(EXTERNAL_ROUTING)).toBe(true);
  });

  it('is true for any object with isExternalRouting', () => {
    expect(isExternalRouting({ isExternalRouting: true })).toBe(true);
  });

  it('is false for a rules array', () => {
    const rules: RoutingRulesData[] = [
      { name: 'r', description: '', priority: 1, actions: [], condition: 'x' },
    ];
    expect(isExternalRouting(rules)).toBe(false);
  });

  it('is false for undefined', () => {
    expect(isExternalRouting(undefined)).toBe(false);
  });
});
