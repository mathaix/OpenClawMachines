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
import { BrowserVMsPage } from "./pages/BrowserVMsPage";
import { BrowserVMLivePage } from "./pages/BrowserVMLivePage";
import { BrowserVMDetailPage } from "./pages/BrowserVMDetailPage";
import { OnboardingWizard } from "./pages/OnboardingWizard";
import { SignedOut } from "./pages/SignedOut";
import { CliAuth } from "./pages/CliAuth";
import { InvitationAccept } from "./pages/InvitationAccept";
import { LoginFirebase } from "./pages/LoginFirebase";

const Blog = lazy(() => import("./pages/Blog"));
const BlogPost = lazy(() => import("./pages/BlogPost"));
const Docs = lazy(() => import("./pages/Docs"));
const DocsPage = lazy(() => import("./pages/DocsPage"));
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
      <Route path="/pricing" element={<Navigate to="/" replace />} />
      <Route path="/docs" element={<Suspense fallback={<div className="min-h-screen bg-gray-950" />}><Docs /></Suspense>} />
      <Route path="/docs/:slug" element={<Suspense fallback={<div className="min-h-screen bg-gray-950" />}><DocsPage /></Suspense>} />
      <Route path="/blog" element={<Suspense fallback={<div className="min-h-screen bg-gray-950" />}><Blog /></Suspense>} />
      <Route path="/blog/:slug" element={<Suspense fallback={<div className="min-h-screen bg-gray-950" />}><BlogPost /></Suspense>} />
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
        <Route path="/settings" element={<Settings />} />
        <Route path="/settings/:tab" element={<Settings />} />
        <Route path="/browser-vms" element={<BrowserVMsPage />} />
        <Route path="/browser-vms/:id" element={<BrowserVMDetailPage />} />
      </Route>
    </Routes>
  );
}
