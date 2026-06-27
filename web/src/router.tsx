/* eslint-disable react-refresh/only-export-components -- route-tree wiring: route definitions + the exported router, not an HMR component boundary */
import { lazy, Suspense } from "react";
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

// Public entry pages stay eager — they're the first paint and tiny.
import { LoginPage } from "@/routes/Login";
import { AuthCallbackPage } from "@/routes/AuthCallback";

// In-app pages are lazy-loaded so each becomes its own chunk: the initial
// bundle no longer carries every detail view (xterm.js, cmdk, charts) up front.
// The Suspense boundary in AppLayoutRoute renders the fallback while a chunk loads.
const named = <T,>(p: Promise<Record<string, T>>, key: string) =>
  p.then((m) => ({ default: m[key] }));

const WorkspaceList = lazy(() => named(import("@/components/workspace/WorkspaceList"), "WorkspaceList"));
const WorkspaceDetail = lazy(() => named(import("@/components/workspace/WorkspaceDetail"), "WorkspaceDetail"));
const RunView = lazy(() => named(import("@/components/run/RunView"), "RunView"));
const PipelineList = lazy(() => named(import("@/components/pipeline/PipelineList"), "PipelineList"));
const PipelineDetail = lazy(() => named(import("@/components/pipeline/PipelineDetail"), "PipelineDetail"));
const PipelineRunView = lazy(() => named(import("@/components/pipeline/PipelineRunView"), "PipelineRunView"));
const AccountList = lazy(() => named(import("@/components/account/AccountList"), "AccountList"));
const AccountDetail = lazy(() => named(import("@/components/account/AccountDetail"), "AccountDetail"));
const ClusterList = lazy(() => named(import("@/components/cluster/ClusterList"), "ClusterList"));
const ClusterDetail = lazy(() => named(import("@/components/cluster/ClusterDetail"), "ClusterDetail"));
const TenantList = lazy(() => named(import("@/components/tenant/TenantList"), "TenantList"));
const TenantDetail = lazy(() => named(import("@/components/tenant/TenantDetail"), "TenantDetail"));
const TemplateList = lazy(() => named(import("@/components/template/TemplateList"), "TemplateList"));
const CatalogPage = lazy(() => named(import("@/components/catalog/CatalogPage"), "CatalogPage"));
const OpsPage = lazy(() => named(import("@/components/ops/OpsPage"), "OpsPage"));
const FleetOverview = lazy(() => named(import("@/components/fleet/FleetOverview"), "FleetOverview"));
const TeamsPage = lazy(() => named(import("@/components/teams/TeamsPage"), "TeamsPage"));
const UsersPage = lazy(() => named(import("@/components/users/UsersPage"), "UsersPage"));
const AuditLogPage = lazy(() => named(import("@/components/audit/AuditLogPage"), "AuditLogPage"));
const OrgSettings = lazy(() => named(import("@/components/settings/OrgSettings"), "OrgSettings"));

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
        onError={(error, info) =>
          // Name the throwing component in the console / pod logs so a render
          // crash is debuggable without reproducing it interactively.
          console.error("[ui] render error:", error, info.componentStack)
        }
      >
        {/* Catches the lazy route chunks while they load. */}
        <Suspense
          fallback={
            <div className="flex items-center justify-center py-20">
              <Spinner className="w-6 h-6" />
            </div>
          }
        >
          <Outlet />
        </Suspense>
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

const fleetRoute = r("/fleet", () => <FleetOverview />);
const opsRoute = r("/ops", () => <OpsPage />);
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
    fleetRoute,
    opsRoute,
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
