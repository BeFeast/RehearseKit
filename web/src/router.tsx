import { Outlet, createRootRoute, createRoute, createRouter } from '@tanstack/react-router';
import { Header } from './components/Header';
import { SignInDialog } from './auth/SignInDialog';
import { ErrorScreen, NotFoundScreen } from './routes/Errors';
import { LandingRoute } from './routes/Landing';
import { JobsListRoute, jobsSearchSchema } from './routes/JobsList';
import { JobDetailRoute } from './routes/JobDetail';
import { PendingApprovalRoute } from './routes/PendingApproval';
import { ProfileRoute } from './routes/Profile';
import { AdminUsersRoute, adminSearchSchema } from './routes/AdminUsers';

function Layout() {
  return (
    <>
      <Header />
      <Outlet />
      <SignInDialog />
    </>
  );
}

export const rootRoute = createRootRoute({
  component: Layout,
  notFoundComponent: NotFoundScreen,
  errorComponent: ErrorScreen,
});

export const landingRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: LandingRoute });

export const jobsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/jobs',
  component: JobsListRoute,
  validateSearch: jobsSearchSchema,
});

export const jobRoute = createRoute({ getParentRoute: () => rootRoute, path: '/jobs/$id', component: JobDetailRoute });

export const pendingRoute = createRoute({ getParentRoute: () => rootRoute, path: '/pending-approval', component: PendingApprovalRoute });

export const profileRoute = createRoute({ getParentRoute: () => rootRoute, path: '/profile', component: ProfileRoute });

export const adminUsersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/admin/users',
  component: AdminUsersRoute,
  validateSearch: adminSearchSchema,
});

const routeTree = rootRoute.addChildren([landingRoute, jobsRoute, jobRoute, pendingRoute, profileRoute, adminUsersRoute]);

export const router = createRouter({
  routeTree,
  defaultPreload: 'intent',
  scrollRestoration: true,
  defaultErrorComponent: ErrorScreen,
  defaultNotFoundComponent: NotFoundScreen,
});

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
