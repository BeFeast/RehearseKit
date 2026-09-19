import { useEffect, useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import * as api from '../api';
import { ApiError } from '../api/client';
import { Dialog } from '../components/Dialog';
import { Icon } from '../components/Icon';
import { useAuth } from './AuthProvider';

type Mode = 'signin' | 'register';
type Banner =
  | { kind: 'wrong' }
  | { kind: 'not-approved' }
  | { kind: 'inactive' }
  | { kind: 'google-blocked' }
  | { kind: 'google-soon' }
  | { kind: 'error'; message: string };

export const PENDING_EMAIL_KEY = 'rk.pendingEmail';

/**
 * Sign-in dialog (screens/05-sign-in): Google first, OR, email + password,
 * links to create an account. Registration swaps the same shell to a form
 * with name and password confirmation and ends on /pending-approval.
 */
export function SignInDialog() {
  const { signInOpen, closeSignIn, onSignedIn } = useAuth();
  const navigate = useNavigate();
  const [mode, setMode] = useState<Mode>('signin');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [banner, setBanner] = useState<Banner | null>(null);
  const [invalid, setInvalid] = useState(false);

  useEffect(() => {
    if (!signInOpen) {
      setMode('signin');
      setEmail('');
      setPassword('');
      setConfirm('');
      setName('');
      setBanner(null);
      setInvalid(false);
      setBusy(false);
    }
  }, [signInOpen]);

  const edit = (setter: (v: string) => void) => (v: string) => {
    setter(v);
    setBanner(null);
    setInvalid(false);
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setBanner(null);
    try {
      if (mode === 'signin') {
        const user = await api.login(email.trim(), password);
        await onSignedIn(user);
      } else {
        if (password !== confirm) {
          setBanner({ kind: 'error', message: 'The two passwords do not match.' });
          setInvalid(true);
          return;
        }
        await api.register(email.trim(), password, name.trim());
        sessionStorage.setItem(PENDING_EMAIL_KEY, email.trim());
        closeSignIn();
        void navigate({ to: '/pending-approval' });
      }
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === 'invalid_credentials') {
          setBanner({ kind: 'wrong' });
          setInvalid(true);
        } else if (err.code === 'pending_approval') {
          sessionStorage.setItem(PENDING_EMAIL_KEY, email.trim());
          setBanner({ kind: 'not-approved' });
        } else if (err.code === 'account_inactive') {
          setBanner({ kind: 'inactive' });
        } else {
          setBanner({ kind: 'error', message: err.message });
          setInvalid(err.status === 400 || err.status === 409);
        }
      } else {
        setBanner({ kind: 'error', message: 'Could not reach the server. Check your connection and try again.' });
      }
    } finally {
      setBusy(false);
    }
  }

  function google() {
    // POST /auth/google answers 501 in this build; the button stays so the
    // primary path is visible, and explains itself instead of failing silently.
    setBanner({ kind: 'google-soon' });
  }

  const title = mode === 'signin' ? 'Sign in to RehearseKit' : 'Create your account';
  const lede =
    mode === 'signin'
      ? 'Keep your jobs in one place and come back to them later.'
      : 'An administrator approves new accounts, usually within 24-48 hours.';

  return (
    <Dialog
      open={signInOpen}
      onClose={closeSignIn}
      pending={busy}
      title={
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 'var(--rk-space-6)' }}>
          <div>
            <h2 style={{ margin: 0 }}>{title}</h2>
            <p style={{ marginTop: 'var(--rk-space-3)' }}>{lede}</p>
          </div>
          <button className="rk-iconbtn" type="button" aria-label="Close" onClick={closeSignIn} disabled={busy}>
            <Icon name="x" />
          </button>
        </div>
      }
    >
      {banner && <SignInBanner banner={banner} />}
      <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-7)' }}>
        {mode === 'signin' && (
          <>
            <button className="rk-btn rk-btn--lg rk-btn--block" type="button" onClick={google} disabled={busy}>
              <Icon name="google" size={18} /> Continue with Google
            </button>
            <div className="rk-or">
              <i />
              OR
              <i />
            </div>
          </>
        )}
        {mode === 'register' && (
          <div className="rk-field">
            <label htmlFor="si-name">Full Name</label>
            <input className="rk-input" id="si-name" value={name} onChange={(e) => edit(setName)(e.target.value)} placeholder="Enter your full name" autoComplete="name" />
          </div>
        )}
        <div className="rk-field">
          <label htmlFor="si-email">Email</label>
          <input
            className="rk-input"
            id="si-email"
            type="email"
            value={email}
            onChange={(e) => edit(setEmail)(e.target.value)}
            placeholder="you@band.com"
            autoComplete="email"
            aria-invalid={invalid || undefined}
            required
          />
        </div>
        <div className="rk-field">
          <label htmlFor="si-password">Password</label>
          <input
            className="rk-input"
            id="si-password"
            type="password"
            value={password}
            onChange={(e) => edit(setPassword)(e.target.value)}
            autoComplete={mode === 'signin' ? 'current-password' : 'new-password'}
            aria-invalid={invalid || undefined}
            required
            minLength={mode === 'register' ? 8 : undefined}
          />
          {mode === 'register' && <span className="rk-help">At least 8 characters.</span>}
        </div>
        {mode === 'register' && (
          <div className="rk-field">
            <label htmlFor="si-confirm">Confirm password</label>
            <input className="rk-input" id="si-confirm" type="password" value={confirm} onChange={(e) => edit(setConfirm)(e.target.value)} autoComplete="new-password" required />
          </div>
        )}
        <button className="rk-btn rk-btn--primary rk-btn--lg rk-btn--block" type="submit" disabled={busy} aria-busy={busy || undefined}>
          {mode === 'signin' ? (busy ? 'Signing in…' : 'Sign in') : busy ? 'Creating account…' : 'Create account'}
        </button>
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 'var(--rk-space-6)' }}>
          {mode === 'signin' ? (
            <>
              <a href="#" style={{ fontSize: 'var(--rk-font-size-md)' }} onClick={(e) => { e.preventDefault(); setMode('register'); setBanner(null); setInvalid(false); }}>
                Create an account
              </a>
              <a
                href="#"
                style={{ fontSize: 'var(--rk-font-size-md)' }}
                onClick={(e) => {
                  e.preventDefault();
                  setBanner({ kind: 'error', message: 'Password reset is not available in this build — ask an administrator to reset it.' });
                }}
              >
                Forgot password
              </a>
            </>
          ) : (
            <a href="#" style={{ fontSize: 'var(--rk-font-size-md)' }} onClick={(e) => { e.preventDefault(); setMode('signin'); setBanner(null); setInvalid(false); }}>
              Already have an account? Sign in
            </a>
          )}
        </div>
      </form>
    </Dialog>
  );
}

function SignInBanner({ banner }: { banner: Banner }) {
  const navigate = useNavigate();
  const { closeSignIn } = useAuth();
  switch (banner.kind) {
    case 'wrong':
      return (
        <div className="rk-alert" role="alert">
          <Icon name="alert" size={18} />
          <div>
            <strong>That email and password do not match.</strong> Check the address, or reset your password. Accounts created with Google do not have a password — use Continue with Google.
          </div>
        </div>
      );
    case 'not-approved':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>This account is waiting for approval.</strong> You will get an email when an administrator approves it.{' '}
            <a
              href="#"
              onClick={(e) => {
                e.preventDefault();
                closeSignIn();
                void navigate({ to: '/pending-approval' });
              }}
            >
              See your approval status
            </a>
            .
          </div>
        </div>
      );
    case 'inactive':
      return (
        <div className="rk-alert" role="alert">
          <Icon name="alert" size={18} />
          <div>
            <strong>This account has been deactivated.</strong> Contact an administrator to restore access.
          </div>
        </div>
      );
    case 'google-blocked':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>The Google window was blocked.</strong> Allow pop-ups for this site and try again, or sign in with an email and password below.
          </div>
        </div>
      );
    case 'google-soon':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>Google sign-in is coming back soon.</strong> This build only supports email and password — use the form below.
          </div>
        </div>
      );
    case 'error':
      return (
        <div className="rk-alert" role="alert">
          <Icon name="alert" size={18} />
          <div>{banner.message}</div>
        </div>
      );
  }
}
