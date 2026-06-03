import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '@/test/renderWithProviders';
import { useAccessStore } from '@/stores/access';
import type { BackendData } from '@/types/api';
import ClusterPage from './ClusterPage';

vi.mock('@/api/endpoints/cluster', () => ({
  getAllBackends: vi.fn(),
  saveBackend: vi.fn(),
  updateBackend: vi.fn(),
  deleteBackend: vi.fn(),
}));
import { getAllBackends } from '@/api/endpoints/cluster';

const ROWS: BackendData[] = [
  {
    name: 'beta',
    proxyTo: 'http://beta',
    externalUrl: 'http://beta-ext',
    active: true,
    routingGroup: 'adhoc',
    queued: 0,
    running: 0,
    status: 'HEALTHY',
  },
  {
    name: 'alpha',
    proxyTo: 'http://alpha',
    externalUrl: '',
    active: false,
    routingGroup: 'etl',
    queued: 0,
    running: 0,
    status: 'UNHEALTHY',
  },
];

describe('ClusterPage', () => {
  afterEach(() => {
    vi.clearAllMocks();
    useAccessStore.getState().clear();
  });

  it('renders backends sorted alphabetically by name', async () => {
    useAccessStore.setState({ token: 'tok', roles: ['USER'] });
    vi.mocked(getAllBackends).mockResolvedValue(ROWS);
    renderWithProviders(<ClusterPage />);

    await waitFor(() => expect(screen.getByText('alpha')).toBeInTheDocument());
    const names = screen
      .getAllByRole('cell')
      .map((c) => c.textContent)
      .filter((t) => t === 'alpha' || t === 'beta');
    expect(names).toEqual(['alpha', 'beta']);
  });

  it('hides the Operations column for non-ADMIN users', async () => {
    useAccessStore.setState({ token: 'tok', roles: ['USER'] });
    vi.mocked(getAllBackends).mockResolvedValue(ROWS);
    renderWithProviders(<ClusterPage />);
    await waitFor(() => expect(screen.getByText('alpha')).toBeInTheDocument());
    expect(screen.queryByText('Operations')).not.toBeInTheDocument();
  });

  it('shows the Operations column for ADMIN users', async () => {
    useAccessStore.setState({ token: 'tok', roles: ['ADMIN'] });
    vi.mocked(getAllBackends).mockResolvedValue(ROWS);
    renderWithProviders(<ClusterPage />);
    await waitFor(() => expect(screen.getByText('alpha')).toBeInTheDocument());
    expect(screen.getByText('Operations')).toBeInTheDocument();
  });
});
