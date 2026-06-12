import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  redirect,
  Outlet,
  useParams,
  useLocation,
} from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { ErrorBoundary } from "react-error-boundary";
import { FileQuestion } from "lucide-react";

import { queryClient } from "@/lib/query-client";
import { registerRouter } from "@/lib/router-ref";
import { useAuthStore } from "@/stores/auth";
import { api } from "@/api/client";

import { AppLayout } from "@/components/layout/AppLayout";
import { ErrorFallback } from "@/components/ErrorFallback";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";

import { LoginPage } from "@/routes/Login";
import { AuthCallbackPage } from "@/routes/AuthCallback";
import { WorkspaceList } from "@/components/workspace/WorkspaceList";
import { WorkspaceDetail } from "@/components/workspace/WorkspaceDetail";
import { RunView } from "@/components/run/RunView";
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
import { TeamsPage } from "@/components/teams/TeamsPage";
import { UsersPage } from "@/components/users/UsersPage";
import { AuditLogPage } from "@/components/audit/AuditLogPage";
import { OrgSettings } from "@/components/settings/OrgSettings";

interface RouterContext {
  queryClient: QueryClient;
}

// ── full-screen states ──────────────────────────────────────────────
function PendingSpinner() {
  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <Spinner className="w-8 h-8" />
    </div>
  );
}

function NotFound() {
  return (
    <div className="min-h-screen flex flex-col items-center justify-center bg-background">
      <FileQuestion className="w-12 h-12 text-muted-foreground mb-4" />
      <h1 className="text-xl font-bold mb-2">Page not found</h1>
      <p className="text-sm text-muted-foreground mb-4">
        The page you're looking for doesn't exist.
      </p>
      <Link href="/" className="text-sm text-primary hover:underline">
        Back to dashboard
      </Link>
    </div>
  );
}

// Body-scoped variant of NotFound: renders inside AppLayout (keeps the sidebar/
// nav) for an authenticated user who lands on an unknown in-app path.
function NotFoundInApp() {
  return (
    <div className="flex flex-col items-center justify-center py-20 text-center animate-fade-up">
      <FileQuestion className="w-12 h-12 text-muted-foreground mb-4" />
      <h1 className="text-xl font-bold mb-2">Page not found</h1>
      <p className="text-sm text-muted-foreground mb-4">
        The page you're looking for doesn't exist.
      </p>
      <Link href="/" className="text-sm text-primary hover:underline">
        Back to dashboard
      </Link>
    </div>
  );
}

// ── root + public routes ────────────────────────────────────────────
const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => <Outlet />,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
});

const callbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/auth/callback",
  component: AuthCallbackPage,
});

// ── protected layout route: the auth gate lives here ────────────────
// beforeLoad resolves auth BEFORE the route renders, so there's no
// post-paint spinner flash and no navigate-during-render.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "_app",
  beforeLoad: async ({ context }) => {
    const { token, user } = useAuthStore.getState();
    if (!token) throw redirect({ to: "/login" });
    if (!user) {
      try {
        const me = await context.queryClient.ensureQueryData({
          queryKey: ["auth", "me"],
          queryFn: async () => {
            const { data, error } = await api.GET("/auth/me");
            if (error) throw error;
            return data;
          },
        });
        useAuthStore.getState().setUser(me);
      } catch {
        useAuthStore.getState().logout();
        throw redirect({ to: "/login" });
      }
    }
  },
  component: AppLayoutRoute,
});

function AppLayoutRoute() {
  // Reset the error boundary on navigation — otherwise one page that throws
  // leaves the boundary stuck on "something went wrong" for every subsequent
  // menu switch until a hard refresh.
  const location = useLocation();
  return (
    <AppLayout>
      <ErrorBoundary
        FallbackComponent={ErrorFallback}
        resetKeys={[location.pathname]}
        onReset={() => window.location.reload()}
      >
        <Outlet />
      </ErrorBoundary>
    </AppLayout>
  );
}

// ── protected children ──────────────────────────────────────────────
const r = <TPath extends string>(
  path: TPath,
  component: () => React.ReactNode,
) => createRoute({ getParentRoute: () => appRoute, path, component });

const homeRoute = r("/", () => <WorkspaceList />);

const workspaceRoute = r("/workspaces/$workspaceId", () => {
  const { workspaceId } = useParams({ strict: false });
  return <WorkspaceDetail workspaceId={workspaceId!} />;
});
const runRoute = r("/workspaces/$workspaceId/runs/$runId", () => {
  const { workspaceId, runId } = useParams({ strict: false });
  return <RunView workspaceId={workspaceId!} runId={runId!} />;
});

const pipelinesRoute = r("/pipelines", () => <PipelineList />);
const pipelineRoute = r("/pipelines/$pipelineId", () => {
  const { pipelineId } = useParams({ strict: false });
  return <PipelineDetail pipelineId={pipelineId!} />;
});
const pipelineRunRoute = r("/pipelines/$pipelineId/runs/$runId", () => {
  const { pipelineId, runId } = useParams({ strict: false });
  return <PipelineRunView pipelineId={pipelineId!} runId={runId!} />;
});

const accountsRoute = r("/accounts", () => <AccountList />);
const accountRoute = r("/accounts/$accountId", () => {
  const { accountId } = useParams({ strict: false });
  return <AccountDetail accountId={accountId!} />;
});

const clustersRoute = r("/clusters", () => <ClusterList />);
const clusterRoute = r("/clusters/$clusterId", () => {
  const { clusterId } = useParams({ strict: false });
  return <ClusterDetail clusterId={clusterId!} />;
});

const tenantsRoute = r("/tenants", () => <TenantList />);
const tenantRoute = r("/tenants/$tenantId", () => {
  const { tenantId } = useParams({ strict: false });
  return <TenantDetail tenantId={tenantId!} />;
});

const templatesRoute = r("/templates", () => <TemplateList />);
const catalogRoute = r("/catalog", () => <CatalogPage />);
const teamsRoute = r("/teams", () => <TeamsPage />);
const usersRoute = r("/users", () => <UsersPage />);
const auditRoute = r("/audit-logs", () => <AuditLogPage />);
const settingsRoute = r("/settings", () => <OrgSettings />);

// Catch-all UNDER the auth gate: unknown in-app paths keep the app chrome, and
// unauthenticated visitors are bounced to /login by appRoute.beforeLoad (the
// root-level defaultNotFoundComponent would do neither). Goes through r() like
// the rest so its splat param stays out of the loose useParams() union.
const notFoundRoute = r("$", NotFoundInApp);

const routeTree = rootRoute.addChildren([
  loginRoute,
  callbackRoute,
  appRoute.addChildren([
    homeRoute,
    workspaceRoute,
    runRoute,
    pipelinesRoute,
    pipelineRoute,
    pipelineRunRoute,
    accountsRoute,
    accountRoute,
    clustersRoute,
    clusterRoute,
    tenantsRoute,
    tenantRoute,
    templatesRoute,
    catalogRoute,
    teamsRoute,
    usersRoute,
    auditRoute,
    settingsRoute,
    notFoundRoute,
  ]),
]);

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPendingComponent: PendingSpinner,
  defaultNotFoundComponent: NotFound,
});

registerRouter(router);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
