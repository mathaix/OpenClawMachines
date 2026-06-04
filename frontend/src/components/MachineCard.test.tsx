import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@/test/test-utils';
import { MachineCard } from './MachineCard';
import type { Machine } from '../lib/types';

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>();
  return {
    ...actual,
    startMachine: vi.fn(() => Promise.resolve({})),
    stopMachine: vi.fn(() => Promise.resolve({})),
    deleteMachine: vi.fn(() => Promise.resolve({})),
    getMachineModel: vi.fn(() => Promise.resolve({})),
    setMachineModel: vi.fn(() => Promise.resolve({})),
    pushMachineConfig: vi.fn(() => Promise.resolve({})),
    listMachineCapabilities: vi.fn(() => Promise.resolve([])),
    listMachineCredentials: vi.fn(() => Promise.resolve([])),
    authMe: vi.fn(() => Promise.reject(new Error('Unauthorized'))),
    listAccounts: vi.fn(() => Promise.resolve([])),
    listPendingInvitations: vi.fn(() => Promise.resolve([])),
  };
});

const baseMachine: Machine = {
  id: 'machine-abc-123',
  account_id: 1,
  name: 'My Bot',
  slug: 'my-bot',
  status: 'stopped',
  vcpus: 2,
  memory_mb: 2048,
  created_at: '2024-01-01T00:00:00Z',
};

describe('MachineCard', () => {
  it('should render machine name', () => {
    render(<MachineCard machine={baseMachine} />);

    expect(screen.getByText('My Bot')).toBeInTheDocument();
  });

  it('should render status badge', () => {
    render(<MachineCard machine={baseMachine} />);

    expect(screen.getByText('stopped')).toBeInTheDocument();
  });

  it('should show workspace and chat links when running', () => {
    const runningMachine: Machine = { ...baseMachine, status: 'running' };
    render(<MachineCard machine={runningMachine} />);

    const workspaceLink = screen.getByRole('link', { name: 'Workspace' });
    expect(workspaceLink).toHaveAttribute('href', '/workspace/machine-abc-123');

    const chatLink = screen.getByRole('link', { name: 'Chat' });
    expect(chatLink).toHaveAttribute('href', '/chat/machine-abc-123');
  });

  it('should show webchat and terminal links for running Hermes machines', () => {
    const runningMachine: Machine = { ...baseMachine, kind: 'hermes', status: 'running' };
    render(<MachineCard machine={runningMachine} />);

    const webchatLink = screen.getByRole('link', { name: 'Webchat' });
    expect(webchatLink).toHaveAttribute('href', '/chat/machine-abc-123');

    const terminalLink = screen.getByRole('link', { name: 'Terminal' });
    expect(terminalLink).toHaveAttribute('href', '/workspace/machine-abc-123');
  });

  it('should not show workspace or chat links when stopped', () => {
    render(<MachineCard machine={baseMachine} />);

    expect(screen.queryByRole('link', { name: 'Workspace' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Chat' })).not.toBeInTheDocument();
  });

  it('should render running status badge', () => {
    const runningMachine: Machine = { ...baseMachine, status: 'running' };
    render(<MachineCard machine={runningMachine} />);

    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it('should render error status badge', () => {
    const errorMachine: Machine = { ...baseMachine, status: 'error' };
    render(<MachineCard machine={errorMachine} />);

    expect(screen.getByText('error')).toBeInTheDocument();
  });

  it('should render provisioning status badge', () => {
    const provisioningMachine: Machine = { ...baseMachine, status: 'provisioning' };
    render(<MachineCard machine={provisioningMachine} />);

    expect(screen.getByText('provisioning')).toBeInTheDocument();
  });

  it('should show Start button enabled when stopped and accountId is provided', () => {
    render(<MachineCard machine={baseMachine} accountId={1} />);

    const startBtn = screen.getByText('Start');
    expect(startBtn).toBeInTheDocument();
    expect(startBtn).not.toBeDisabled();

    expect(screen.queryByText('Stop')).not.toBeInTheDocument();
  });

  it('should show Stop button enabled when running and accountId is provided', () => {
    const runningMachine: Machine = { ...baseMachine, status: 'running' };
    render(<MachineCard machine={runningMachine} accountId={1} />);

    const stopBtn = screen.getByText('Stop');
    expect(stopBtn).toBeInTheDocument();
    expect(stopBtn).not.toBeDisabled();

    expect(screen.queryByText('Start')).not.toBeInTheDocument();
  });

  it('should not show start/stop buttons without accountId', () => {
    render(<MachineCard machine={baseMachine} />);

    expect(screen.queryByText('Start')).not.toBeInTheDocument();
    expect(screen.queryByText('Stop')).not.toBeInTheDocument();
  });

  it('should show provision_step during provisioning', () => {
    const provMachine: Machine = { ...baseMachine, status: 'provisioning', provision_step: 'booting' };
    render(<MachineCard machine={provMachine} />);

    expect(screen.getByText('booting')).toBeInTheDocument();
  });

  it('should call startMachine on Start button click', async () => {
    const { startMachine: mockStart } = await import('../lib/api');
    const onStatusChange = vi.fn();
    render(<MachineCard machine={baseMachine} accountId={1} onStatusChange={onStatusChange} />);

    fireEvent.click(screen.getByText('Start'));

    expect(mockStart).toHaveBeenCalledWith(1, 'machine-abc-123');
  });
});
