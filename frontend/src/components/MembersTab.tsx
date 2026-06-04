import { useEffect, useState, useCallback } from "react";
import { listMembers, listInvitations, createInvitation, revokeInvitation, updateMemberRole, removeMember, leaveAccount } from "../lib/api";
import type { AccountMember, AccountInvitation } from "../lib/types";
import { useAuth } from "../lib/auth";
import { useToast } from "./Toast";

interface MembersTabProps {
  accountId: number;
}

export function MembersTab({ accountId }: MembersTabProps) {
  const { user } = useAuth();
  const { toast } = useToast();

  const [members, setMembers] = useState<AccountMember[]>([]);
  const [invitations, setInvitations] = useState<AccountInvitation[]>([]);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [pendingRoles, setPendingRoles] = useState<Set<number>>(new Set());

  const currentMember = members.find((m) => m.user_id === user?.id);
  const isOwnerOrAdmin = currentMember?.role === "owner" || currentMember?.role === "admin";
  const isOwner = currentMember?.role === "owner";

  const fetchData = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const [membersData, invitationsData] = await Promise.all([
        listMembers(accountId),
        listInvitations(accountId).catch(() => [] as AccountInvitation[]),
      ]);
      setMembers(membersData || []);
      setInvitations((invitationsData || []).filter((i) => i.status === "pending"));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load members");
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const handleRoleChange = async (userId: number, role: string) => {
    if (pendingRoles.has(userId)) return;
    setPendingRoles((prev) => new Set(prev).add(userId));
    try {
      await updateMemberRole(accountId, userId, role);
      toast({ title: "Role updated", variant: "success" });
      fetchData();
    } catch (err) {
      toast({ title: err instanceof Error ? err.message : "Failed to update role", variant: "error" });
    } finally {
      setPendingRoles((prev) => {
        const next = new Set(prev);
        next.delete(userId);
        return next;
      });
    }
  };

  const handleRemoveMember = async (userId: number, email?: string) => {
    if (!confirm(`Remove ${email || "this member"} from the account?`)) return;
    try {
      await removeMember(accountId, userId);
      toast({ title: "Member removed", variant: "success" });
      fetchData();
    } catch (err) {
      toast({ title: err instanceof Error ? err.message : "Failed to remove member", variant: "error" });
    }
  };

  const handleLeave = async () => {
    if (!confirm("Are you sure you want to leave this account?")) return;
    try {
      await leaveAccount(accountId);
      toast({ title: "You have left the account", variant: "success" });
      window.location.reload();
    } catch (err) {
      toast({ title: err instanceof Error ? err.message : "Failed to leave account", variant: "error" });
    }
  };

  const handleRevokeInvitation = async (invitationId: number) => {
    try {
      await revokeInvitation(accountId, invitationId);
      toast({ title: "Invitation revoked", variant: "success" });
      fetchData();
    } catch (err) {
      toast({ title: err instanceof Error ? err.message : "Failed to revoke invitation", variant: "error" });
    }
  };

  const handleSendInvite = async () => {
    if (!inviteEmail.trim()) return;
    try {
      await createInvitation(accountId, { email: inviteEmail.trim(), role: inviteRole });
      toast({ title: `Invitation sent to ${inviteEmail.trim()}`, variant: "success" });
      setInviteEmail("");
      setInviteRole("member");
      setInviteOpen(false);
      fetchData();
    } catch (err) {
      toast({ title: err instanceof Error ? err.message : "Failed to create invitation", variant: "error" });
    }
  };

  const closeInviteDialog = () => {
    setInviteOpen(false);
    setInviteEmail("");
    setInviteRole("member");
  };

  const daysUntil = (dateStr: string) => {
    const diff = new Date(dateStr).getTime() - Date.now();
    const days = Math.ceil(diff / (1000 * 60 * 60 * 24));
    return days > 0 ? days : 0;
  };

  const roleBadge = (role: string) => {
    const colors: Record<string, string> = {
      owner: "bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300",
      admin: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300",
      member: "bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300",
    };
    return (
      <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${colors[role] || colors.member}`}>
        {role}
      </span>
    );
  };

  if (loading) {
    return <p className="text-sm text-gray-500 dark:text-gray-400">Loading members...</p>;
  }

  if (error) {
    return (
      <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-4">
        <p className="text-sm text-red-700 dark:text-red-300">{error}</p>
        <button onClick={fetchData} className="mt-2 text-sm text-red-600 dark:text-red-400 underline">
          Retry
        </button>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Members list */}
      <div className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-border">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Members</h2>
          {isOwnerOrAdmin && (
            <button
              onClick={() => setInviteOpen(true)}
              className="text-sm font-medium text-brand-600 dark:text-brand-400 hover:text-brand-700 dark:hover:text-brand-300"
            >
              Invite Member
            </button>
          )}
        </div>
        <div className="divide-y divide-gray-100 dark:divide-border">
          {members.map((member) => (
            <div key={member.user_id} className="flex items-center justify-between px-4 py-3">
              <div className="flex items-center gap-3 min-w-0">
                <div className="min-w-0">
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100 truncate">
                    {member.name || member.email}
                    {member.user_id === user?.id && (
                      <span className="ml-1.5 text-xs text-gray-400 dark:text-gray-500">(you)</span>
                    )}
                  </p>
                  {member.name && (
                    <p className="text-xs text-gray-500 dark:text-gray-400 truncate">{member.email}</p>
                  )}
                </div>
              </div>
              <div className="flex items-center gap-2 flex-shrink-0">
                {member.role === "owner" ? (
                  roleBadge("owner")
                ) : isOwner && member.user_id !== user?.id ? (
                  <>
                    <select
                      value={member.role}
                      onChange={(e) => handleRoleChange(member.user_id, e.target.value)}
                      disabled={pendingRoles.has(member.user_id)}
                      className="text-xs border border-gray-200 dark:border-border rounded-md px-2 py-1 bg-white dark:bg-surface-elevated text-gray-700 dark:text-gray-300 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      <option value="admin">admin</option>
                      <option value="member">member</option>
                    </select>
                    <button
                      onClick={() => handleRemoveMember(member.user_id, member.email)}
                      className="text-xs text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300"
                    >
                      Remove
                    </button>
                  </>
                ) : member.user_id === user?.id ? (
                  <>
                    {roleBadge(member.role)}
                    <button
                      onClick={handleLeave}
                      className="text-xs text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300"
                    >
                      Leave
                    </button>
                  </>
                ) : (
                  roleBadge(member.role)
                )}
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Pending Invitations */}
      {isOwnerOrAdmin && invitations.length > 0 && (
        <div className="bg-white dark:bg-surface-card rounded-lg border border-gray-200 dark:border-border">
          <div className="px-4 py-3 border-b border-gray-200 dark:border-border">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Pending Invitations</h2>
          </div>
          <div className="divide-y divide-gray-100 dark:divide-border">
            {invitations.map((inv) => (
              <div key={inv.id} className="flex items-center justify-between px-4 py-3">
                <div className="min-w-0">
                  <p className="text-sm text-gray-900 dark:text-gray-100 truncate">{inv.email}</p>
                  <p className="text-xs text-gray-500 dark:text-gray-400">
                    {roleBadge(inv.role)}
                    <span className="ml-2">expires in {daysUntil(inv.expires_at)} days</span>
                  </p>
                </div>
                <button
                  onClick={() => handleRevokeInvitation(inv.id)}
                  className="text-xs text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300 flex-shrink-0"
                >
                  Revoke
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Invite Dialog */}
      {inviteOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="fixed inset-0 bg-black/50" onClick={closeInviteDialog} />
          <div className="relative bg-white dark:bg-surface-card rounded-lg shadow-xl p-6 w-full max-w-md">
            <h3 className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-4">Invite Member</h3>

            <div className="space-y-4">
                <div>
                  <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                    Email address
                  </label>
                  <input
                    type="email"
                    value={inviteEmail}
                    onChange={(e) => setInviteEmail(e.target.value)}
                    placeholder="colleague@example.com"
                    className="w-full text-sm border border-gray-200 dark:border-border rounded-md px-3 py-2 bg-white dark:bg-surface-elevated text-gray-900 dark:text-gray-100 placeholder-gray-400"
                  />
                </div>
                <div>
                  <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                    Role
                  </label>
                  <select
                    value={inviteRole}
                    onChange={(e) => setInviteRole(e.target.value)}
                    className="w-full text-sm border border-gray-200 dark:border-border rounded-md px-3 py-2 bg-white dark:bg-surface-elevated text-gray-700 dark:text-gray-300"
                  >
                    <option value="admin">Admin</option>
                    <option value="member">Member</option>
                  </select>
                </div>
                <div className="flex gap-2 pt-2">
                  <button
                    onClick={closeInviteDialog}
                    className="flex-1 text-sm font-medium px-3 py-2 border border-gray-200 dark:border-border rounded-md text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-surface-elevated"
                  >
                    Cancel
                  </button>
                  <button
                    onClick={handleSendInvite}
                    disabled={!inviteEmail.trim()}
                    className="flex-1 text-sm font-medium px-3 py-2 bg-brand-600 text-white rounded-md hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    Send Invite
                  </button>
                </div>
              </div>
          </div>
        </div>
      )}
    </div>
  );
}
