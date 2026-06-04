import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { CreateMachineModal } from './CreateMachineModal';

const mockUseAuth = vi.fn();

vi.mock('../lib/auth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/auth')>();
  return {
    ...actual,
    useAuth: () => mockUseAuth(),
  };
});

vi.mock('../lib/useSizes', () => ({
  useSizes: () => [
    { id: 'standard', label: 'Standard', description: '2 vCPU / 4GB', is_default: true, vcpus: 2, memory_mb: 4096, disk_gb: 20 },
  ],
}));

vi.mock('../lib/useRegions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/useRegions')>();
  return {
    ...actual,
    useRegions: () => [{ code: 'us-central1', name: 'Iowa', available: true }],
  };
});

const apiMocks = vi.hoisted(() => ({
  createMachine: vi.fn(() => Promise.resolve({ id: 'm-1' })),
  listOpenClawReleases: vi.fn(() =>
    Promise.resolve([
      { version: 'v2026.4', exact_version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28' },
    ]),
  ),
  listRootfsReleases: vi.fn(() =>
    Promise.resolve([
      { version: 'v2026.4.26-r2', exact_version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28' },
    ]),
  ),
  listAdminOpenClawReleases: vi.fn(() =>
    Promise.resolve({
      releases: [
        { version: 'v2026.5.2-r1', channel: 'rc', created_at: '2026-05-04T23:51:15Z' },
        { version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28T19:15:15Z' },
        { version: 'v2026.4.2-r10', channel: 'stable', created_at: '2026-04-02T00:00:00Z' },
      ],
    }),
  ),
  listAdminRootfsReleases: vi.fn(() =>
    Promise.resolve({
      releases: [
        { version: 'v2026.5.2-r1', channel: 'rc', created_at: '2026-05-04T23:51:15Z' },
        { version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28T19:15:15Z' },
      ],
    }),
  ),
}));

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>();
  return {
    ...actual,
    ...apiMocks,
  };
});

const onOpenChange = vi.fn();
const onCreated = vi.fn();

beforeEach(() => {
  vi.clearAllMocks();
  apiMocks.createMachine.mockResolvedValue({ id: 'm-1' });
  apiMocks.listOpenClawReleases.mockResolvedValue([
    { version: 'v2026.4', exact_version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28' },
  ]);
  apiMocks.listRootfsReleases.mockResolvedValue([
    { version: 'v2026.4.26-r2', exact_version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28' },
  ]);
  apiMocks.listAdminOpenClawReleases.mockResolvedValue({
    releases: [
      { version: 'v2026.5.2-r1', channel: 'rc', created_at: '2026-05-04T23:51:15Z' },
      { version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28T19:15:15Z' },
      { version: 'v2026.4.2-r10', channel: 'stable', created_at: '2026-04-02T00:00:00Z' },
    ],
  });
  apiMocks.listAdminRootfsReleases.mockResolvedValue({
    releases: [
      { version: 'v2026.5.2-r1', channel: 'rc', created_at: '2026-05-04T23:51:15Z' },
      { version: 'v2026.4.26-r2', channel: 'stable', created_at: '2026-04-28T19:15:15Z' },
    ],
  });
});

const baseAuth = {
  user: null,
  account: null,
  accounts: [],
  loading: false,
  accountError: false,
  pendingInvitationCount: 0,
  setAccount: vi.fn(),
  refreshAccounts: vi.fn(),
  logout: vi.fn(),
};

describe('CreateMachineModal', () => {
  it('non-admin: fetches stable-only user endpoints', async () => {
    mockUseAuth.mockReturnValue({ ...baseAuth, isAdmin: false });

    render(
      <CreateMachineModal open={true} onOpenChange={onOpenChange} accountId={1} onCreated={onCreated} />,
    );

    await waitFor(() => {
      expect(apiMocks.listOpenClawReleases).toHaveBeenCalledWith(1);
    });
    expect(apiMocks.listRootfsReleases).toHaveBeenCalledWith(1);
    expect(apiMocks.listAdminOpenClawReleases).not.toHaveBeenCalled();
    expect(apiMocks.listAdminRootfsReleases).not.toHaveBeenCalled();

    await screen.findByText('v2026.4');
    expect(screen.queryByText(/v2026\.5\.2-r1/)).not.toBeInTheDocument();
  });

  it('admin: fetches admin endpoints and populates dropdowns with rc + stable options', async () => {
    mockUseAuth.mockReturnValue({ ...baseAuth, isAdmin: true });

    render(
      <CreateMachineModal open={true} onOpenChange={onOpenChange} accountId={1} onCreated={onCreated} />,
    );

    await waitFor(() => {
      expect(apiMocks.listAdminOpenClawReleases).toHaveBeenCalledTimes(1);
    });
    expect(apiMocks.listAdminRootfsReleases).toHaveBeenCalledTimes(1);
    expect(apiMocks.listOpenClawReleases).not.toHaveBeenCalled();
    expect(apiMocks.listRootfsReleases).not.toHaveBeenCalled();

    const openclawSelect = (await screen.findByLabelText(/OpenClaw Version/i)) as HTMLSelectElement;
    await waitFor(() => {
      expect(openclawSelect.options.length).toBe(4); // 1 placeholder + 3 releases
    });
    const openclawLabels = Array.from(openclawSelect.options).map((o) => o.textContent);
    expect(openclawLabels).toContain('v2026.5.2-r1 (rc)');
    expect(openclawLabels).toContain('v2026.4.26-r2 (stable)');
    expect(openclawLabels).toContain('v2026.4.2-r10 (stable)');

    const rootfsSelect = screen.getByLabelText(/Rootfs Version/i) as HTMLSelectElement;
    const rootfsLabels = Array.from(rootfsSelect.options).map((o) => o.textContent);
    expect(rootfsLabels).toContain('v2026.5.2-r1 (rc)');
    expect(rootfsLabels).toContain('v2026.4.26-r2 (stable)');
  });

  it('admin: selecting an rc version and submitting posts openclaw_version', async () => {
    mockUseAuth.mockReturnValue({ ...baseAuth, isAdmin: true });

    render(
      <CreateMachineModal open={true} onOpenChange={onOpenChange} accountId={1} onCreated={onCreated} />,
    );

    const openclawSelect = (await screen.findByLabelText(/OpenClaw Version/i)) as HTMLSelectElement;
    await waitFor(() => expect(openclawSelect.options.length).toBeGreaterThan(1));

    fireEvent.change(openclawSelect, { target: { value: 'v2026.5.2-r1' } });

    const submit = screen.getByRole('button', { name: /Create/i });
    fireEvent.click(submit);

    await waitFor(() => {
      expect(apiMocks.createMachine).toHaveBeenCalledTimes(1);
    });
    const [, payload] = apiMocks.createMachine.mock.calls[0];
    expect(payload.openclaw_version).toBe('v2026.5.2-r1');
  });
});
