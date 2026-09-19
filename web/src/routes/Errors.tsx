import { useEffect, useMemo } from 'react';
import { useNavigate, useRouter } from '@tanstack/react-router';
import { useAuth } from '../auth/AuthProvider';
import { lastSeenRequestId } from '../api/client';
import { Icon } from '../components/Icon';
import { ILLUSTRATIONS } from '../components/EmptyState';

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
      <div
        style={{
          maxWidth: 620,
          margin: 'var(--rk-space-14) auto 0',
          textAlign: 'center',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          gap: 'var(--rk-space-7)',
        }}
      >
        {children}
      </div>
    </main>
  );
}

/** screens/09-not-found/index.html */
export function NotFoundScreen() {
  const navigate = useNavigate();
  const { user } = useAuth();
  useEffect(() => {
    document.title = 'Not found — RehearseKit';
  }, []);
  return (
    <Shell>
      <img src={ILLUSTRATIONS.error} alt="" width={176} height={128} />
      <div className="rk-mono" style={{ fontSize: 'var(--rk-font-size-sm)', letterSpacing: 'var(--rk-tracking-widest)', color: 'var(--rk-color-ink-muted)' }}>
        ERROR 404
      </div>
      <h1 className="rk-title">That page does not exist</h1>
      <p style={{ margin: 0, maxWidth: '52ch', fontSize: 'var(--rk-font-size-xl)', color: 'var(--rk-color-ink-muted)' }}>
        The link may be wrong, or the job it pointed at was deleted. Anonymous job links expire after 24 hours.
      </p>
      <div style={{ display: 'flex', gap: 'var(--rk-space-5)', flexWrap: 'wrap', justifyContent: 'center' }}>
        {user ? (
          <button className="rk-btn rk-btn--primary rk-btn--lg" type="button" onClick={() => void navigate({ to: '/jobs' })}>
            Go to Job History
          </button>
        ) : (
          <button className="rk-btn rk-btn--primary rk-btn--lg" type="button" onClick={() => void navigate({ to: '/' })}>
            Go home
          </button>
        )}
        <button className="rk-btn rk-btn--lg" type="button" onClick={() => void navigate({ to: '/' })}>
          Upload a track
        </button>
      </div>
    </Shell>
  );
}

/** screens/09-not-found/state-error-boundary.html */
export function ErrorScreen({ error, reset }: { error: unknown; reset?: () => void }) {
  const navigate = useNavigate();
  const router = useRouter();
  const { user } = useAuth();
  const incident = useMemo(() => lastSeenRequestId() ?? crypto.randomUUID(), []);
  useEffect(() => {
    document.title = 'Something went wrong — RehearseKit';
    console.error('[rk] route error', error);
  }, [error]);
  const err = error instanceof Error ? error : new Error(String(error));
  const stackLine = (err.stack ?? '').split('\n').slice(0, 2).join('\n');
  return (
    <Shell>
      <img src={ILLUSTRATIONS.error} alt="" width={176} height={128} />
      <div className="rk-mono" style={{ fontSize: 'var(--rk-font-size-sm)', letterSpacing: 'var(--rk-tracking-widest)', color: 'var(--rk-color-ink-muted)' }}>
        UNEXPECTED ERROR
      </div>
      <h1 className="rk-title" role="alert">
        Something went wrong
      </h1>
      <p style={{ margin: 0, maxWidth: '52ch', fontSize: 'var(--rk-font-size-xl)', color: 'var(--rk-color-ink-muted)' }}>
        This page failed to render. Your jobs and files are not affected — nothing was lost.
      </p>
      <div style={{ display: 'flex', gap: 'var(--rk-space-5)', flexWrap: 'wrap', justifyContent: 'center' }}>
        <button
          className="rk-btn rk-btn--primary rk-btn--lg"
          type="button"
          onClick={() => {
            reset?.();
            void router.invalidate();
          }}
        >
          <Icon name="refresh" size={18} /> Try again
        </button>
        <button className="rk-btn rk-btn--lg" type="button" onClick={() => void navigate({ to: user ? '/jobs' : '/' })}>
          {user ? 'Go to Job History' : 'Go home'}
        </button>
      </div>
      <details className="rk-detail" style={{ marginTop: 'var(--rk-space-6)', textAlign: 'left', width: '100%' }}>
        <summary className="rk-help" style={{ cursor: 'pointer' }}>
          Technical detail
        </summary>
        <pre className="rk-mono">
          {stackLine || `${err.name}: ${err.message}`}
          {'\n'}request id: {incident}
        </pre>
      </details>
    </Shell>
  );
}
