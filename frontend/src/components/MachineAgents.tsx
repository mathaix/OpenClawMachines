import { useEffect, useState, useCallback } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { listMachineAgents, createMachineAgent, updateMachineAgent, deleteMachineAgent } from "../lib/api";
import type { MachineAgent, CreateAgentRequest } from "../lib/api";
import { useToast } from "./Toast";

interface MachineAgentsProps {
  accountId: number;
  machineId: string;
}

function generateAgentId(): string {
  return crypto.getRandomValues(new Uint8Array(4)).reduce(
    (s, b) => s + b.toString(16).padStart(2, "0"),
    "agent-",
  );
}

interface AgentFormData {
  agent_id: string;
  name: string;
  model: string;
  identity_emoji: string;
  identity_avatar: string;
  soul: string;
  is_default: boolean;
  sort_order: number;
}

const emptyForm = (): AgentFormData => ({
  agent_id: generateAgentId(),
  name: "",
  model: "",
  identity_emoji: "",
  identity_avatar: "",
  soul: "",
  is_default: false,
  sort_order: 0,
});

function agentToForm(agent: MachineAgent): AgentFormData {
  return {
    agent_id: agent.agent_id,
    name: agent.name,
    model: agent.model ?? "",
    identity_emoji: agent.identity_emoji ?? "",
    identity_avatar: agent.identity_avatar ?? "",
    soul: agent.soul ?? "",
    is_default: agent.is_default,
    sort_order: agent.sort_order,
  };
}

function formToCreateRequest(form: AgentFormData): CreateAgentRequest {
  return {
    agent_id: form.agent_id,
    name: form.name,
    model: form.model || undefined,
    identity_emoji: form.identity_emoji || undefined,
    identity_avatar: form.identity_avatar || undefined,
    soul: form.soul || undefined,
    is_default: form.is_default,
    sort_order: form.sort_order,
  };
}

function formToUpdateRequest(form: AgentFormData): Partial<CreateAgentRequest> {
  return {
    name: form.name,
    model: form.model || "",
    identity_emoji: form.identity_emoji || "",
    identity_avatar: form.identity_avatar || "",
    soul: form.soul || "",
    is_default: form.is_default,
    sort_order: form.sort_order,
  };
}

function AgentFormFields({
  form,
  onChange,
  isEdit,
}: {
  form: AgentFormData;
  onChange: (form: AgentFormData) => void;
  isEdit: boolean;
}) {
  return (
    <div className="space-y-4">
      {!isEdit && (
        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Agent ID</label>
          <input
            type="text"
            value={form.agent_id}
            onChange={(e) => onChange({ ...form, agent_id: e.target.value })}
            required
            className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
            placeholder="agent-abc123"
          />
        </div>
      )}

      <div>
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Name</label>
        <input
          type="text"
          value={form.name}
          onChange={(e) => onChange({ ...form, name: e.target.value })}
          required
          className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
          placeholder="My Agent"
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Emoji</label>
          <input
            type="text"
            value={form.identity_emoji}
            onChange={(e) => onChange({ ...form, identity_emoji: e.target.value })}
            className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
            placeholder="e.g. robot face"
          />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Model</label>
          <input
            type="text"
            value={form.model}
            onChange={(e) => onChange({ ...form, model: e.target.value })}
            className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
            placeholder="e.g. gpt-4o"
          />
        </div>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Avatar URL</label>
        <input
          type="text"
          value={form.identity_avatar}
          onChange={(e) => onChange({ ...form, identity_avatar: e.target.value })}
          className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
          placeholder="https://example.com/avatar.png"
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Soul (system prompt)</label>
        <textarea
          value={form.soul}
          onChange={(e) => onChange({ ...form, soul: e.target.value })}
          rows={3}
          className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm resize-y"
          placeholder="Describe the agent's personality and behavior..."
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div className="flex items-center gap-2">
          <input
            type="checkbox"
            id="is_default"
            checked={form.is_default}
            onChange={(e) => onChange({ ...form, is_default: e.target.checked })}
            className="h-4 w-4 rounded border-gray-300 text-brand-600 focus:ring-brand-500"
          />
          <label htmlFor="is_default" className="text-sm text-gray-700 dark:text-gray-300">Default agent</label>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">Sort Order</label>
          <input
            type="number"
            value={form.sort_order}
            onChange={(e) => onChange({ ...form, sort_order: parseInt(e.target.value) || 0 })}
            className="w-full border border-gray-300 dark:border-border bg-white dark:bg-surface-input text-gray-900 dark:text-gray-100 rounded-lg px-3 py-2 text-sm"
          />
        </div>
      </div>
    </div>
  );
}

export function MachineAgents({ accountId, machineId }: MachineAgentsProps) {
  const { toast } = useToast();
  const [agents, setAgents] = useState<MachineAgent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Dialog state
  const [createOpen, setCreateOpen] = useState(false);
  const [editAgent, setEditAgent] = useState<MachineAgent | null>(null);
  const [deleteAgent, setDeleteAgent] = useState<MachineAgent | null>(null);
  const [form, setForm] = useState<AgentFormData>(emptyForm);
  const [submitting, setSubmitting] = useState(false);

  const fetchAgents = useCallback(async () => {
    try {
      const data = await listMachineAgents(accountId, machineId);
      setAgents(data);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load agents");
    } finally {
      setLoading(false);
    }
  }, [accountId, machineId]);

  useEffect(() => {
    fetchAgents();
  }, [fetchAgents]);

  const handleCreate = async () => {
    if (!form.name.trim()) return;
    setSubmitting(true);
    try {
      await createMachineAgent(accountId, machineId, formToCreateRequest(form));
      toast({ title: "Agent created", variant: "success" });
      setCreateOpen(false);
      setForm(emptyForm());
      await fetchAgents();
    } catch (err) {
      toast({ title: "Failed to create agent", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setSubmitting(false);
    }
  };

  const handleUpdate = async () => {
    if (!editAgent || !form.name.trim()) return;
    setSubmitting(true);
    try {
      await updateMachineAgent(accountId, machineId, editAgent.agent_id, formToUpdateRequest(form));
      toast({ title: "Agent updated", variant: "success" });
      setEditAgent(null);
      await fetchAgents();
    } catch (err) {
      toast({ title: "Failed to update agent", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteAgent) return;
    setSubmitting(true);
    try {
      await deleteMachineAgent(accountId, machineId, deleteAgent.agent_id);
      toast({ title: "Agent deleted", variant: "success" });
      setDeleteAgent(null);
      await fetchAgents();
    } catch (err) {
      toast({ title: "Failed to delete agent", description: err instanceof Error ? err.message : "Unknown error", variant: "error" });
    } finally {
      setSubmitting(false);
    }
  };

  const openEdit = (agent: MachineAgent) => {
    setForm(agentToForm(agent));
    setEditAgent(agent);
  };

  const openCreate = () => {
    setForm(emptyForm());
    setCreateOpen(true);
  };

  if (loading) return <p className="text-sm text-gray-500 dark:text-gray-400">Loading agents...</p>;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-medium text-gray-900 dark:text-gray-100">Agents</h3>
        <button
          onClick={openCreate}
          className="bg-brand-600 text-white px-3 py-1.5 rounded-lg text-sm font-medium hover:bg-brand-700"
        >
          Create Agent
        </button>
      </div>

      {error && (
        <p className="text-sm text-red-600 dark:text-red-400">{error}</p>
      )}

      {agents.length === 0 && !error && (
        <p className="text-sm text-gray-500 dark:text-gray-400">No agents configured. Create one to get started.</p>
      )}

      <div className="space-y-2">
        {agents.map((agent) => (
          <div
            key={agent.id}
            className="flex items-center justify-between bg-gray-50 dark:bg-surface-deep rounded-lg px-4 py-3 border border-gray-200 dark:border-border"
          >
            <div className="flex items-center gap-3 min-w-0">
              {agent.identity_emoji && (
                <span className="text-xl flex-shrink-0">{agent.identity_emoji}</span>
              )}
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100 truncate">{agent.name}</p>
                  {agent.is_default && (
                    <span className="inline-flex items-center rounded-full bg-brand-100 dark:bg-brand-900/30 px-2 py-0.5 text-xs font-medium text-brand-700 dark:text-brand-400">
                      Default
                    </span>
                  )}
                </div>
                <p className="text-xs text-gray-500 dark:text-gray-400 truncate">
                  {agent.agent_id}
                  {agent.model && <span className="ml-2">{agent.model}</span>}
                </p>
              </div>
            </div>

            <div className="flex items-center gap-1 flex-shrink-0 ml-2">
              <button
                onClick={() => openEdit(agent)}
                className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 p-1 rounded"
                title="Edit"
              >
                <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
                </svg>
              </button>
              {!agent.is_default && (
                <button
                  onClick={() => setDeleteAgent(agent)}
                  className="text-gray-400 hover:text-red-600 dark:hover:text-red-400 p-1 rounded"
                  title="Delete"
                >
                  <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                  </svg>
                </button>
              )}
            </div>
          </div>
        ))}
      </div>

      {/* Create Agent Dialog */}
      <Dialog.Root open={createOpen} onOpenChange={setCreateOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/50 z-50" />
          <Dialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-white dark:bg-surface-card border border-transparent dark:border-border rounded-lg shadow-lg p-6 w-full max-w-lg z-50 max-h-[85vh] overflow-y-auto">
            <Dialog.Title className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-4">
              Create Agent
            </Dialog.Title>

            <form onSubmit={(e) => { e.preventDefault(); handleCreate(); }}>
              <AgentFormFields form={form} onChange={setForm} isEdit={false} />

              <div className="flex gap-3 mt-6">
                <button
                  type="submit"
                  disabled={submitting || !form.name.trim()}
                  className="flex-1 bg-brand-600 text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-brand-700 disabled:opacity-50"
                >
                  {submitting ? "Creating..." : "Create"}
                </button>
                <Dialog.Close asChild>
                  <button
                    type="button"
                    className="px-4 py-2 rounded-lg text-sm font-medium border border-gray-300 dark:border-border text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-surface-elevated"
                  >
                    Cancel
                  </button>
                </Dialog.Close>
              </div>
            </form>

            <Dialog.Close asChild>
              <button
                className="absolute top-4 right-4 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                aria-label="Close"
              >
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                </svg>
              </button>
            </Dialog.Close>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>

      {/* Edit Agent Dialog */}
      <Dialog.Root open={!!editAgent} onOpenChange={(open) => { if (!open) setEditAgent(null); }}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/50 z-50" />
          <Dialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-white dark:bg-surface-card border border-transparent dark:border-border rounded-lg shadow-lg p-6 w-full max-w-lg z-50 max-h-[85vh] overflow-y-auto">
            <Dialog.Title className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-4">
              Edit Agent
            </Dialog.Title>

            <form onSubmit={(e) => { e.preventDefault(); handleUpdate(); }}>
              <AgentFormFields form={form} onChange={setForm} isEdit={true} />

              <div className="flex gap-3 mt-6">
                <button
                  type="submit"
                  disabled={submitting || !form.name.trim()}
                  className="flex-1 bg-brand-600 text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-brand-700 disabled:opacity-50"
                >
                  {submitting ? "Saving..." : "Save Changes"}
                </button>
                <Dialog.Close asChild>
                  <button
                    type="button"
                    className="px-4 py-2 rounded-lg text-sm font-medium border border-gray-300 dark:border-border text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-surface-elevated"
                  >
                    Cancel
                  </button>
                </Dialog.Close>
              </div>
            </form>

            <Dialog.Close asChild>
              <button
                className="absolute top-4 right-4 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                aria-label="Close"
              >
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                </svg>
              </button>
            </Dialog.Close>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>

      {/* Delete Confirmation Dialog */}
      <Dialog.Root open={!!deleteAgent} onOpenChange={(open) => { if (!open) setDeleteAgent(null); }}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/50 z-50" />
          <Dialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-white dark:bg-surface-card border border-transparent dark:border-border rounded-lg shadow-lg p-6 w-full max-w-sm z-50">
            <Dialog.Title className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-2">
              Delete Agent
            </Dialog.Title>
            <p className="text-sm text-gray-600 dark:text-gray-400 mb-4">
              Are you sure you want to delete <strong>{deleteAgent?.name}</strong>? This action cannot be undone.
            </p>
            <div className="flex justify-end gap-2">
              <Dialog.Close asChild>
                <button className="px-4 py-2 text-sm font-medium text-gray-700 dark:text-gray-300 border border-gray-300 dark:border-border rounded-lg hover:bg-gray-50 dark:hover:bg-surface-elevated">
                  Cancel
                </button>
              </Dialog.Close>
              <button
                onClick={handleDelete}
                disabled={submitting}
                className="px-4 py-2 text-sm font-medium text-white bg-red-600 rounded-lg hover:bg-red-700 disabled:opacity-50"
              >
                {submitting ? "Deleting..." : "Delete"}
              </button>
            </div>

            <Dialog.Close asChild>
              <button
                className="absolute top-4 right-4 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                aria-label="Close"
              >
                <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                </svg>
              </button>
            </Dialog.Close>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </div>
  );
}
