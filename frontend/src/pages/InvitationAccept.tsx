import { useEffect, useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { getInvitationPublic, acceptInvitation, declineInvitation } from "../lib/api";
import { useAuth } from "../lib/auth";

type InvitationData = {
  account_name: string;
  role: string;
  status: string;
  inviter_email: string;
  expires_at: string;
};

type PageState =
  | { kind: "loading" }
  | { kind: "invitation"; data: InvitationData }
  | { kind: "error"; message: string }
  | { kind: "accepted" }
  | { kind: "declined" }
  | { kind: "acting"; action: "accept" | "decline"; data: InvitationData };

export function InvitationAccept() {
  const { token } = useParams<{ token: string }>();
  const navigate = useNavigate();
  const { user, refreshAccounts } = useAuth();
  const [state, setState] = useState<PageState>({ kind: "loading" });

  // Phase 1: Load invitation details (public, no auth needed)
  useEffect(() => {
    if (!token) {
      setState({ kind: "error", message: "No invitation token provided." });
      return;
    }

    getInvitationPublic(token)
      .then((data) => {
        if (data.status === "accepted" || data.status === "declined") {
          setState({
            kind: "error",
            message: `This invitation has already been ${data.status}.`,
          });
        } else if (
          data.status === "expired" ||
          new Date(data.expires_at) < new Date()
        ) {
          setState({ kind: "error", message: "This invitation has expired." });
        } else {
          setState({ kind: "invitation", data });
        }
      })
      .catch((err) => {
        if (err?.status === 404) {
          setState({
            kind: "error",
            message: "Invitation not found or invalid.",
          });
        } else {
          setState({
            kind: "error",
            message: err?.message || "Failed to load invitation.",
          });
        }
      });
  }, [token]);

  // Phase 2: If user just logged in and we were waiting to accept, do it now
  useEffect(() => {
    if (!user || !token) return;
    const pending = sessionStorage.getItem("ocm_pending_invitation_action");
    if (pending === "accept") {
      sessionStorage.removeItem("ocm_pending_invitation_action");
      doAccept();
    } else if (pending === "decline") {
      sessionStorage.removeItem("ocm_pending_invitation_action");
      doDecline();
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user, token]);

  function redirectToLogin() {
    window.location.href = `/login?return=${encodeURIComponent(`/invitations/${token}`)}`;
  }

  async function doAccept() {
    if (!token) return;
    const data = state.kind === "invitation" || state.kind === "acting"
      ? (state as { data: InvitationData }).data
      : undefined;
    if (data) setState({ kind: "acting", action: "accept", data });

    try {
      const result = await acceptInvitation(token);
      localStorage.setItem("ocm_active_account_id", String(result.account_id));
      await refreshAccounts();
      setState({ kind: "accepted" });
      setTimeout(() => navigate("/dashboard"), 1500);
    } catch (err: unknown) {
      const error = err as { status?: number; message?: string };
      if (error?.status === 403) {
        setState({
          kind: "error",
          message: `This invitation was sent to a different email. You're signed in as ${user?.email ?? "a different account"}.`,
        });
      } else {
        setState({
          kind: "error",
          message: error?.message || "Failed to accept invitation.",
        });
      }
    }
  }

  async function doDecline() {
    if (!token) return;
    const data = state.kind === "invitation" || state.kind === "acting"
      ? (state as { data: InvitationData }).data
      : undefined;
    if (data) setState({ kind: "acting", action: "decline", data });

    try {
      await declineInvitation(token);
      setState({ kind: "declined" });
    } catch (err: unknown) {
      const error = err as { status?: number; message?: string };
      setState({
        kind: "error",
        message: error?.message || "Failed to decline invitation.",
      });
    }
  }

  function handleAccept(data: InvitationData) {
    if (!user) {
      // Not logged in — save intent and redirect to login
      sessionStorage.setItem("ocm_pending_invitation_action", "accept");
      redirectToLogin();
      return;
    }
    setState({ kind: "acting", action: "accept", data });
    doAccept();
  }

  function handleDecline(data: InvitationData) {
    if (!user) {
      sessionStorage.setItem("ocm_pending_invitation_action", "decline");
      redirectToLogin();
      return;
    }
    setState({ kind: "acting", action: "decline", data });
    doDecline();
  }

  function renderContent() {
    switch (state.kind) {
      case "loading":
        return (
          <div className="flex flex-col items-center gap-4 py-8">
            <div className="h-8 w-8 animate-spin rounded-full border-2 border-gray-300 border-t-brand-600" />
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Loading invitation...
            </p>
          </div>
        );

      case "invitation":
      case "acting": {
        const inv = state.data;
        const isActing = state.kind === "acting";
        return (
          <>
            <h1 className="text-xl font-semibold text-gray-900 dark:text-gray-100 mb-1">
              You've been invited
            </h1>
            <p className="text-sm text-gray-500 dark:text-gray-400 mb-6">
              {inv.inviter_email ? `${inv.inviter_email} invited you to join` : "You've been invited to join"}
            </p>
            <div className="bg-gray-50 dark:bg-gray-900 rounded-md p-4 mb-6 space-y-2">
              <div className="flex justify-between text-sm">
                <span className="text-gray-500 dark:text-gray-400">
                  Account
                </span>
                <span className="font-medium text-gray-900 dark:text-gray-100">
                  {inv.account_name}
                </span>
              </div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500 dark:text-gray-400">Role</span>
                <span className="font-medium text-gray-900 dark:text-gray-100 capitalize">
                  {inv.role}
                </span>
              </div>
              {inv.inviter_email && (
                <div className="flex justify-between text-sm">
                  <span className="text-gray-500 dark:text-gray-400">
                    Invited by
                  </span>
                  <span className="font-medium text-gray-900 dark:text-gray-100">
                    {inv.inviter_email}
                  </span>
                </div>
              )}
            </div>
            {!user && (
              <p className="text-xs text-gray-400 dark:text-gray-500 mb-4 text-center">
                You'll be asked to sign in before accepting.
              </p>
            )}
            <div className="flex gap-3">
              <button
                onClick={() => handleDecline(inv)}
                disabled={isActing}
                className="flex-1 px-4 py-2 rounded-lg border border-gray-300 dark:border-gray-600 text-sm font-medium text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-800 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {isActing && state.kind === "acting" && (state as { action: string }).action === "decline"
                  ? "Declining..."
                  : "Decline"}
              </button>
              <button
                onClick={() => handleAccept(inv)}
                disabled={isActing}
                className="flex-1 px-4 py-2 rounded-lg bg-brand-600 text-white text-sm font-medium hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {isActing && state.kind === "acting" && (state as { action: string }).action === "accept"
                  ? "Accepting..."
                  : "Accept Invitation"}
              </button>
            </div>
          </>
        );
      }

      case "accepted":
        return (
          <div className="text-center py-4">
            <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30">
              <svg
                className="h-6 w-6 text-green-600 dark:text-green-400"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth={2}
                stroke="currentColor"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M4.5 12.75l6 6 9-13.5"
                />
              </svg>
            </div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-1">
              Invitation accepted
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Redirecting to dashboard...
            </p>
          </div>
        );

      case "declined":
        return (
          <div className="text-center py-4">
            <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-1">
              Invitation declined
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400 mb-4">
              You have declined this invitation.
            </p>
            <button
              onClick={() => navigate("/")}
              className="text-sm text-brand-600 hover:text-brand-700 font-medium"
            >
              Go to homepage
            </button>
          </div>
        );

      case "error":
        return (
          <div className="text-center py-4">
            <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/30">
              <svg
                className="h-6 w-6 text-red-600 dark:text-red-400"
                fill="none"
                viewBox="0 0 24 24"
                strokeWidth={2}
                stroke="currentColor"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M6 18L18 6M6 6l12 12"
                />
              </svg>
            </div>
            <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100 mb-1">
              Unable to proceed
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400 mb-4">
              {state.message}
            </p>
            <button
              onClick={() => navigate("/")}
              className="text-sm text-brand-600 hover:text-brand-700 font-medium"
            >
              Go to homepage
            </button>
          </div>
        );
    }
  }

  return (
    <div className="min-h-screen bg-gray-50 dark:bg-gray-950 flex items-center justify-center p-4">
      <div className="bg-white dark:bg-surface-card rounded-lg shadow-lg border border-gray-200 dark:border-border p-8 w-full max-w-md">
        {renderContent()}
      </div>
    </div>
  );
}
