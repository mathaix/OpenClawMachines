import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  authMe,
  listAccounts,
  createAccount,
  getAccount,
  listMachines,
  getMachine,
  createMachine,
  listRegions,
  updateMachine,
  deleteMachine,
  startMachine,
  stopMachine,
  triggerHostUpdate,
  drainHostUpdate,
  configureHostBrowserStorage,
} from './api';
import { ApiError } from './errors';

// Mock fetch
const mockFetch = vi.fn();
global.fetch = mockFetch;

// Mock localStorage
const localStorageMock = {
  store: {} as Record<string, string>,
  getItem: vi.fn((key: string) => localStorageMock.store[key] || null),
  setItem: vi.fn((key: string, value: string) => {
    localStorageMock.store[key] = value;
  }),
  removeItem: vi.fn((key: string) => {
    delete localStorageMock.store[key];
  }),
  clear: vi.fn(() => {
    localStorageMock.store = {};
  }),
};

Object.defineProperty(window, 'localStorage', {
  value: localStorageMock,
});

describe('API client', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorageMock.clear();
    localStorageMock.store = {};
  });

  describe('request helper', () => {
    it('should use credentials include for cookie auth', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => [],
      });

      await listAccounts();

      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts',
        expect.objectContaining({
          credentials: 'include',
        })
      );
      // No Authorization header — auth is via HttpOnly cookie
      const callArgs = mockFetch.mock.calls[0][1];
      expect(callArgs.headers.Authorization).toBeUndefined();
    });

    it('should handle HTTP error with JSON error body', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: false,
        status: 400,
        statusText: 'Bad Request',
        json: async () => ({ error: 'invalid request body' }),
      });

      await expect(listAccounts()).rejects.toThrow('invalid request body');

      // Verify ApiError properties
      mockFetch.mockResolvedValueOnce({
        ok: false,
        status: 400,
        statusText: 'Bad Request',
        json: async () => ({ error: 'invalid request body' }),
      });

      try {
        await listAccounts();
        expect.fail('should have thrown');
      } catch (err) {
        expect(err).toBeInstanceOf(ApiError);
        expect((err as ApiError).status).toBe(400);
        expect((err as ApiError).retryable).toBe(false);
      }
    });

    it('should handle HTTP error with non-JSON body', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        json: async () => {
          throw new Error('not json');
        },
      });

      await expect(listAccounts()).rejects.toThrow('Internal Server Error');

      // Verify ApiError properties
      mockFetch.mockResolvedValueOnce({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        json: async () => {
          throw new Error('not json');
        },
      });

      try {
        await listAccounts();
        expect.fail('should have thrown');
      } catch (err) {
        expect(err).toBeInstanceOf(ApiError);
        expect((err as ApiError).status).toBe(500);
        expect((err as ApiError).retryable).toBe(true);
      }
    });

    it('should handle 204 No Content', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        json: async () => {
          throw new Error('no body');
        },
      });

      const result = await deleteMachine(1, 'machine-123');

      expect(result).toBeUndefined();
    });

    it('should handle network errors', async () => {
      mockFetch.mockRejectedValueOnce(new Error('Network error'));

      await expect(listAccounts()).rejects.toThrow('Network error');
    });
  });

  describe('admin host actions', () => {
    it('should trigger a hot agent update', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ status: 'updated', message: 'ok' }),
      });

      const result = await triggerHostUpdate(42);

      expect(result.status).toBe('updated');
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/admin/hosts/42/trigger-update',
        expect.objectContaining({ method: 'POST' }),
      );
    });

    it('should trigger a drain-and-update flow', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ status: 'updated', message: 'ok' }),
      });

      const result = await drainHostUpdate(42);

      expect(result.status).toBe('updated');
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/admin/hosts/42/drain-update',
        expect.objectContaining({ method: 'POST' }),
      );
    });

    it('should configure browser storage on a host', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 202,
        json: async () => ({ status: 'queued', operation_id: 'hostupd-123' }),
      });

      const result = await configureHostBrowserStorage(42, { device: '/dev/nvme1n1', format: true });

      expect(result.operation_id).toBe('hostupd-123');
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/admin/hosts/42/configure-browser-storage',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ device: '/dev/nvme1n1', format: true }),
        }),
      );
    });
  });

  describe('auth', () => {
    it('should get authenticated user via authMe', async () => {
      localStorageMock.store['token'] = 'saved-token';
      const mockUser = {
        id: 1,
        email: 'test@example.com',
        name: 'Test User',
        auth_provider: 'email',
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ user: mockUser }),
      });

      const result = await authMe();

      expect(result).toEqual({ user: mockUser });
      expect(mockFetch).toHaveBeenCalledWith('/api/auth/me', expect.any(Object));
    });
  });

  describe('accounts', () => {
    it('should list accounts', async () => {
      const mockAccounts = [
        { id: 1, name: 'Acme', slug: 'acme', created_by: 1, created_at: '2024-01-01T00:00:00Z' },
      ];

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockAccounts,
      });

      const result = await listAccounts();

      expect(result).toEqual(mockAccounts);
      expect(mockFetch).toHaveBeenCalledWith('/api/accounts', expect.any(Object));
    });

    it('should create an account', async () => {
      const mockAccount = {
        id: 2,
        name: 'New Account',
        slug: 'new-account',
        created_by: 1,
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockAccount,
      });

      const result = await createAccount({ name: 'New Account', slug: 'new-account' });

      expect(result).toEqual(mockAccount);
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ name: 'New Account', slug: 'new-account' }),
        })
      );
    });

    it('should get a single account', async () => {
      const mockAccount = {
        id: 1,
        name: 'Acme',
        slug: 'acme',
        created_by: 1,
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockAccount,
      });

      const result = await getAccount(1);

      expect(result).toEqual(mockAccount);
      expect(mockFetch).toHaveBeenCalledWith('/api/accounts/1', expect.any(Object));
    });
  });

  describe('machines', () => {
    it('should list machines for an account', async () => {
      const mockMachines = [
        {
          id: 'machine-1',
          account_id: 1,
          name: 'My Bot',
          slug: 'my-bot',
          status: 'stopped',
          vcpus: 2,
          memory_mb: 2048,
          created_at: '2024-01-01T00:00:00Z',
        },
      ];

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockMachines,
      });

      const result = await listMachines(1);

      expect(result).toEqual(mockMachines);
      expect(mockFetch).toHaveBeenCalledWith('/api/accounts/1/machines', expect.any(Object));
    });

    it('should get a single machine', async () => {
      const mockMachine = {
        id: 'machine-1',
        account_id: 1,
        name: 'My Bot',
        slug: 'my-bot',
        status: 'running',
        vcpus: 2,
        memory_mb: 2048,
        host_id: 5,
        vm_ip: '192.168.100.10',
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockMachine,
      });

      const result = await getMachine(1, 'machine-1');

      expect(result).toEqual(mockMachine);
      expect(mockFetch).toHaveBeenCalledWith('/api/accounts/1/machines/machine-1', expect.any(Object));
    });

    it('should create a machine', async () => {
      const mockMachine = {
        id: 'machine-new',
        account_id: 1,
        name: 'New Bot',
        slug: 'new-bot',
        status: 'stopped',
        vcpus: 4,
        memory_mb: 4096,
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockMachine,
      });

      const result = await createMachine(1, {
        name: 'New Bot',
        size: 'pro',
        preferred_region: 'us-west',
      });

      expect(result).toEqual(mockMachine);
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts/1/machines',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ name: 'New Bot', size: 'pro', preferred_region: 'us-west' }),
        })
      );
    });

    it('should pass optional runtime selection fields on machine create', async () => {
      const mockMachine = {
        id: 'machine-new',
        account_id: 1,
        name: 'New Bot',
        slug: 'new-bot',
        status: 'stopped',
        vcpus: 4,
        memory_mb: 4096,
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockMachine,
      });

      await createMachine(1, {
        name: 'New Bot',
        size: 'pro',
        rootfs_version: 'rootfs-2026.04.01',
        openclaw_version: 'openclaw-1.2.3',
        runtime_source: 'artifact',
      });

      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts/1/machines',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({
            name: 'New Bot',
            size: 'pro',
            rootfs_version: 'rootfs-2026.04.01',
            openclaw_version: 'openclaw-1.2.3',
            runtime_source: 'artifact',
          }),
        })
      );
    });

    it('should list regions', async () => {
      const mockRegions = [
        { code: 'us-west', name: 'US West', available: true },
        { code: 'eu-central', name: 'EU Central', available: true },
      ];

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockRegions,
      });

      const result = await listRegions();

      expect(result).toEqual(mockRegions);
      expect(mockFetch).toHaveBeenCalledWith('/api/regions', expect.any(Object));
    });

    it('should update a machine', async () => {
      const mockMachine = {
        id: 'machine-1',
        account_id: 1,
        name: 'Updated Bot',
        slug: 'my-bot',
        status: 'stopped',
        vcpus: 4,
        memory_mb: 4096,
        created_at: '2024-01-01T00:00:00Z',
      };

      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => mockMachine,
      });

      const result = await updateMachine(1, 'machine-1', { name: 'Updated Bot', vcpus: 4 });

      expect(result).toEqual(mockMachine);
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts/1/machines/machine-1',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify({ name: 'Updated Bot', vcpus: 4 }),
        })
      );
    });

    it('should delete a machine', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
      });

      await deleteMachine(1, 'machine-1');

      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts/1/machines/machine-1',
        expect.objectContaining({ method: 'DELETE' })
      );
    });

    it('should start a machine', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          status: 'provisioning',
          host_id: 5,
          vm_ip: '192.168.100.10',
        }),
      });

      const result = await startMachine(1, 'machine-1');

      expect(result.status).toBe('provisioning');
      expect(result.host_id).toBe(5);
      expect(result.vm_ip).toBe('192.168.100.10');
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts/1/machines/machine-1/start',
        expect.objectContaining({ method: 'POST' })
      );
    });

    it('should stop a machine', async () => {
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ status: 'stopped' }),
      });

      const result = await stopMachine(1, 'machine-1');

      expect(result.status).toBe('stopped');
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/accounts/1/machines/machine-1/stop',
        expect.objectContaining({ method: 'POST' })
      );
    });
  });
});
