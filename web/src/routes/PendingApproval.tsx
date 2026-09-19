import { useEffect, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import { PENDING_EMAIL_KEY } from '../auth/SignInDialog';
import { Icon } from '../components/Icon';
import { Panel } from '../components/Panel';

/**
 * screens/06-pending-approval. Copy is verbatim from the current app. The
 * page polls /auth/me every 30 s; a pending password account has no session
 * (login answers 403 until approval), so the poll only flips the page to
 * the approved state when a session exists — otherwise the visitor signs in
 * again once the approval email arrives.
 */
export function PendingApprovalRoute() {
  const navigate = useNavigate();
  const { user, openSignIn, signOut } = useAuth();
  const [email] = useState(() => user?.email ?? sessionStorage.getItem(PENDING_EMAIL_KEY) ?? '');

  useEffect(() => {
    document.title = 'Pending approval — RehearseKit';
  }, []);

  const poll = useQuery({
    queryKey: ['auth', 'pending-poll'],
    queryFn: async () => {
      try {
        return await api.me();
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) return null;
        throw err;
      }
    },
    refetchInterval: 30_000,
    refetchIntervalInBackground: true,
    retry: false,
  });

  const approved = (poll.data ?? user)?.status === 'active';
  const shownEmail = poll.data?.email ?? email;

  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
      <div style={{ maxWidth: 620, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-9)', paddingTop: 'var(--rk-space-11)' }}>
        <Panel style={{ padding: 'var(--rk-space-11) var(--rk-space-10)' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-7)' }} aria-live="polite">
            {approved ? (
              <>
                <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-5)' }}>
                  <span style={{ width: 28, height: 28, borderRadius: 'var(--rk-radius-round)', background: 'var(--rk-color-status-success)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--rk-color-on-copper)' }}>
                    <Icon name="check" size={16} />
                  </span>
                  <span className="rk-eyebrow">ACCOUNT APPROVED</span>
                </div>
                <h1 className="rk-title">You’re in</h1>
                <EmailRow email={shownEmail} />
                <p style={{ margin: 0, fontSize: 'var(--rk-font-size-xl)', color: 'var(--rk-color-ink-muted)' }}>
                  An administrator approved this account. Everything is unlocked — upload a track and the stems come back in a few minutes.
                </p>
                <div style={{ display: 'flex', gap: 'var(--rk-space-5)', flexWrap: 'wrap' }}>
                  <button className="rk-btn rk-btn--primary rk-btn--lg" type="button" onClick={() => void navigate({ to: '/' })}>
                    Upload a track
                  </button>
                  <button className="rk-btn rk-btn--lg" type="button" onClick={() => void navigate({ to: '/jobs' })}>
                    Go to Job History
                  </button>
                </div>
              </>
            ) : (
              <>
                <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-5)' }}>
                  <span style={{ width: 28, height: 28, borderRadius: 'var(--rk-radius-round)', background: 'var(--rk-color-status-warning)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#332c1c' }}>
                    <Icon name="clock" size={16} />
                  </span>
                  <span className="rk-eyebrow">ACCOUNT CREATED SUCCESSFULLY</span>
                </div>
                <h1 className="rk-title">Account Pending Approval</h1>
                {shownEmail && <EmailRow email={shownEmail} />}
                <p style={{ margin: 0, fontSize: 'var(--rk-font-size-xl)', color: 'var(--rk-color-ink-muted)' }}>
                  Our administrators will review your account within 24-48 hours.
                </p>
                <div className="rk-flat" style={{ padding: 'var(--rk-space-8)', display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-6)' }}>
                  <h2 className="rk-h2" style={{ fontSize: 'var(--rk-font-size-xl)' }}>
                    What happens next?
                  </h2>
                  <ul style={{ margin: 0, paddingLeft: 'var(--rk-space-9)', display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)', fontSize: 'var(--rk-font-size-base)', color: 'var(--rk-color-ink-muted)', listStyle: 'disc' }}>
                    <li>You will get an email notification once your account is approved.</li>
                    <li>You will have full access to RehearseKit after approval.</li>
                  </ul>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 'var(--rk-space-6)', flexWrap: 'wrap' }}>
                  <span className="rk-help">This page refreshes itself — you can leave it open.</span>
                  {user ? (
                    <button
                      className="rk-btn"
                      type="button"
                      onClick={async () => {
                        await signOut();
                        void navigate({ to: '/' });
                      }}
                    >
                      <Icon name="logout" /> Log out
                    </button>
                  ) : (
                    <button className="rk-btn" type="button" onClick={() => openSignIn(() => void navigate({ to: '/jobs' }))}>
                      Sign in
                    </button>
                  )}
                </div>
              </>
            )}
          </div>
        </Panel>
      </div>
    </main>
  );
}

function EmailRow({ email }: { email: string }) {
  return (
    <div className="rk-flat" style={{ padding: 'var(--rk-space-6) var(--rk-space-7)', display: 'flex', alignItems: 'center', gap: 'var(--rk-space-5)' }}>
      <Icon name="mail" size={18} />
      <span className="rk-mono">{email || '—'}</span>
    </div>
  );
}
