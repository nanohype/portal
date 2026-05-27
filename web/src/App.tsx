import { ErrorBoundary } from "react-error-boundary";
import { AppLayout } from "@/components/layout/AppLayout";
import { ErrorFallback } from "@/components/ErrorFallback";
import { WorkspaceList } from "@/components/workspace/WorkspaceList";
import { WorkspaceDetail } from "@/components/workspace/WorkspaceDetail";
import { RunView } from "@/components/run/RunView";
import { LoginPage } from "@/routes/Login";
import { AuthCallbackPage } from "@/routes/AuthCallback";
import { TeamsPage } from "@/components/teams/TeamsPage";
import { UsersPage } from "@/components/users/UsersPage";
import { AuditLogPage } from "@/components/audit/AuditLogPage";
import { PipelineList } from "@/components/pipeline/PipelineList";
import { PipelineDetail } from "@/components/pipeline/PipelineDetail";
import { PipelineRunView } from "@/components/pipeline/PipelineRunView";
import { AccountList } from "@/components/account/AccountList";
import { AccountDetail } from "@/components/account/AccountDetail";
import { ClusterList } from "@/components/cluster/ClusterList";
import { ClusterDetail } from "@/components/cluster/ClusterDetail";
import { TenantList } from "@/components/tenant/TenantList";
import { TenantDetail } from "@/components/tenant/TenantDetail";
import { TemplateList } from "@/components/template/TemplateList";
import { CatalogPage } from "@/components/catalog/CatalogPage";
import { OrgSettings } from "@/components/settings/OrgSettings";
import { useAuth } from "@/hooks/useAuth";
import { useLocation, navigate } from "@/hooks/useNavigate";
import { Spinner } from "@/components/ui/spinner";
import { FileQuestion } from "lucide-react";
import { Link } from "@/components/ui/link";

function resolveRoute(location: string) {
  const path = location.split("?")[0];

  if (path === "/login") return { page: "login" as const };
  if (path === "/auth/callback") return { page: "callback" as const };

  const pipelineRunMatch = path.match(
    /^\/pipelines\/([^/]+)\/runs\/([^/]+)/
  );
  if (pipelineRunMatch)
    return {
      page: "pipeline-run" as const,
      pipelineId: pipelineRunMatch[1],
      runId: pipelineRunMatch[2],
    };

  const pipelineMatch = path.match(/^\/pipelines\/([^/]+)/);
  if (pipelineMatch)
    return { page: "pipeline" as const, pipelineId: pipelineMatch[1] };

  if (path === "/pipelines") return { page: "pipelines" as const };

  const runMatch = path.match(/^\/workspaces\/([^/]+)\/runs\/([^/]+)/);
  if (runMatch)
    return {
      page: "run" as const,
      workspaceId: runMatch[1],
      runId: runMatch[2],
    };

  const wsMatch = path.match(/^\/workspaces\/([^/]+)/);
  if (wsMatch)
    return { page: "workspace" as const, workspaceId: wsMatch[1] };

  const accountMatch = path.match(/^\/accounts\/([^/]+)/);
  if (accountMatch)
    return { page: "account" as const, accountId: accountMatch[1] };
  if (path === "/accounts") return { page: "accounts" as const };

  const clusterMatch = path.match(/^\/clusters\/([^/]+)/);
  if (clusterMatch)
    return { page: "cluster" as const, clusterId: clusterMatch[1] };
  if (path === "/clusters") return { page: "clusters" as const };

  const tenantMatch = path.match(/^\/tenants\/([^/]+)/);
  if (tenantMatch)
    return { page: "tenant" as const, tenantId: tenantMatch[1] };
  if (path === "/tenants") return { page: "tenants" as const };

  if (path === "/templates") return { page: "templates" as const };
  if (path === "/catalog") return { page: "catalog" as const };

  if (path === "/teams") return { page: "teams" as const };
  if (path === "/users") return { page: "users" as const };
  if (path === "/audit-logs") return { page: "audit-logs" as const };
  if (path === "/settings") return { page: "settings" as const };
  if (path === "/") return { page: "home" as const };
  return { page: "not-found" as const };
}

function NotFoundPage() {
  return (
    <div className="flex flex-col items-center justify-center py-20">
      <FileQuestion className="w-12 h-12 text-muted-foreground mb-4" />
      <h1 className="text-xl font-bold mb-2">Page not found</h1>
      <p className="text-sm text-muted-foreground mb-4">
        The page you're looking for doesn't exist.
      </p>
      <Link
        href="/"
        className="text-sm text-primary hover:underline"
      >
        Back to dashboard
      </Link>
    </div>
  );
}

export function App() {
  const location = useLocation();
  const route = resolveRoute(location);
  const { user, isLoading } = useAuth();

  // Public routes
  if (route.page === "login") return <LoginPage />;
  if (route.page === "callback") return <AuthCallbackPage />;

  // Auth loading
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background">
        <Spinner className="w-8 h-8" />
      </div>
    );
  }

  // Not logged in
  if (!user) {
    navigate("/login");
    return null;
  }

  return (
    <AppLayout>
      <ErrorBoundary FallbackComponent={ErrorFallback} onReset={() => window.location.reload()}>
        {route.page === "home" && <WorkspaceList />}
        {route.page === "pipelines" && <PipelineList />}
        {route.page === "pipeline" && (
          <PipelineDetail pipelineId={route.pipelineId!} />
        )}
        {route.page === "pipeline-run" && (
          <PipelineRunView
            pipelineId={route.pipelineId!}
            runId={route.runId!}
          />
        )}
        {route.page === "workspace" && (
          <WorkspaceDetail workspaceId={route.workspaceId!} />
        )}
        {route.page === "run" && (
          <RunView
            workspaceId={route.workspaceId!}
            runId={route.runId!}
          />
        )}
        {route.page === "accounts" && <AccountList />}
        {route.page === "account" && (
          <AccountDetail accountId={route.accountId!} />
        )}
        {route.page === "clusters" && <ClusterList />}
        {route.page === "cluster" && (
          <ClusterDetail clusterId={route.clusterId!} />
        )}
        {route.page === "tenants" && <TenantList />}
        {route.page === "tenant" && (
          <TenantDetail tenantId={route.tenantId!} />
        )}
        {route.page === "templates" && <TemplateList />}
        {route.page === "catalog" && <CatalogPage />}
        {route.page === "teams" && <TeamsPage />}
        {route.page === "users" && <UsersPage />}
        {route.page === "audit-logs" && <AuditLogPage />}
        {route.page === "settings" && <OrgSettings />}
        {route.page === "not-found" && <NotFoundPage />}
      </ErrorBoundary>
    </AppLayout>
  );
}
