import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '@/test/renderWithProviders';
import { EXTERNAL_ROUTING } from '@/api/client';
import { useAccessStore } from '@/stores/access';
import RoutingRulesPage from './RoutingRulesPage';

vi.mock('@/api/endpoints/routingRules', () => ({
  getRoutingRules: vi.fn(),
  updateRoutingRules: vi.fn(),
}));
import { getRoutingRules } from '@/api/endpoints/routingRules';

describe('RoutingRulesPage', () => {
  afterEach(() => {
    vi.clearAllMocks();
    useAccessStore.getState().clear();
  });

  function authed() {
    useAccessStore.setState({ token: 'tok', roles: ['ADMIN'] });
  }

  it('shows the external-routing notice on a 204 sentinel', async () => {
    authed();
    vi.mocked(getRoutingRules).mockResolvedValue(EXTERNAL_ROUTING);
    renderWithProviders(<RoutingRulesPage />);
    await waitFor(() =>
      expect(screen.getByText(/managed by an external service/i)).toBeInTheDocument(),
    );
  });

  it('shows the empty notice when there are no rules', async () => {
    authed();
    vi.mocked(getRoutingRules).mockResolvedValue([]);
    renderWithProviders(<RoutingRulesPage />);
    await waitFor(() =>
      expect(screen.getByText(/no routing rules configured/i)).toBeInTheDocument(),
    );
  });

  it('renders one card per rule with the Edit button for ADMIN', async () => {
    authed();
    vi.mocked(getRoutingRules).mockResolvedValue([
      { name: 'r1', description: 'd', priority: 1, actions: ['a'], condition: 'c' },
    ]);
    renderWithProviders(<RoutingRulesPage />);
    await waitFor(() =>
      expect(screen.getByText('Routing rule #1')).toBeInTheDocument(),
    );
    expect(screen.getByRole('button', { name: /edit/i })).toBeInTheDocument();
  });
});
