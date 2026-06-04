import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '@/test/renderWithProviders';
import type { LoginType } from '@/types/api';
import LoginPage from './LoginPage';

vi.mock('@/api/endpoints/auth', () => ({
  fetchLoginType: vi.fn(),
  login: vi.fn(),
  ssoRedirectUrl: vi.fn(),
  fetchUserInfo: vi.fn(),
  logout: vi.fn(),
}));
import { fetchLoginType } from '@/api/endpoints/auth';

describe('LoginPage', () => {
  afterEach(() => vi.clearAllMocks());

  it('renders username + password fields for the form login type', async () => {
    vi.mocked(fetchLoginType).mockResolvedValue('form' as LoginType);
    renderWithProviders(<LoginPage />);
    await waitFor(() =>
      expect(screen.getByLabelText('Username')).toBeInTheDocument(),
    );
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
  });

  it('renders a single SSO button for the oauth login type', async () => {
    vi.mocked(fetchLoginType).mockResolvedValue('oauth' as LoginType);
    renderWithProviders(<LoginPage />);
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: /external authentication/i }),
      ).toBeInTheDocument(),
    );
  });

  it('marks the password field read-only for the none login type', async () => {
    vi.mocked(fetchLoginType).mockResolvedValue('none' as LoginType);
    renderWithProviders(<LoginPage />);
    await waitFor(() =>
      expect(screen.getByLabelText('Username')).toBeInTheDocument(),
    );
    expect(screen.getByLabelText('Password')).toHaveAttribute('readonly');
  });
});
