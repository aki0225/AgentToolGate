import { Navigate, Route, Routes } from "react-router-dom";
import { AuthCallbackPage } from "./pages/AuthCallbackPage";
import { AuditLogsPage } from "./pages/AuditLogsPage";
import { ApprovalsPage } from "./pages/ApprovalsPage";
import { ConnectorsPage } from "./pages/ConnectorsPage";
import { DashboardPage } from "./pages/DashboardPage";
import { Layout } from "./components/Layout";
import { LoginPage } from "./pages/LoginPage";
import { PoliciesPage } from "./pages/PoliciesPage";
import { SecretsPage } from "./pages/SecretsPage";
import { ToolDetailPage } from "./pages/ToolDetailPage";
import { ToolsPage } from "./pages/ToolsPage";
import { useAuth } from "./auth/AuthContext";
import { Card } from "./components/ui/card";
import { Toaster } from "./components/ui/sonner";
import { useI18n } from "./i18n";

function ProtectedLayout() {
  const auth = useAuth();
  if (!auth.me) {
    return <Navigate to="/login" replace />;
  }
  return <Layout />;
}

export function App() {
  const auth = useAuth();
  const { t } = useI18n();

  if (auth.isLoading) {
    return (
      <div className="grid min-h-screen place-items-center p-8">
        <Card className="w-full max-w-xl rounded-3xl p-7">
          <div className="inline-flex items-center gap-2 text-xs font-bold uppercase tracking-[0.18em] text-primary">
            {t("app.loading.kicker")}
          </div>
          <h1 className="mt-2 text-3xl font-bold tracking-tight text-foreground">{t("app.loading.title")}</h1>
          <p className="m-0 mt-3 text-sm text-muted-foreground">{t("app.loading.description")}</p>
        </Card>
      </div>
    );
  }

  return (
    <>
      <Toaster />
      <Routes>
        <Route path="/auth/callback" element={<AuthCallbackPage />} />
        <Route path="/login" element={<LoginPage />} />
        <Route element={<ProtectedLayout />}>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/tools" element={<ToolsPage />} />
          <Route path="/tools/:toolId" element={<ToolDetailPage />} />
          <Route path="/policies" element={<PoliciesPage />} />
          <Route path="/secrets" element={<SecretsPage />} />
          <Route path="/connectors" element={<ConnectorsPage />} />
          <Route path="/approvals" element={<ApprovalsPage />} />
          <Route path="/audit" element={<AuditLogsPage />} />
        </Route>
      </Routes>
    </>
  );
}
