import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, act, waitFor } from '@/test/test-utils';
import { ResourcesTab } from './ResourcesTab';
import type { Machine, VMMetricsResponse } from '../../lib/types';

vi.mock('../../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../lib/api')>();
  return {
    ...actual,
    authMe: vi.fn(() => Promise.reject(new Error('Unauthorized'))),
    listAccounts: vi.fn(() => Promise.resolve([])),
    listPendingInvitations: vi.fn(() => Promise.resolve([])),
    getMachineMetrics: vi.fn(),
  };
});

// jsdom has no layout — Recharts ResponsiveContainer would render 0×0 and emit
// dev warnings. Replace its components with simple DOM markers; assertions
// target shape (chart present? correct points?) not visual rendering.
vi.mock('recharts', () => {
  const Pass = ({ children }: { children?: React.ReactNode }) => <div>{children}</div>;
  return {
    ResponsiveContainer: Pass,
    LineChart: ({ data, children }: { data?: unknown[]; children?: React.ReactNode }) => (
      <div data-testid="line-chart" data-points={(data ?? []).length}>{children}</div>
    ),
    Line: () => <div data-testid="line" />,
    XAxis: () => <div />,
    YAxis: () => <div />,
    Tooltip: () => <div />,
    CartesianGrid: () => <div />,
    Brush: () => <div data-testid="brush" />,
  };
});

import { getMachineMetrics } from '../../lib/api';

const baseMachine: Machine = {
  id: 'm-1',
  account_id: 1,
  name: 'Test',
  slug: 'test',
  status: 'running',
  vcpus: 2,
  memory_mb: 2048,
  created_at: '2026-04-29T00:00:00Z',
};

function makeResponse(points: { ts: string; cpu: number | null; mem: number }[], resolution: '1s' | '1m' | 'mixed' = '1s'): VMMetricsResponse {
  return {
    vm_id: 'm-1',
    kind: 'machine',
    resolution,
    from: '2026-04-29T00:00:00Z',
    to: '2026-04-29T00:00:05Z',
    points: points.map((p) => ({ ts: p.ts, cpu_pct: p.cpu, mem_bytes: p.mem })),
  };
}

const mockedGet = getMachineMetrics as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => {
  mockedGet.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

function recentTs(secondsAgo: number): string {
  return new Date(Date.now() - secondsAgo * 1000).toISOString();
}

describe('ResourcesTab — Live view', () => {
  it('renders Live sub-tab by default and shows points from initial fetch', async () => {
    mockedGet.mockResolvedValueOnce(makeResponse([
      { ts: recentTs(5), cpu: 12.5, mem: 256 * 1024 * 1024 },
      { ts: recentTs(4), cpu: 25.0, mem: 280 * 1024 * 1024 },
      { ts: recentTs(3), cpu: 40.0, mem: 290 * 1024 * 1024 },
      { ts: recentTs(2), cpu: 55.5, mem: 310 * 1024 * 1024 },
      { ts: recentTs(1), cpu: 80.0, mem: 320 * 1024 * 1024 },
    ]));

    render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);

    await waitFor(() => {
      const charts = screen.getAllByTestId('line-chart');
      expect(charts.length).toBe(2); // CPU + Memory
      expect(charts[0]).toHaveAttribute('data-points', '5');
    });

    // First call is the cursor seed (no since param, server defaults to last 5min)
    expect(mockedGet).toHaveBeenCalledWith(1, 'm-1', expect.objectContaining({}));
    // CPU big-number reflects most recent point
    expect(screen.getByText(/80%/)).toBeInTheDocument();
    // CPU denominator: vcpus * 100 = 200
    expect(screen.getByText(/of 200%/)).toBeInTheDocument();
    // Memory denominator
    expect(screen.getByText(/of 2,048 MB/)).toBeInTheDocument();
  });

  it('polls every second with since cursor advancing', async () => {
    vi.useFakeTimers();
    mockedGet.mockResolvedValue(makeResponse([
      { ts: '2026-04-29T00:00:05Z', cpu: 10, mem: 100 * 1024 * 1024 },
    ]));

    render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);

    // Wait for the initial fetch to land
    await act(async () => {
      await Promise.resolve();
    });
    expect(mockedGet).toHaveBeenCalledTimes(1);

    // Advance 1s → second fetch; this time should pass since=last ts
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(mockedGet).toHaveBeenCalledTimes(2);
    const secondCallArgs = mockedGet.mock.calls[1];
    expect(secondCallArgs[2]).toEqual({ since: '2026-04-29T00:00:05Z' });

    // Another tick reuses same cursor when no new points
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(mockedGet).toHaveBeenCalledTimes(3);
    expect(mockedGet.mock.calls[2][2]).toEqual({ since: '2026-04-29T00:00:05Z' });
  });

  it('clears polling interval on unmount', async () => {
    vi.useFakeTimers();
    mockedGet.mockResolvedValue(makeResponse([]));

    const { unmount } = render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    const before = mockedGet.mock.calls.length;

    unmount();
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(mockedGet.mock.calls.length).toBe(before);
  });

  it('shows reconnecting indicator on fetch failure and clears it on recovery', async () => {
    vi.useFakeTimers();
    mockedGet.mockResolvedValueOnce(makeResponse([
      { ts: '2026-04-29T00:00:01Z', cpu: 10, mem: 100 * 1024 * 1024 },
    ]));

    render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    // Next tick: fail
    mockedGet.mockRejectedValueOnce(new Error('network'));
    await act(async () => {
      vi.advanceTimersByTime(1000);
      await Promise.resolve();
    });
    expect(screen.getByText(/reconnecting/i)).toBeInTheDocument();

    // Following tick: recover
    mockedGet.mockResolvedValueOnce(makeResponse([
      { ts: '2026-04-29T00:00:02Z', cpu: 20, mem: 200 * 1024 * 1024 },
    ]));
    await act(async () => {
      vi.advanceTimersByTime(1000);
      await Promise.resolve();
    });
    expect(screen.queryByText(/reconnecting/i)).not.toBeInTheDocument();
  });

  it('shows "Waiting for first sample…" when no points yet', async () => {
    mockedGet.mockResolvedValueOnce(makeResponse([]));
    render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);
    await waitFor(() => {
      expect(screen.getByText(/waiting for first sample/i)).toBeInTheDocument();
    });
  });
});

describe('ResourcesTab — History view', () => {
  it('switches to History sub-tab and shows resolution badge', async () => {
    vi.useFakeTimers();
    // Initial Live fetch
    mockedGet.mockResolvedValueOnce(makeResponse([
      { ts: recentTs(1), cpu: 10, mem: 100 * 1024 * 1024 },
    ]));
    render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    // History fetch (default range: last 24h → 1m)
    mockedGet.mockResolvedValueOnce(
      makeResponse(
        Array.from({ length: 60 }, (_, i) => ({
          ts: new Date(Date.UTC(2026, 3, 29, 0, i)).toISOString(),
          cpu: 5 + i,
          mem: (100 + i) * 1024 * 1024,
        })),
        '1m',
      ),
    );

    fireEvent.click(screen.getByRole('button', { name: /history/i }));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(screen.getByText(/1-minute/i)).toBeInTheDocument();
    const charts = screen.getAllByTestId('line-chart');
    expect(charts.length).toBeGreaterThanOrEqual(2);
  });

  it('range picker swaps resolution from 1-minute to 1-second when range ≤ 1h', async () => {
    // Route mocks by call shape so live polling can't consume history mocks.
    mockedGet.mockImplementation((_acct, _id, params: { since?: string; from?: string; to?: string }) => {
      if (params?.since !== undefined || (params?.from === undefined && params?.to === undefined)) {
        // Live call (cursor or first-time seed with no params)
        return Promise.resolve(makeResponse([{ ts: recentTs(1), cpu: 10, mem: 1 }]));
      }
      // History call — span derived from from/to
      const span = new Date(params.to!).getTime() - new Date(params.from!).getTime();
      const res = span <= 60 * 60 * 1000 ? '1s' : '1m';
      return Promise.resolve(makeResponse([], res));
    });

    render(<ResourcesTab kind="machine" vm={baseMachine} accountId={1} />);

    fireEvent.click(screen.getByRole('button', { name: /history/i }));
    await waitFor(() => expect(screen.getByText(/1-minute/i)).toBeInTheDocument());

    fireEvent.click(screen.getByRole('button', { name: /^last 1 h$/i }));
    await waitFor(() => expect(screen.getByText(/1-second/i)).toBeInTheDocument());
  });
});
