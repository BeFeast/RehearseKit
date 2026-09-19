import { useEffect, useRef, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useAuth } from '../auth/AuthProvider';
import { Skeleton } from '../components/Badge';

/**
 * Signed-in only. There is no full-page /login (design gap), so an
 * anonymous visitor gets the sign-in dialog over an empty shell and the
 * page renders once a session exists. The dialog opens once per visit to
 * the page; dismissing it leaves the shell with an "Open sign in" link
 * rather than reopening in a loop.
 */
export function RequireUser({ children, title = 'Sign in to continue', admin = false }: { children: ReactNode; title?: string; admin?: boolean }) {
  const { user, loading, openSignIn } = useAuth();
  const navigate = useNavigate();
  const prompted = useRef(false);

  useEffect(() => {
    if (!loading && !user && !prompted.current) {
      prompted.current = true;
      openSignIn();
    }
  }, [loading, user, openSignIn]);

  useEffect(() => {
    if (user && user.status === 'pending') void navigate({ to: '/pending-approval' });
    else if (user && admin && user.role !== 'admin') void navigate({ to: '/jobs' });
  }, [user, admin, navigate]);

  if (loading) {
    return (
      <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)' }}>
        <Skeleton height={27} width="32%" />
        <Skeleton height={12} width="20%" style={{ marginTop: 'var(--rk-space-5)' }} />
      </main>
    );
  }
  if (!user) {
    return (
      <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
        <h1 className="rk-title">{title}</h1>
        <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
          Sign in to see this page.{' '}
          <a href="#" onClick={(e) => { e.preventDefault(); openSignIn(); }}>
            Open sign in
          </a>
          .
        </p>
      </main>
    );
  }
  if (admin && user.role !== 'admin') return null;
  return <>{children}</>;
}
