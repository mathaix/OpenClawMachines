import { lazy, Suspense } from "react";
import { Routes, Route, Navigate, useParams } from "react-router-dom";
import { useAuth } from "./lib/auth";
import { AppShell } from "./components/AppShell";
import { Admin } from "./pages/Admin";
import { Dashboard } from "./pages/Dashboard";
import { Landing } from "./pages/Landing";
import { MachineView } from "./pages/MachineView";
import { MachineWorkspace } from "./pages/MachineWorkspace";
import { Settings } from "./pages/Settings";
import { Observability } from "./pages/Observability";
import { WorkspaceIntegrations } from "./pages/WorkspaceIntegrations";
import { DefaultWorkspaceIntegrationsRedirect, WorkspaceOverview, Workspaces } from "./pages/Workspaces";
import { BrowserVMsPage } from "./pages/BrowserVMsPage";
import { BrowserVMLivePage } from "./pages/BrowserVMLivePage";
import { BrowserVMDetailPage } from "./pages/BrowserVMDetailPage";
import { OnboardingWizard } from "./pages/OnboardingWizard";
import { SignedOut } from "./pages/SignedOut";
import { CliAuth } from "./pages/CliAuth";
import { InvitationAccept } from "./pages/InvitationAccept";
import { LoginFirebase } from "./pages/LoginFirebase";

const ChatPage = lazy(() => import("./pages/ChatPage"));

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { user, loading } = useAuth();
  if (loading) return <div className="flex items-center justify-center h-screen bg-gray-950" />;
  if (!user) {
    // Redirect to Firebase login page with return URL
    const returnPath = window.location.pathname + window.location.search;
    window.location.href = `/login?return=${encodeURIComponent(returnPath)}`;
    return <div className="flex items-center justify-center h-screen bg-gray-950" />;
  }
  return <>{children}</>;
}

function GatewayRedirect() {
  const { id } = useParams<{ id: string }>();
  return <Navigate to={`/workspace/${id}?view=gateway`} replace />;
}

const chatFallback = (
  <div className="flex items-center justify-center h-32">
    <p className="text-text-tertiary">Loading...</p>
  </div>
);

export function App() {
  return (
    <Routes>
      <Route path="/" element={<Landing />} />
      <Route path="/docs" element={<Navigate to="/" replace />} />
      <Route path="/docs/:slug" element={<Navigate to="/" replace />} />
      <Route path="/blog" element={<Navigate to="/" replace />} />
      <Route path="/blog/:slug" element={<Navigate to="/" replace />} />
      <Route path="/signed-out" element={<SignedOut />} />
      <Route path="/login" element={<LoginFirebase />} />
      <Route path="/cli-auth" element={<CliAuth />} />
      <Route path="/welcome" element={<ProtectedRoute><OnboardingWizard /></ProtectedRoute>} />
      <Route path="/invitations/:token" element={<InvitationAccept />} />
      {/* Full-screen views (outside AppShell) */}
      <Route
        path="/workspace/:id"
        element={
          <ProtectedRoute>
            <MachineWorkspace />
          </ProtectedRoute>
        }
      />
      <Route
        path="/workspace/:id/gateway"
        element={<GatewayRedirect />}
      />
      <Route
        path="/browser-vms/:id/live"
        element={
          <ProtectedRoute>
            <BrowserVMLivePage />
          </ProtectedRoute>
        }
      />
      {/* AppShell layout routes */}
      <Route
        element={
          <ProtectedRoute>
            <AppShell />
          </ProtectedRoute>
        }
      >
        <Route path="/dashboard" element={<Dashboard />} />
        <Route path="/dashboard/admin" element={<Admin />} />
        <Route path="/machines/:id" element={<MachineView />} />
        <Route path="/machines/:id/:tab" element={<MachineView />} />
        <Route path="/chat" element={<Suspense fallback={chatFallback}><ChatPage /></Suspense>} />
        <Route path="/chat/:machineId" element={<Suspense fallback={chatFallback}><ChatPage /></Suspense>} />
        <Route path="/observability" element={<Observability />} />
        <Route path="/workspaces" element={<Workspaces />} />
        <Route path="/workspaces/:workspaceId" element={<WorkspaceOverview />} />
        <Route path="/workspaces/:workspaceId/integrations" element={<WorkspaceIntegrations />} />
        <Route path="/workspaces/:workspaceId/integrations/:catalogId" element={<WorkspaceIntegrations />} />
        <Route path="/integrations" element={<DefaultWorkspaceIntegrationsRedirect />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/settings/:tab" element={<Settings />} />
        <Route path="/browser-vms" element={<BrowserVMsPage />} />
        <Route path="/browser-vms/:id" element={<BrowserVMDetailPage />} />
      </Route>
    </Routes>
  );
}
