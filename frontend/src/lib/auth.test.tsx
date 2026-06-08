import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import { AuthProvider, configuredCookieDomains, useAuth } from './auth';
import { ReactNode } from 'react';

// Mock the api module so we can control auth calls
vi.mock('./api', () => ({
  authMe: vi.fn(),
  listAccounts: vi.fn(),
  listPendingInvitations: vi.fn(),
}));

// Mock Firebase sign-out
vi.mock('./firebase', () => ({
  firebaseSignOut: vi.fn().mockResolvedValue(undefined),
}));

import { authMe, listAccounts, listPendingInvitations } from './api';
const mockAuthMe = vi.mocked(authMe);
const mockListAccounts = vi.mocked(listAccounts);
const mockListPendingInvitations = vi.mocked(listPendingInvitations);

const wrapper = ({ children }: { children: ReactNode }) => (
  <AuthProvider>{children}</AuthProvider>
);

const mockUser = {
  id: 1,
  email: 'test@example.com',
  name: 'Test User',
  auth_provider: 'email',
  created_at: '2024-01-01T00:00:00Z',
};

const mockAccount = {
  id: 1,
  name: 'Test Account',
  slug: 'test',
  created_by: 1,
  created_at: '2024-01-01T00:00:00Z',
};

describe('useAuth hook', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockListAccounts.mockResolvedValue([]);
    mockListPendingInvitations.mockResolvedValue([]);
    localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('should initialize without user when no cookie/session', async () => {
    mockAuthMe.mockRejectedValueOnce(new Error('Unauthorized'));

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.user).toBe(null);
    expect(mockAuthMe).toHaveBeenCalled();
  });

  it('should load user and accounts on mount', async () => {
    mockAuthMe.mockResolvedValueOnce({ user: mockUser });
    mockListAccounts.mockResolvedValueOnce([mockAccount]);

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.user).toEqual(mockUser);
    expect(result.current.account).toEqual(mockAccount);
    expect(result.current.accounts).toEqual([mockAccount]);
    expect(mockAuthMe).toHaveBeenCalled();
  });

  it('should remain unauthenticated when authMe fails on mount', async () => {
    mockAuthMe.mockRejectedValueOnce(new Error('Unauthorized'));

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.user).toBe(null);
  });

  it('should restore last active account from localStorage', async () => {
    const account2 = { ...mockAccount, id: 2, name: 'Second', slug: 'second' };
    mockAuthMe.mockResolvedValueOnce({ user: mockUser });
    mockListAccounts.mockResolvedValueOnce([mockAccount, account2]);
    vi.mocked(localStorage.getItem).mockImplementation((key: string) =>
      key === 'ocm_active_account_id' ? '2' : null,
    );

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.loading).toBe(false);
    });

    expect(result.current.account).toEqual(account2);
  });

  it('should fetch pending invitation count', async () => {
    mockAuthMe.mockResolvedValueOnce({ user: mockUser });
    mockListAccounts.mockResolvedValueOnce([mockAccount]);
    mockListPendingInvitations.mockResolvedValueOnce([
      { id: 1, account_id: 2, email: 'test@example.com', role: 'admin' as const, status: 'pending' as const, invited_by: 3, expires_at: '', created_at: '' },
    ]);

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.pendingInvitationCount).toBe(1);
    });
  });

  it('should logout by clearing user and navigating to /signed-out', async () => {
    mockAuthMe.mockResolvedValueOnce({ user: mockUser });

    const hrefSetter = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { href: '' },
      writable: true,
    });
    Object.defineProperty(window.location, 'href', {
      set: hrefSetter,
      get: () => '',
    });

    // Mock fetch for logout endpoint
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true });

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.user).toEqual(mockUser);
    });

    await act(async () => {
      result.current.logout();
    });

    expect(result.current.user).toBe(null);
    expect(hrefSetter).toHaveBeenCalledWith('/signed-out');
  });

  it('prefers explicit cookie domain for logout cleanup', () => {
    vi.stubEnv('VITE_DATA_PLANE_DOMAIN', 'example.com');
    vi.stubEnv('VITE_COOKIE_DOMAIN', '.auth.example.com');

    expect(configuredCookieDomains()).toEqual(['', '.auth.example.com']);
  });

  it('does not set a cookie domain for localhost logout cleanup', () => {
    vi.stubEnv('VITE_DATA_PLANE_DOMAIN', 'localhost');

    expect(configuredCookieDomains()).toEqual(['']);
  });
});
